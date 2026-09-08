package server

import (
	"slices"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"go.uber.org/zap"
)

// Store-all intake: a StoreAll node fetches new content when the uploads row
// reaches it, instead of waiting for a repair sweep to walk down to it.
//
// Upload-time replication never targets these nodes -- replicateToHosts caps
// its target list at the rendezvous top ReplicationFactor -- so repair is the
// only way they hear about anything. That sweep is a full keyset walk of the
// uploads table, which on a large catalog takes far longer than its one hour
// interval, so new content arrives on the cadence of a whole cycle.
//
// This is a fast path over that, not a replacement for it. Every way it can
// fail -- an op that arrives while the queue is full, a source that is down, a
// job still queued when the process restarts -- ends with the blob simply not
// fetched yet, which is where repair already finds it. Nothing here is allowed
// to be load-bearing, and that is what keeps it this small.
func (ss *MediorumServer) storeAllIntakeFromOp(op *crudr.Op, records interface{}) {
	// Nothing about this is cheap enough to run on a node that would then
	// throw the blob away: only StoreAll nodes hold content they are not
	// rendezvous-responsible for.
	if !ss.Config.StoreAll {
		return
	}
	if op == nil || op.Table != "uploads" || op.Action == crudr.ActionDelete {
		return
	}
	uploads, ok := records.(*[]*Upload)
	if !ok || uploads == nil {
		return
	}

	for _, upload := range *uploads {
		if upload == nil {
			continue
		}
		transcoded320 := upload.TranscodeResults["320"]
		for _, cid := range storeAllIntakeCIDs(upload) {
			ss.enqueueStoreAllIntake(upload, cid, cid == transcoded320)
		}
	}
}

// storeAllIntakeUnmirrored reports whether a blob is still held only by the
// node that produced it -- an empty mirror list, or one naming just that node.
//
// This is the edge the op stream does not carry. An uploads row is rewritten
// many times and every op repeats the same cids, so acting on all of them would
// offer the same blob a dozen times over. Held-only-by-its-producer is true for
// the op that first announces a blob and stops being true as soon as anyone
// else records a copy, which narrows that to about two offers per upload with
// no cache or window needed to synthesize it.
//
// It cannot miss the announcing op, because ops are immutable snapshots rather
// than current state: transcode writes TranscodedMirrors as exactly [self] or
// [] in the same update that first publishes the 320, and an upload is created
// with Mirrors set the same way, so those ops still read that way however far
// replication has moved on by the time a peer applies them.
//
// The empty case is not defensive. Whether a producer puts itself in the list
// depends on its rendezvous rank for the blob's own cid, so keying on "exactly
// one" alone silently drops every upload whose producer was not a placement
// target for what it produced -- measured at 7-12% on the local cluster, and
// higher the larger the network gets.
func storeAllIntakeUnmirrored(mirrors []string, producer string) bool {
	switch len(mirrors) {
	case 0:
		return true
	case 1:
		return mirrors[0] == producer
	default:
		return false
	}
}

// storeAllIntakeCIDs lists the blobs this op announces: those the upload names
// whose covering mirror list still shows only the producer.
//
// The two lists are consulted separately because they cover different blobs
// that become available at different moments -- the original exists from
// upload, the 320 only once transcode publishes it. One upload-level predicate
// would have a single answer for both and be wrong for one of them.
//
// Images fall out of the original arm. They never populate transcoded_mirrors,
// so a transcode-list predicate would be permanently true for them, and their
// only transcode result is the original's own cid anyway.
func storeAllIntakeCIDs(upload *Upload) []string {
	cids := []string{}
	add := func(cid string) {
		if cid != "" && !slices.Contains(cids, cid) {
			cids = append(cids, cid)
		}
	}

	// TranscodedBy is empty until a transcoder claims the row, and it is set in
	// the same write that flips the status to busy. So an op with the original's
	// cid and no transcoder is one written before transcode began -- the row as
	// its creator first published it. Every later op repeats that cid, and this
	// is what tells the two apart, taking the original arm from about five
	// offers per upload down to one.
	//
	// Images never populate it, so they keep announcing on the mirror list
	// alone, which is the only list that covers them.
	if upload.TranscodedBy == "" && storeAllIntakeUnmirrored(upload.Mirrors, upload.CreatedBy) {
		add(upload.OrigFileCID)
	}
	if storeAllIntakeUnmirrored(upload.TranscodedMirrors, upload.TranscodedBy) {
		for _, cid := range upload.TranscodeResults {
			// For an image this is the original's cid, which belongs to the
			// arm above; add() keeps it from being offered twice regardless.
			if cid != upload.OrigFileCID {
				add(cid)
			}
		}
	}
	return cids
}

// enqueueStoreAllIntake hands one blob to the async pull pool.
//
// This runs inside ApplyOp, which syncCoreMediorumOps calls in its loop over
// blocks, so everything here has to be non-blocking and free of I/O -- a slow
// callback does not slow store-all intake down, it stalls chain op sync for the
// whole node. That is why the presence check lives in runAsyncPull and not
// here: haveInMyBucket is a live bucket Exists, and paying one per op while
// replaying a backlog of blocks is exactly the stall this avoids.
func (ss *MediorumServer) enqueueStoreAllIntake(upload *Upload, cid string, transcoded bool) {
	sourceHost := ss.storeAllIntakeSource(upload, transcoded)
	if sourceHost == "" {
		// Nobody else is holding it yet -- typically our own op coming back
		// around, or a row whose mirrors have not been recorded. Repair has it.
		return
	}

	admission, err := ss.enqueueAsyncPull(asyncPullJob{
		sourceHost:     sourceHost,
		cid:            cid,
		placementHosts: upload.PlacementHosts,
		uploadID:       upload.ID,
		transcoded:     transcoded,
	})
	if err != nil {
		// The queue is shared with peer-driven pulls, and those have a sender
		// waiting on the answer. Dropping our own work is the right side to
		// lose on, and the sweep will offer it again.
		ss.logger.Debug("store-all intake skipped; pull queue full",
			zap.String("cid", cid), zap.Error(err))
		return
	}
	if admission == asyncPullQueued {
		ss.logger.Debug("store-all intake queued",
			zap.String("cid", cid),
			zap.String("sourceHost", sourceHost),
			zap.String("uploadID", upload.ID))
	}
}

// storeAllIntakeSource picks who to ask. One host, no fallback list: a miss
// costs nothing that repair does not already recover, and iterating hosts here
// would mean holding a pull worker across several failures.
//
// Mirrors are preferred over the node that produced the blob, because by the
// time a mirror is recorded it has been verified present, where TranscodedBy
// is only a claim about who ran the job.
func (ss *MediorumServer) storeAllIntakeSource(upload *Upload, transcoded bool) string {
	var candidates [][]string
	if transcoded {
		candidates = [][]string{upload.TranscodedMirrors, {upload.TranscodedBy}, upload.Mirrors}
	} else {
		candidates = [][]string{upload.Mirrors, {upload.CreatedBy}, upload.TranscodedMirrors}
	}
	for _, hosts := range candidates {
		for _, host := range hosts {
			if host != "" && host != ss.Config.Self.Host {
				return host
			}
		}
	}
	return ""
}
