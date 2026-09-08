package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"slices"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"go.uber.org/zap"
)

func (ss *MediorumServer) startReplicationWorkers(ctx context.Context) error {
	numWorkers := 3 // Run a 3 replication workers (arbitrary, can be tuned)

	ss.logger.Info("starting replication workers", zap.Int("count", numWorkers))

	// Start worker routines
	for i := range numWorkers {
		workerID := i
		go func() {
			ss.replicationWorker(ctx, workerID)
		}()
	}

	// Periodic job to find uploads that need replication
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ss.findMissedReplications()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) replicationWorker(ctx context.Context, workerID int) error {
	logger := ss.logger.With(zap.Int("worker", workerID), zap.String("task", "replication"))

	for {
		select {
		case upload, ok := <-ss.replicationWork:
			if !ok {
				return nil // channel closed
			}

			logger.Debug("replicating upload", zap.String("uploadID", upload.ID), zap.String("cid", upload.OrigFileCID))

			// Determine target replication count based on placement hosts
			targetReplicationCount := replicationTargetFor(upload, ss.Config.ReplicationFactor)

			// Replicate transcoded file if it exists and needs replication
			if _, hasTranscoded := upload.TranscodeResults["320"]; hasTranscoded && len(upload.TranscodedMirrors) < targetReplicationCount {
				if err := ss.replicateTranscode(ctx, upload); err != nil {
					logger.Error("transcoded replication failed", zap.String("uploadID", upload.ID), zap.Error(err))
				} else {
					logger.Info("transcoded replication completed", zap.String("uploadID", upload.ID))
				}
			}

			// Replicate original file if it needs replication
			if len(upload.Mirrors) < targetReplicationCount {
				if err := ss.replicateOriginal(ctx, upload); err != nil {
					logger.Error("original replication failed", zap.String("uploadID", upload.ID), zap.Error(err))
				} else {
					logger.Info("original replication completed", zap.String("uploadID", upload.ID))
				}
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) replicateOriginal(ctx context.Context, upload *Upload) error {
	if upload.OrigFileCID == "" {
		ss.logger.Warn("replicateUpload called with empty OrigFileCID; skipping replication", zap.String("uploadID", upload.ID))
		return nil
	}
	return ss.replicateToHosts(ctx, upload, upload.OrigFileCID, upload.Mirrors, false)
}

func (ss *MediorumServer) replicateTranscode(ctx context.Context, upload *Upload) error {
	// Get the transcoded CID from results
	transcodedCID, ok := upload.TranscodeResults["320"]
	if !ok || transcodedCID == "" {
		ss.logger.Warn("replicateTranscodedUpload called but no transcoded file exists; skipping replication", zap.String("uploadID", upload.ID))
		return nil
	}
	return ss.replicateToHosts(ctx, upload, transcodedCID, upload.TranscodedMirrors, true)
}

// pullHandoffOutcome is what one peer's answer to a pull request says about a
// transfer we handed it.
type pullHandoffOutcome int

const (
	// pullHandoffNone: not a handoff answer at all.
	pullHandoffNone pullHandoffOutcome = iota
	// pullHandoffFresh: the peer has just taken the transfer on, and we had no
	// outstanding handoff with it for this blob.
	pullHandoffFresh
	// pullHandoffRunning: the peer is still working on one it took earlier.
	pullHandoffRunning
	// pullHandoffRepeat: the peer took the transfer on again while we still had
	// an outstanding handoff with it. Its in-flight set is empty and its bucket
	// does not hold the blob, so the earlier attempt is over and produced
	// nothing. This is the only failure report a handoff ever generates: the
	// peer logs the error on its own side and nothing is waiting on the result,
	// so without this the sender would never learn.
	pullHandoffRepeat
)

func pullHandoffKey(host, cid string) string { return host + "|" + cid }

// notePullHandoff records what a peer answered and classifies it. Keyed per
// host and cid, because peers answer independently and a fresh acceptance from
// one must not read as a repeat for another in the same sweep.
//
// The marker is cleared as soon as it has been acted on -- a repeat arms the
// backoff, and the next attempt an hour later should start from a clean slate
// rather than reporting failure again immediately.
func (ss *MediorumServer) notePullHandoff(host, cid string, err error) pullHandoffOutcome {
	key := pullHandoffKey(host, cid)
	switch {
	case errors.Is(err, errPeerPullInProgress):
		return pullHandoffRunning
	case errors.Is(err, errPeerPullAccepted):
		if _, outstanding := ss.pullHandoffs.Get(key); outstanding {
			ss.pullHandoffs.Remove(key)
			return pullHandoffRepeat
		}
		ss.pullHandoffs.Set(key, struct{}{}, imcache.WithDefaultExpiration())
		return pullHandoffFresh
	case err == nil:
		// Confirmed present on the peer. Whatever we handed it is settled.
		ss.pullHandoffs.Remove(key)
		return pullHandoffNone
	default:
		return pullHandoffNone
	}
}

// replicateFile is the shared implementation for replicating files to all necessary mirrors in parallel
func (ss *MediorumServer) replicateToHosts(ctx context.Context, upload *Upload, cid string, existingMirrors []string, isTranscoded bool) error {
	// Get the file from our bucket — hot first, archive fallback so we
	// source from wherever the blob actually lives on this node.
	shardedCid := cidutil.ShardCID(cid)
	_, srcBucket, err := ss.blobAttrs(ctx, shardedCid)
	if err != nil {
		return fmt.Errorf("failed to get file attributes: %w", err)
	}

	// Determine placement hosts
	placementHosts := upload.PlacementHosts
	if len(placementHosts) == 0 {
		allHosts, _ := ss.rendezvousAllHosts(cid)
		// Limit to replication factor
		if len(allHosts) > ss.Config.ReplicationFactor {
			placementHosts = allHosts[:ss.Config.ReplicationFactor]
		} else {
			placementHosts = allHosts
		}
	}

	// Filter out self and hosts that already have the file
	targetHosts := []string{}
	for _, host := range placementHosts {
		if host == ss.Config.Self.Host {
			continue
		}
		if slices.Contains(existingMirrors, host) {
			continue
		}
		targetHosts = append(targetHosts, host)
	}

	if len(targetHosts) == 0 {
		fileType := "original"
		if isTranscoded {
			fileType = "transcoded"
		}
		ss.logger.Debug("no hosts need replication", zap.String("uploadID", upload.ID), zap.String("type", fileType))
		return nil
	}

	// Replicate to all target hosts in parallel
	type replicationResult struct {
		host string
		err  error
	}

	resultsChan := make(chan replicationResult, len(targetHosts))
	var wg sync.WaitGroup

	for _, host := range targetHosts {
		wg.Add(1)
		go func(targetHost string) {
			defer wg.Done()

			err := ss.replicateStoredFileToHost(ctx, targetHost, cid, srcBucket, shardedCid, upload.PlacementHosts, upload.ID, isTranscoded)
			resultsChan <- replicationResult{host: targetHost, err: err}
		}(host)
	}

	// Wait for all replications to complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	newSuccessHosts := []string{}
	handoffProgressing := false
	for result := range resultsChan {
		switch ss.notePullHandoff(result.host, cid, result.err) {
		case pullHandoffFresh, pullHandoffRunning:
			handoffProgressing = true
			// The peer owns this transfer. Recording a mirror would be a claim
			// we cannot back, and logging a failure would be wrong too. A later
			// sweep gets already_present -- the peer reporting what is actually
			// in its bucket, which is a better signal than anything it could
			// have promised us here.
			ss.logger.Debug("peer is pulling blob; awaiting confirmation on a later sweep",
				zap.String("host", result.host),
				zap.String("cid", cid))
			continue
		case pullHandoffRepeat:
			// The peer took this on before and has neither the blob nor a
			// transfer running, so that attempt failed. Nothing was waiting on
			// it, so this is the only place the failure surfaces.
			ss.logger.Warn("peer re-accepted blob pull; its previous transfer produced nothing",
				zap.String("host", result.host),
				zap.String("cid", cid))
			continue
		}
		if result.err != nil {
			fileType := "file"
			if isTranscoded {
				fileType = "transcoded file"
			}
			ss.logger.Warn("failed to replicate "+fileType+" to host",
				zap.String("host", result.host),
				zap.String("cid", cid),
				zap.Error(result.err))
		} else {
			newSuccessHosts = append(newSuccessHosts, result.host)
		}
	}

	// A handoff that is getting somewhere must not arm the under-replication
	// backoff. findMissedReplications sets that marker when it queues an upload
	// and its TTL is an hour, which suits an attempt that definitively failed. A
	// transfer still running at the next five minute sweep would otherwise
	// suppress the confirming already_present for the rest of the hour -- and
	// the blobs that take longest to transfer are exactly the ones this path
	// exists to carry, so that would be the common case for them rather than an
	// edge.
	//
	// Only fresh and running qualify. A repeat acceptance is the opposite: it
	// says the last transfer ended without producing the blob, so it must leave
	// the backoff armed. Clearing on every 202 gave a permanently failing pull
	// no terminal state at all -- the peer accepted, failed and was asked again
	// five minutes later, indefinitely and with nothing on the sender to show
	// for it.
	if handoffProgressing {
		ss.replicationAttempts.Remove(upload.ID)
	}

	// No replications succeeded; skip the DB read and Core operation relay.
	// The replication worker re-queues an under-replicated upload every cache
	// TTL (1h); without this fast-exit, each retry where every reachable peer
	// fails would write a fresh uploads op with byte-identical mirrors and
	// submit that op to every node via Core, dominating the
	// uploads-update op rate.
	if len(newSuccessHosts) == 0 {
		return nil
	}

	// Update upload record with successful mirrors using the operation log.
	var dbUpload Upload
	if err := ss.crud.DB.Where("id = ?", upload.ID).First(&dbUpload).Error; err != nil {
		return fmt.Errorf("failed to get upload from DB: %w", err)
	}

	// Start with existing mirrors and merge in successful hosts.
	merged, changed := mergeReplicationMirrors(isTranscoded, &dbUpload, newSuccessHosts)
	if !changed {
		// A concurrent worker has already recorded every host we just
		// replicated to. The merged list equals what's already in the DB,
		// so emitting a Core operation now would relay a row with byte-
		// identical content to every node for no semantic gain.
		ss.logger.Debug("replication produced no new mirrors; suppressing core operation",
			zap.String("uploadID", upload.ID),
			zap.String("cid", cid),
			zap.Strings("newSuccessHosts", newSuccessHosts),
		)
		return nil
	}
	if isTranscoded {
		dbUpload.TranscodedMirrors = merged
	} else {
		dbUpload.Mirrors = merged
	}

	if err := ss.crud.Update(&dbUpload); err != nil {
		return fmt.Errorf("failed to update mirrors: %w", err)
	}

	fieldName := "mirrors"
	if isTranscoded {
		fieldName = "transcoded_mirrors"
	}
	ss.logger.Info("mirrored file",
		zap.String("name", upload.OrigFileName),
		zap.String("uploadID", upload.ID),
		zap.String("cid", cid),
		zap.String("field", fieldName),
		zap.Strings(fieldName, merged),
	)

	return nil
}

// mergeReplicationMirrors merges newSuccessHosts into the upload's existing
// mirror list (transcoded vs original chosen by isTranscoded) in stable
// order, de-duplicating against hosts already present. Returns the merged
// list and whether the merge actually added anything; callers use the bool
// to decide whether to relay a Core operation or suppress a no-op write.
func mergeReplicationMirrors(isTranscoded bool, upload *Upload, newSuccessHosts []string) ([]string, bool) {
	var existing []string
	if isTranscoded {
		existing = upload.TranscodedMirrors
	} else {
		existing = upload.Mirrors
	}
	merged := append([]string{}, existing...)
	changed := false
	for _, host := range newSuccessHosts {
		if !slices.Contains(merged, host) {
			merged = append(merged, host)
			changed = true
		}
	}
	return merged, changed
}

// underReplicatedUploadsSQL selects this node's uploads that still owe a copy
// to somebody, matching what replicationWorker will actually attempt.
//
// The transcode arm is guarded on the 320 existing because the two mirror
// lists do not describe the same population. Image uploads record
// transcode_results->>'original.jpg' and never populate transcoded_mirrors at
// all, so an unguarded length check would match every image ever uploaded, on
// every pass, forever -- and the worker would drop each one right back on the
// floor, since it makes the same 320 check before doing anything.
//
// NULLIF keeps the length check total. jsonb_array_length raises on a scalar,
// and a JSON null survives COALESCE (it is a value, not SQL NULL), so a single
// legacy row storing 'null' rather than '[]' would take the error path for the
// whole query -- which is silent, because this scan feeds a background sweep
// with nobody to return an error to. Rows written by this code cannot be in
// that shape; rows arriving through crudr from other node versions are not
// ours to assume about.
const underReplicatedUploadsSQL = `created_by = ?
	AND orig_file_cid IS NOT NULL
	AND orig_file_cid != ''
	AND status != ?
	AND (
		jsonb_array_length(COALESCE(NULLIF(mirrors, 'null')::jsonb, '[]'::jsonb)) < ` + replicationTargetSQL + `
		OR (
			COALESCE(transcode_results::jsonb ->> '320', '') != ''
			AND jsonb_array_length(COALESCE(NULLIF(transcoded_mirrors, 'null')::jsonb, '[]'::jsonb)) < ` + replicationTargetSQL + `
		)
	)`

// replicationTargetSQL is how many copies a row wants, in SQL: the number of
// hosts it was explicitly placed on, or the bound parameter when it was not.
//
// NULLIF(..., 0) is what makes the fallback work -- an absent or empty
// placement list has length 0, which becomes NULL and lets COALESCE reach the
// parameter. The inner guards mirror the ones above for the same reason: a
// legacy row storing the text 'null' would otherwise raise for the whole scan.
const replicationTargetSQL = `COALESCE(
		NULLIF(jsonb_array_length(COALESCE(NULLIF(placement_hosts, 'null')::jsonb, '[]'::jsonb)), 0),
		?
	)`

// uploadNeedsReplication reports whether either blob this upload owns is still
// short of replicas. It is the in-process twin of underReplicatedUploadsSQL and
// of the two conditions replicationWorker branches on, kept as a free function
// so the agreement between the three is directly testable.
func uploadNeedsReplication(upload *Upload, replicationFactor int) bool {
	target := replicationTargetFor(upload, replicationFactor)
	if len(upload.Mirrors) < target {
		return true
	}
	cid := upload.TranscodeResults["320"]
	return cid != "" && len(upload.TranscodedMirrors) < target
}

// replicationTargetFor is how many copies an upload wants.
//
// Placement is an instruction, not a hint: an upload placed on two hosts is
// fully replicated at two, and asking for ReplicationFactor copies of it would
// mean putting it somewhere it was told not to go. replicationWorker has always
// read it this way; the query and check that decide what to hand the worker did
// not, so every placed upload with fewer hosts than the replication factor was
// selected on every sweep, queued, and then dropped by the worker for the life
// of the row.
func replicationTargetFor(upload *Upload, replicationFactor int) int {
	if placed := len(upload.PlacementHosts); placed > 0 {
		return placed
	}
	return replicationFactor
}

func (ss *MediorumServer) findMissedReplications() {
	// Find uploads that don't have enough replicas
	uploads := []*Upload{}
	if err := ss.crud.DB.Where(
		underReplicatedUploadsSQL,
		ss.Config.Self.Host, JobStatusBusy, ss.Config.ReplicationFactor, ss.Config.ReplicationFactor,
	).Find(&uploads).Error; err != nil {
		// Worth a line of its own: a failure here is indistinguishable from a
		// fully replicated node, so without it the sweep can be dead for weeks
		// and look healthy the whole time.
		ss.logger.Error("failed to scan for under-replicated uploads", zap.Error(err))
		return
	}

	for _, upload := range uploads {
		if uploadNeedsReplication(upload, ss.Config.ReplicationFactor) {
			// Backoff so we don't re-queue the same upload every cycle while
			// it stays under-replicated (e.g. its source blob is gone or
			// peers keep rejecting it). After the cache TTL we'll try again.
			if _, attempted := ss.replicationAttempts.Get(upload.ID); attempted {
				continue
			}
			select {
			case ss.replicationWork <- upload:
				ss.replicationAttempts.Set(upload.ID, struct{}{}, imcache.WithDefaultExpiration())
				ss.logger.Info("queued upload for replication", zap.String("uploadID", upload.ID))
			default:
				// Channel full, skip for now
			}
		}
	}
}
