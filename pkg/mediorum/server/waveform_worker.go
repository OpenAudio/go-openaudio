package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
	"gocloud.dev/gcerrors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	waveformStatusDone           = "done"
	waveformStatusError          = "error"
	waveformStatusNotLocal       = "not_local"
	waveformStatusUnavailable    = "unavailable"
	waveformStatusArchiveSkipped = "archive_skipped"

	// waveformMaxTries caps retries of rows that fail on bytes we actually
	// read. not_local and unavailable deliberately do not count against it:
	// neither says anything about whether the audio is analyzable.
	waveformMaxTries = 3

	// Retry backoffs, chosen by what the status actually means.
	//
	// unavailable is short because it signals an unhealthy bucket rather than
	// missing content -- a transient archive outage should not park a large
	// slice of the catalog for a day.
	waveformRetryBackoffError       = 6 * time.Hour
	waveformRetryBackoffNotLocal    = 24 * time.Hour
	waveformRetryBackoffUnavailable = 15 * time.Minute

	waveformSweepBatchLimit = 50
	waveformDiscoveryLimit  = 100

	// Batches pulled per sweep before returning to re-check context and
	// version. The queue filling usually stops a sweep well before this; it
	// exists so a fast-draining pool cannot keep one sweep running for hours.
	waveformMaxBatchesPerSweep = 20

	// Short while there is a backlog, long once caught up. The short interval
	// only has to beat the rate the workers drain the queue, since the sweep
	// stops as soon as the queue is full.
	waveformSweepIntervalActive = 5 * time.Second
	waveformSweepIntervalIdle   = 5 * time.Minute

	// Bounds a single sweep. A caught-up re-walk scans to the end of history to
	// prove nothing is left, which is the one query here that grows with the
	// catalog.
	waveformSweepTimeout = 2 * time.Minute

	// How long after finishing a pass the walk starts over. Uploads arrive from
	// peers with older timestamps than the cursor has already passed, so a
	// finished walk is only ever finished for now.
	waveformRewalkInterval = 6 * time.Hour

	// Archive reads come from a colder, slower tier.
	waveformJobTimeout        = 5 * time.Minute
	waveformArchiveJobTimeout = 20 * time.Minute

	// Bounds on the peer search behind a redirect. The probe sits inline in a
	// user request, so the whole search has to stay well under a client's
	// patience -- and unlike a blob, a miss here is cheap to accept.
	waveformRedirectMaxProbes = 3
	waveformProbeTimeout      = 2 * time.Second

	// How stale the outstanding-work estimate may get. It is refreshed on a
	// sweep rather than on a page load: the count is an anti-join across the
	// whole catalog, which is fine occasionally in the background and not fine
	// on every console render.
	waveformRollupTTL = 2 * time.Minute
)

// waveformJob is one unit of work: analyze the audio behind a CID.
type waveformJob struct {
	cid string
	// uploadID is empty for legacy Qm content, which has no upload row. It is
	// carried so the stored row can be anti-joined against uploads cheaply.
	uploadID string
	// placementHosts feeds isArchiveCID, which needs it to tell "this node
	// holds the CID only because StoreAll" from an explicit placement.
	placementHosts []string
	// localPath is a copy of the blob that already exists on disk, handed over
	// by a replication path that had it in a temp file. Ownership transfers
	// with the job: whoever holds it deletes it, so a worker that accepts this
	// job is responsible for removing the file.
	localPath string
}

// waveformRow is a stored analysis result.
type waveformRow struct {
	CID         string
	Peaks       []byte
	Buckets     int
	Version     int
	SampleRate  int
	SampleCount int64
	DurationMs  int64
	Status      string
}

func (ss *MediorumServer) startWaveformAnalyzer(ctx context.Context) error {
	numWorkers := ss.Config.WaveformWorkers
	if numWorkers < 1 {
		numWorkers = 2
	}
	for i := 0; i < numWorkers; i++ {
		ss.startWaveformWorker(i)
	}

	if !ss.Config.WaveformBackfillEnabled {
		ss.logger.Info("waveform backfill disabled; live path only")
	}

	// The loop runs whether or not backfill is enabled, and only the history
	// walk is gated inside it. Returning early here instead would also strand
	// the retry sweep, so a transient failure from the live transcode hook --
	// recorded with a next_attempt_at nobody ever reads -- would be permanent
	// on a live-only node. It would also freeze the outstanding count at zero,
	// which reads as "nothing left" when in truth nothing has been looked at.
	//
	// The sweep refills the queue and the workers set the pace, so throughput
	// is bounded by OPENAUDIO_WAVEFORM_WORKERS rather than by how often this
	// fires. Sweeping on a fixed drip instead would leave the workers idle most
	// of the time and stretch a large catalog over months.
	//
	// Two intervals: a short one while there is a backlog, a long one once the
	// walk is caught up, so an idle node is not querying every few seconds.
	ticker := time.NewTicker(waveformSweepIntervalActive)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			busy := ss.runWaveformSweeps(ctx)
			if busy {
				ticker.Reset(waveformSweepIntervalActive)
			} else {
				ticker.Reset(waveformSweepIntervalIdle)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runWaveformSweeps runs one pass of both sweeps and reports whether work
// remains. The timeout bounds the discovery scan: a caught-up re-walk reads to
// the end of history to prove there is nothing left, and that must not be able
// to sit on a connection indefinitely.
func (ss *MediorumServer) runWaveformSweeps(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, waveformSweepTimeout)
	defer cancel()

	// Retries run regardless of backfill: they re-attempt rows that already
	// exist, which on a live-only node are the ones the transcode hook wrote.
	busy := ss.sweepWaveformRetries(ctx)
	if ss.Config.WaveformBackfillEnabled {
		busy = ss.sweepWaveformDiscovery(ctx) || busy
	}
	// Also unconditional: an unlinked row is the one failure the other sweeps
	// cannot repair. Discovery correlates on upload_id, so a row missing it is
	// invisible there -- and on a live-only node discovery never runs at all.
	ss.linkOrphanWaveforms(ctx)
	ss.refreshWaveformRollup(ctx)
	return busy
}

// linkOrphanWaveforms fills in upload_id on rows that were written without one.
//
// The replication handoff knows only a cid. It now prefers the upload id the
// sending peer supplies, but a sender that predates that field -- or one with
// no upload context -- still leaves the puller resolving against its own
// uploads table, which the sender publishes through consensus. That row can
// arrive after the blob does, and nothing revisits the waveform afterwards:
// discovery correlates on upload_id, so an unlinked row makes its upload look
// permanently outstanding and every re-walk recomputes a waveform that is
// already correct.
//
// This repairs the link in place rather than recomputing. Legacy Qm content is
// excluded because it has no upload at all -- for those rows a null upload_id
// is the right answer, not a miss, and retrying them every sweep would be
// wasted work that never converges.
func (ss *MediorumServer) linkOrphanWaveforms(ctx context.Context) {
	if !ss.Config.WaveformEnabled {
		return
	}

	tag, err := ss.pgPool.Exec(ctx, `
		update waveforms w set upload_id = u.id
		from uploads u
		where w.upload_id is null
		  and w.cid not like 'Qm%'
		  and u.transcode_results::jsonb ->> '320' = w.cid
	`)
	if err != nil {
		ss.logger.Warn("waveform orphan link failed", zap.Error(err))
		return
	}
	linked := tag.RowsAffected()

	// Previews reach their upload through audio_previews, which carries the
	// source 320 rather than the upload id.
	tag, err = ss.pgPool.Exec(ctx, `
		update waveforms w set upload_id = u.id
		from audio_previews p
		join uploads u on u.transcode_results::jsonb ->> '320' = p.source_c_id
		where w.upload_id is null
		  and w.cid not like 'Qm%'
		  and p.cid = w.cid
	`)
	if err != nil {
		ss.logger.Warn("waveform preview orphan link failed", zap.Error(err))
		return
	}
	linked += tag.RowsAffected()

	if linked > 0 {
		ss.logger.Info("linked orphaned waveforms", zap.Int64("count", linked))
	}
}

// Upload states. Every analyzable upload lands in exactly one, so the counts
// reconcile with each other and with the catalog -- which per-blob counts of
// the waveforms table never could, since an upload yields one or two blobs.
const (
	waveformStateAnalyzed       = "analyzed"
	waveformStatePartial        = "partial"
	waveformStateToRecompute    = "to_recompute"
	waveformStateFailed         = "failed"
	waveformStateUnavailable    = "unavailable"
	waveformStateNotLocal       = "not_local"
	waveformStateArchiveSkipped = "archive_skipped"
	waveformStateNeverAnalyzed  = "never_analyzed"
)

type waveformRollup struct {
	byState    map[string]int64
	orphanRows int64
}

// refreshWaveformRollup samples every reported figure in a single pass.
//
// One query rather than one per tile is what makes the numbers add up. Counting
// present rows from the waveforms table and absent ones from uploads meant two
// units and two moments: an upload carrying a stale row was reported as
// analyzed, as awaiting recompute, and as outstanding at the same time, and no
// arithmetic over the tiles recovered the size of the catalog.
//
// Where an upload's blobs disagree the worse state wins. A finished 320 beside
// a failed preview is a failure an operator should see, and counting it under
// both headings is what produced the double counting in the first place.
//
// It stays a sample rather than a per-request count because the anti-join half
// is proportional to the catalog and cannot be indexed away -- the preview key
// is extracted dynamically from jsonb. Taking it all from one pass is what buys
// consistency; the console reports the age alongside.
//
// expected mirrors waveformTargets exactly, including its requirement that a
// selected preview actually resolve to a blob. Deriving it any other way lets
// an upload expect a row that can never be written, leaving it permanently
// short of its own expected count.
func (ss *MediorumServer) refreshWaveformRollup(ctx context.Context) {
	ss.waveformRollupMu.Lock()
	fresh := time.Since(ss.waveformRollupAt) < waveformRollupTTL
	ss.waveformRollupMu.Unlock()
	if fresh {
		return
	}

	rollup := waveformRollup{byState: map[string]int64{}}

	rows, err := ss.pgPool.Query(ctx, `
		with per_upload as (
			select u.id,
			       count(w.cid) filter (where w.version = $1 and w.status = $3) as done,
			       count(w.cid) filter (where w.version = $1 and w.status = $4) as failed,
			       count(w.cid) filter (where w.version = $1 and w.status = $5) as unavailable,
			       count(w.cid) filter (where w.version = $1 and w.status = $6) as not_local,
			       count(w.cid) filter (where w.version = $1 and w.status = $7) as archive_skipped,
			       count(w.cid) filter (where w.version <> $1)                  as stale,
			       (case when coalesce(u.transcode_results::jsonb ->> '320', '') <> ''
			             then 1 else 0 end)
			     + (case when coalesce(u.selected_preview, '') <> ''
			              and coalesce(u.transcode_results::jsonb ->> u.selected_preview, '') <> ''
			             then 1 else 0 end) as expected
			from uploads u
			left join waveforms w on w.upload_id = u.id
			where u.template = $2
			group by u.id
		)
		select case
		         when failed          > 0 then $8
		         when unavailable     > 0 then $9
		         when not_local       > 0 then $10
		         when archive_skipped > 0 then $11
		         when stale           > 0 then $12
		         when done >= expected    then $13
		         when done > 0            then $14
		         else $15
		       end as state,
		       count(*)::bigint
		from per_upload
		where expected > 0
		group by 1
	`,
		waveformVersion, JobTemplateAudio,
		waveformStatusDone, waveformStatusError, waveformStatusUnavailable,
		waveformStatusNotLocal, waveformStatusArchiveSkipped,
		waveformStateFailed, waveformStateUnavailable, waveformStateNotLocal,
		waveformStateArchiveSkipped, waveformStateToRecompute, waveformStateAnalyzed,
		waveformStatePartial, waveformStateNeverAnalyzed,
	)
	if err != nil {
		ss.logger.Warn("waveform rollup query failed", zap.Error(err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			ss.logger.Warn("waveform rollup scan failed", zap.Error(err))
			return
		}
		rollup.byState[state] = count
	}
	if err := rows.Err(); err != nil {
		ss.logger.Warn("waveform rollup query failed", zap.Error(err))
		return
	}

	// Counted apart because it is the one population the rollup structurally
	// cannot see: that query is keyed by upload, and these rows have none.
	if err := ss.pgPool.QueryRow(ctx, `
		select count(*)::bigint from waveforms
		where upload_id is null and cid not like 'Qm%'
	`).Scan(&rollup.orphanRows); err != nil {
		ss.logger.Warn("waveform orphan count failed", zap.Error(err))
		return
	}

	ss.waveformRollupMu.Lock()
	ss.waveformRollup = rollup
	ss.waveformRollupAt = time.Now()
	ss.waveformRollupMu.Unlock()
}

func (ss *MediorumServer) startWaveformWorker(workerId int) {
	ss.lc.AddManagedRoutine(fmt.Sprintf("waveform worker %d", workerId), func(ctx context.Context) error {
		for {
			select {
			case job, ok := <-ss.waveformWork:
				if !ok {
					return nil
				}
				ss.processWaveformJob(ctx, job)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
}

// waveformTargets lists the analyzable blobs an upload produced, each paired
// with the placement context its own blob was replicated under.
//
// The 320 is written via replicateToMyBucket with upload.PlacementHosts; the
// preview via replicateFileParallel with nil. That difference is not cosmetic:
// bucketForCID treats any non-empty placement as "force primary", so a preview
// given the upload's hosts would be judged primary-tier when it may actually
// live in archive.
func waveformTargets(upload Upload) []waveformJob {
	targets := make([]waveformJob, 0, 2)
	if cid := upload.TranscodeResults["320"]; cid != "" {
		targets = append(targets, waveformJob{
			cid:            cid,
			uploadID:       upload.ID,
			placementHosts: upload.PlacementHosts,
		})
	}
	// The preview is stored under its own selection key rather than a fixed
	// one, since the key encodes the start offset it was cut at.
	if upload.SelectedPreview.Valid {
		if cid := upload.TranscodeResults[upload.SelectedPreview.String]; cid != "" {
			targets = append(targets, waveformJob{
				cid:      cid,
				uploadID: upload.ID,
				// nil placement, matching replicateFileParallel above.
			})
		}
	}
	return targets
}

// resolveWaveformUploadID finds which upload a replicated cid belongs to.
//
// Replication paths know only a cid, but a row written without an upload_id is
// invisible to discovery -- it correlates on upload_id -- so that upload would
// stay outstanding forever and every re-walk would analyze it again. Resolving
// once here avoids that.
//
// Both lookups are index-backed: the 320 by idx_uploads_transcode_cid_320, and
// a preview by its own primary key in audio_previews, which yields the source
// 320 and from there the upload. Returns empty for legacy Qm content, which has
// no upload at all -- that is a legitimate null, not a failure.
func (ss *MediorumServer) resolveWaveformUploadID(ctx context.Context, cid string) string {
	var uploadID string
	err := ss.pgPool.QueryRow(ctx, `
		select id from uploads where transcode_results::jsonb ->> '320' = $1 limit 1
	`, cid).Scan(&uploadID)
	if err == nil {
		return uploadID
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		ss.logger.Debug("waveform upload lookup failed", zap.String("cid", cid), zap.Error(err))
		return ""
	}

	// source_c_id, not source_cid: gorm derives the column from AudioPreview's
	// SourceCID field and splits the acronym. Getting this wrong does not fail
	// loudly -- the query errors, the error is not ErrNoRows, and the caller
	// treats an empty return as "legacy content with no upload" -- so it looks
	// exactly like a preview that legitimately has no owner.
	err = ss.pgPool.QueryRow(ctx, `
		select u.id
		from audio_previews p
		join uploads u on u.transcode_results::jsonb ->> '320' = p.source_c_id
		where p.cid = $1
		limit 1
	`, cid).Scan(&uploadID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			ss.logger.Warn("waveform preview lookup failed", zap.String("cid", cid), zap.Error(err))
		}
		return ""
	}
	return uploadID
}

// enqueueWaveformJob offers work to the pool without blocking. A full queue
// means the sweep will find the CID again next pass, so dropping is safe and
// preferable to stalling a sweep or an HTTP handler.
func (ss *MediorumServer) enqueueWaveformJob(job waveformJob) bool {
	select {
	case ss.waveformWork <- job:
		return true
	default:
		return false
	}
}

func (ss *MediorumServer) processWaveformJob(ctx context.Context, job waveformJob) {
	logger := ss.logger.With(zap.String("cid", job.cid))

	// This job owns the handed-over file now. Deferred so it is released on
	// success, failure and panic alike -- the enqueue site only stops owning it
	// once the send succeeded, so nothing else will remove it.
	if job.localPath != "" {
		defer os.Remove(job.localPath)
	}

	timeout := waveformJobTimeout
	if ss.isArchiveCID(job.cid, job.placementHosts) {
		timeout = waveformArchiveJobTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	if err := ss.analyzeWaveform(ctx, job); err != nil {
		logger.Warn("waveform analysis failed", zap.Error(err), zap.Duration("took", time.Since(start)))
		return
	}
	logger.Debug("waveform analysis done", zap.Duration("took", time.Since(start)))
}

// analyzeWaveform reads the blob from this node's own storage and computes its
// envelope. It never fetches from a peer -- avoiding that egress is the whole
// point of doing this on the node.
func (ss *MediorumServer) analyzeWaveform(ctx context.Context, job waveformJob) error {
	// A replication path already had these bytes on disk and handed them over,
	// so read them rather than fetching back what we just wrote.
	//
	// The archive guard is deliberately skipped on this path. It exists to stop
	// the backfill pulling blobs out of cold storage, and there is no bucket
	// read here at all -- so an archive-tier blob that arrives by replication is
	// analyzed for free rather than deferred.
	if job.localPath != "" {
		return ss.analyzeWaveformFromFile(ctx, job.cid, job.uploadID, job.localPath)
	}

	// The archive guard belongs here, immediately before the first bucket call,
	// rather than only where jobs are queued.
	//
	// Which tier a cid belongs to is not fixed: rendezvous rank shifts whenever
	// the validator set changes, and replicateToMyBucket writes through
	// bucketForCID, so a blob can move to archive after an earlier attempt
	// recorded not_local or unavailable. The retry sweep re-queues those rows
	// without consulting the tier, and readBlob falls back to archive on
	// NotFound unconditionally -- so a rank shift could pull a whole slice of
	// the catalog out of cold storage with the flag switched off.
	//
	// Checking at the read makes the flag authoritative however the job was
	// queued. It costs nothing: isArchiveCID is rendezvous arithmetic against
	// the in-memory host ring and touches no bucket.
	//
	// job.placementHosts must mirror how the blob was written, not what its
	// upload says: bucketForCID treats any non-empty placement as "force
	// primary", and previews are replicated with nil placement even when their
	// upload has some. Passing the upload's hosts for a preview would report
	// every preview as primary-tier and read it straight out of cold storage.
	if !ss.Config.WaveformArchiveEnabled && ss.isArchiveCID(job.cid, job.placementHosts) {
		if err := ss.markWaveformStatus(ctx, job.cid, job.uploadID, waveformStatusArchiveSkipped, nil, waveformRetryBackoffNotLocal); err != nil {
			return err
		}
		return nil
	}

	key := cidutil.ShardCID(job.cid)

	// blobAttrs and readBlob both try primary, then fall back to archive on
	// NotFound. They also keep a real archive failure distinguishable from a
	// genuine miss, which is what the status split below depends on.
	//
	// blobExists is deliberately not used here: it discards its errors, so it
	// reports "absent" for an unreachable bucket. Fine as a cheap pre-filter,
	// useless as the basis for a durable decision.
	attrs, _, err := ss.blobAttrs(ctx, key)
	if err != nil {
		return ss.recordWaveformReadFailure(ctx, job, err)
	}

	r, _, err := ss.readBlob(ctx, key)
	if err != nil {
		return ss.recordWaveformReadFailure(ctx, job, err)
	}
	defer r.Close()

	result, err := computeWaveform(ctx, r, attrs.Size)
	if err != nil {
		// We read bytes and failed to make sense of them. That is a property
		// of the content, so it counts against the retry cap.
		if markErr := ss.markWaveformError(ctx, job.cid, job.uploadID, err); markErr != nil {
			ss.logger.Error("failed to record waveform error", zap.String("cid", job.cid), zap.Error(markErr))
		}
		return err
	}

	return ss.upsertWaveform(ctx, job.cid, job.uploadID, result)
}

// recordWaveformReadFailure separates "nobody has this blob" from "our storage
// is unhealthy".
//
// Collapsing the two would be the expensive mistake: a transient archive
// outage during a backfill would stamp a 24h backoff across a large slice of
// the catalog in a single sweep. Neither case increments error_count, since
// neither is evidence about the audio itself -- mirroring the existing
// audio-analysis behavior that lets a job migrate to whichever node holds the
// file.
func (ss *MediorumServer) recordWaveformReadFailure(ctx context.Context, job waveformJob, readErr error) error {
	if gcerrors.Code(readErr) == gcerrors.NotFound {
		if err := ss.markWaveformStatus(ctx, job.cid, job.uploadID, waveformStatusNotLocal, readErr, waveformRetryBackoffNotLocal); err != nil {
			return err
		}
		return fmt.Errorf("waveform: blob not on this node: %w", readErr)
	}
	if err := ss.markWaveformStatus(ctx, job.cid, job.uploadID, waveformStatusUnavailable, readErr, waveformRetryBackoffUnavailable); err != nil {
		return err
	}
	return fmt.Errorf("waveform: storage unavailable: %w", readErr)
}

// -- sweeps ---------------------------------------------------------------

// sweepWaveformRetries re-queues rows whose backoff has elapsed. It reads only
// from the waveforms table, so it is indexed and proportional to the result
// set rather than to catalog size.
//
// The archive pre-filter is intentionally not applied here: a row that reached
// not_local or error was already judged worth attempting.
//
// Placement is recovered rather than assumed. analyzeWaveform decides the
// archive tier from it, and bucketForCID reads any non-empty placement as
// "force primary" -- so queueing a placement-pinned 320 with nil would judge it
// archive-tier and skip a blob that is really in primary. The join is against
// upload_id, and only ever over a batch, so it costs a primary-key lookup per
// row rather than a scan.
//
// A row whose cid is not its upload's 320 is the preview, which was replicated
// with no placement at all, so it correctly gets nil either way.
//
// Returns whether it queued anything, which keeps the sweep interval short
// while a backlog is draining.
func (ss *MediorumServer) sweepWaveformRetries(ctx context.Context) bool {
	rows, err := ss.pgPool.Query(ctx, `
		select w.cid, coalesce(w.upload_id, ''),
		       coalesce(u.transcode_results::jsonb ->> '320', ''),
		       coalesce(u.placement_hosts, '[]')
		from waveforms w
		left join uploads u on u.id = w.upload_id
		where w.status <> $1
		  and (w.status <> $2 or $3)
		  and coalesce(w.error_count, 0) < $4
		  and w.next_attempt_at is not null
		  and w.next_attempt_at <= now()
		order by w.next_attempt_at
		limit $5
	`, waveformStatusDone, waveformStatusArchiveSkipped, ss.Config.WaveformArchiveEnabled, waveformMaxTries, waveformSweepBatchLimit)
	if err != nil {
		ss.logger.Warn("waveform retry sweep failed", zap.Error(err))
		return false
	}
	defer rows.Close()

	jobs := []waveformJob{}
	for rows.Next() {
		var cid, uploadID, cid320, placementJSON string
		if err := rows.Scan(&cid, &uploadID, &cid320, &placementJSON); err != nil {
			ss.logger.Warn("waveform retry scan failed", zap.Error(err))
			return false
		}
		job := waveformJob{cid: cid, uploadID: uploadID}
		if cid != "" && cid == cid320 {
			var hosts []string
			if err := json.Unmarshal([]byte(placementJSON), &hosts); err == nil {
				job.placementHosts = hosts
			}
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		ss.logger.Warn("waveform retry sweep failed", zap.Error(err))
		return false
	}

	queued := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		// A full queue is not a problem here: these rows carry their own
		// next_attempt_at, so whatever does not fit is simply picked up again.
		if !ss.enqueueWaveformJob(job) {
			break
		}
		queued++
	}
	return queued > 0
}

// sweepWaveformDiscovery walks uploads newest-first looking for 320s that have
// no waveforms row yet.
//
// Newest-first matters: the recent slice is what anyone actually looks at, and
// it lands early rather than after the whole catalog.
//
// Returns whether work remains, which the caller uses to keep sweeping often
// while a backlog drains.
func (ss *MediorumServer) sweepWaveformDiscovery(ctx context.Context) bool {
	cur, err := ss.getWaveformCursor(ctx)
	if err != nil {
		ss.logger.Warn("waveform cursor read failed", zap.Error(err))
		return false
	}

	// A version change means every stored waveform was computed under different
	// rules, so the walk starts over. nextWaveformUploadBatch only counts rows
	// at the current version as present, so the re-walk finds them all.
	if cur.Version != waveformVersion {
		ss.logger.Info("waveform version changed; restarting backfill",
			zap.Int("was", cur.Version), zap.Int("now", waveformVersion))
		if err := ss.resetWaveformCursor(ctx); err != nil {
			ss.logger.Warn("waveform cursor reset failed", zap.Error(err))
			return false
		}
		cur = waveformCursor{Version: waveformVersion}
	}

	// Reaching the end of history is not the end of the job. Uploads replicate
	// from peers, so this node keeps learning about older ones after the walk
	// has passed their position -- and a descending cursor never goes back for
	// them. Latching exhausted forever left those without waveforms for good.
	if cur.Exhausted {
		if time.Since(cur.UpdatedAt) < waveformRewalkInterval {
			return false
		}
		ss.logger.Info("waveform backfill re-walking history for late-arriving uploads")
		if err := ss.resetWaveformCursor(ctx); err != nil {
			ss.logger.Warn("waveform cursor reset failed", zap.Error(err))
			return false
		}
		cur = waveformCursor{Version: waveformVersion}
	}

	cursorTime, cursorID := cur.CreatedAt, cur.UploadID
	queued, skippedArchive := 0, 0

	// Keep pulling until the queue is full. The workers then set the pace,
	// which is what makes OPENAUDIO_WAVEFORM_WORKERS the throughput knob rather
	// than the sweep interval.
	for batch := 0; batch < waveformMaxBatchesPerSweep; batch++ {
		if ctx.Err() != nil {
			return true
		}

		uploads, err := ss.nextWaveformUploadBatch(ctx, cursorTime, cursorID, waveformDiscoveryLimit)
		if err != nil {
			ss.logger.Warn("waveform discovery query failed", zap.Error(err))
			return queued > 0
		}
		if len(uploads) == 0 {
			if err := ss.setWaveformCursorExhausted(ctx); err != nil {
				ss.logger.Warn("waveform cursor update failed", zap.Error(err))
			}
			ss.logger.Info("waveform backfill reached end of history",
				zap.Int("queued_this_sweep", queued))
			return queued > 0
		}

		// The cursor may only advance over uploads actually dealt with. An
		// earlier version advanced to the end of the batch regardless, so
		// anything dropped by a full queue was silently skipped until the next
		// re-walk hours later.
		var advanceTo *Upload
		full := false
		batchQueued, batchSkipped := int64(0), int64(0)

		for i := range uploads {
			upload := uploads[i]
			if ctx.Err() != nil {
				break
			}

			// An upload yields up to two analyzable blobs, and they do not share
			// placement semantics: the 320 is replicated with the upload's
			// placement hosts, the preview with none. bucketForCID reads any
			// non-empty placement as "force primary", so handing a preview the
			// upload's hosts would report it primary-tier and read it out of
			// cold storage. Each carries what its own blob was written with.
			full = false
			for _, target := range waveformTargets(upload) {
				if !ss.Config.WaveformArchiveEnabled && ss.isArchiveCID(target.cid, target.placementHosts) {
					// An optimization, not the guarantee -- analyzeWaveform
					// re-checks immediately before its first bucket call, since
					// a cid's tier can change between queueing and reading.
					//
					// Recorded rather than silently skipped: the status doubles
					// as the re-sweep predicate if the operator later opts in,
					// and it makes the held-back population visible before they
					// commit to paying for it.
					if err := ss.markWaveformStatus(ctx, target.cid, upload.ID, waveformStatusArchiveSkipped, nil, waveformRetryBackoffNotLocal); err != nil {
						ss.logger.Warn("failed to record archive skip", zap.String("cid", target.cid), zap.Error(err))
					}
					skippedArchive++
					batchSkipped++
					continue
				}
				if !ss.enqueueWaveformJob(target) {
					full = true
					break
				}
				queued++
				batchQueued++
			}
			if full {
				break
			}
			advanceTo = &upload
		}

		// Checkpoint position and progress together, so the run's counters can
		// never claim work the cursor has not actually passed.
		if advanceTo != nil {
			cursorTime, cursorID = advanceTo.CreatedAt, advanceTo.ID
			if err := ss.setWaveformCursor(ctx, cursorTime, cursorID, batchQueued, batchSkipped); err != nil {
				ss.logger.Warn("waveform cursor update failed", zap.Error(err))
				return true
			}
		}
		if full {
			break
		}
	}

	if skippedArchive > 0 {
		ss.logger.Info("waveform discovery skipped archive-tier cids",
			zap.Int("skipped", skippedArchive))
	}
	return true
}

// nextWaveformUploadBatch pages backwards through uploads by (created_at, id).
//
// Keyset pagination requires the sort direction to match the comparison
// operator -- see nextUploadBatch in repair.go for what goes wrong otherwise.
// Here the walk is newest-first, so the comparison is "<" and the order is
// DESC, which keeps the last row of each batch the true low-water mark.
//
// Rows already carrying a waveforms row are excluded by the not-exists clause
// rather than by an anti-join over the whole table, so a resumed or repeated
// sweep does not re-enqueue completed work.
func (ss *MediorumServer) nextWaveformUploadBatch(ctx context.Context, cursorTime time.Time, cursorID string, limit int) ([]Upload, error) {
	var uploads []Upload
	q := ss.crud.DB.WithContext(ctx).
		Where("template = ?", JobTemplateAudio).
		Where(`coalesce(transcode_results::jsonb ->> '320', '') <> ''`).
		// An upload is outstanding until every blob it produced has a row at
		// the current version -- the 320 always, plus the preview when one was
		// selected. Counting rows rather than checking a single cid is what
		// makes a track whose preview is still unanalyzed stay in the walk.
		//
		// A row at a different version was computed under different rules, so
		// it does not count. That is the whole re-backfill mechanism: no
		// separate sweep, no table rewrite.
		//
		// Joined on upload_id, which is indexed, instead of extracting jsonb
		// on both sides of the correlation as this previously did.
		Where(`(
			select count(*) from waveforms w
			where w.upload_id = uploads.id and w.version = ?
		) < (case when selected_preview is not null and selected_preview <> '' then 2 else 1 end)`,
			waveformVersion)

	if !cursorTime.IsZero() {
		q = q.Where("(created_at, id) < (?, ?)", cursorTime, cursorID)
	}

	err := q.Order("created_at DESC").Order("id DESC").Limit(limit).Find(&uploads).Error
	return uploads, err
}

// -- storage --------------------------------------------------------------
//
// Raw SQL through pgPool rather than gorm, following prune_skips and
// repair_data_loss. These tables are deliberately outside crudr: registering a
// model is what puts a table on the operation log, and mediorum rows replicate
// only by riding the core chain into consensus-visible state.

// nullableUploadID keeps Qm content, which has no upload row, as a null rather
// than an empty string -- the discovery index is partial on upload_id is not
// null, and empty strings would sit in it meaning nothing.
func nullableUploadID(uploadID string) any {
	if uploadID == "" {
		return nil
	}
	return uploadID
}

func (ss *MediorumServer) upsertWaveform(ctx context.Context, cid, uploadID string, result *waveformResult) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms
			(cid, peaks, buckets, version, sample_rate, sample_count, duration_ms,
			 status, error, error_count, upload_id, analyzed_at, next_attempt_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, '', 0, $9, now(), null)
		on conflict (cid) do update set
			peaks = excluded.peaks,
			buckets = excluded.buckets,
			version = excluded.version,
			sample_rate = excluded.sample_rate,
			sample_count = excluded.sample_count,
			duration_ms = excluded.duration_ms,
			status = excluded.status,
			error = '',
			error_count = 0,
			-- Never clear a known upload_id: the live hook knows it, a later
			-- retry driven off the waveforms table alone may not.
			upload_id = coalesce(excluded.upload_id, waveforms.upload_id),
			analyzed_at = now(),
			next_attempt_at = null
	`, cid, result.Peaks, waveformBuckets, waveformVersion,
		result.SampleRate, result.SampleCount, result.DurationMs, waveformStatusDone,
		nullableUploadID(uploadID))
	return err
}

// markWaveformStatus records a non-terminal outcome without touching
// error_count. Used for not_local, unavailable and archive_skipped, none of
// which are evidence about the content.
//
// The version is stamped even though there are no peaks to describe. Discovery
// treats an upload as outstanding when it has no row at the current version, so
// leaving this unset would make these rows invisible to it forever and every
// re-walk would re-enqueue them, bypassing the backoff below. Stamping it hands
// scheduling to the retry sweep alone, which keys off status and ignores
// version, so a version change still recomputes them once they succeed.
func (ss *MediorumServer) markWaveformStatus(ctx context.Context, cid, uploadID, status string, cause error, backoff time.Duration) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms (cid, status, error, error_count, version, upload_id, analyzed_at, next_attempt_at)
		values ($1, $2, $3, 0, $4, $5, now(), now() + $6::interval)
		on conflict (cid) do update set
			status = excluded.status,
			error = excluded.error,
			version = excluded.version,
			upload_id = coalesce(excluded.upload_id, waveforms.upload_id),
			analyzed_at = now(),
			next_attempt_at = excluded.next_attempt_at
	`, cid, status, msg, waveformVersion, nullableUploadID(uploadID),
		fmt.Sprintf("%d seconds", int(backoff.Seconds())))
	return err
}

// markWaveformError records a decode failure and advances the retry counter.
// Stamped with the current version for the same reason as markWaveformStatus:
// otherwise discovery re-enqueues the row on every re-walk and the backoff
// never applies. A row at the retry cap then stays put rather than being
// retried forever by the back door.
func (ss *MediorumServer) markWaveformError(ctx context.Context, cid, uploadID string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms (cid, status, error, error_count, version, upload_id, analyzed_at, next_attempt_at)
		values ($1, $2, $3, 1, $4, $5, now(), now() + $6::interval)
		on conflict (cid) do update set
			status = excluded.status,
			error = excluded.error,
			error_count = coalesce(waveforms.error_count, 0) + 1,
			version = excluded.version,
			upload_id = coalesce(excluded.upload_id, waveforms.upload_id),
			analyzed_at = now(),
			next_attempt_at = excluded.next_attempt_at
	`, cid, waveformStatusError, msg, waveformVersion, nullableUploadID(uploadID),
		fmt.Sprintf("%d seconds", int(waveformRetryBackoffError.Seconds())))
	return err
}

func (ss *MediorumServer) getWaveform(ctx context.Context, cid string) (*waveformRow, error) {
	row := &waveformRow{}
	err := ss.pgPool.QueryRow(ctx, `
		select cid, coalesce(peaks, ''::bytea), buckets, version,
		       coalesce(sample_rate, 0), coalesce(sample_count, 0),
		       coalesce(duration_ms, 0), status
		from waveforms where cid = $1
	`, cid).Scan(&row.CID, &row.Peaks, &row.Buckets, &row.Version,
		&row.SampleRate, &row.SampleCount, &row.DurationMs, &row.Status)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// waveformCursor is the current backfill run: where the newest-first walk has
// reached, the waveform version it is walking under, and what the pass has done
// so far.
type waveformCursor struct {
	CreatedAt time.Time
	UploadID  string
	Exhausted bool
	Version   int
	StartedAt time.Time
	Queued    int64
	Skipped   int64
	UpdatedAt time.Time
}

func (ss *MediorumServer) getWaveformCursor(ctx context.Context) (waveformCursor, error) {
	var (
		createdAt *time.Time
		uploadID  *string
		version   *int
		cur       waveformCursor
	)
	var startedAt *time.Time
	err := ss.pgPool.QueryRow(ctx, `
		select created_at, upload_id, exhausted, version, started_at,
		       coalesce(queued, 0), coalesce(archive_skipped, 0), updated_at
		from waveform_cursor where id = 1
	`).Scan(&createdAt, &uploadID, &cur.Exhausted, &version, &startedAt,
		&cur.Queued, &cur.Skipped, &cur.UpdatedAt)
	if err != nil {
		// No row yet means the walk has not started. Report the current version
		// so a first run is not mistaken for a version change.
		if errors.Is(err, pgx.ErrNoRows) {
			return waveformCursor{Version: waveformVersion}, nil
		}
		return waveformCursor{}, err
	}

	if createdAt != nil {
		cur.CreatedAt = *createdAt
	}
	if uploadID != nil {
		cur.UploadID = *uploadID
	}
	if version != nil {
		cur.Version = *version
	}
	if startedAt != nil {
		cur.StartedAt = *startedAt
	}
	return cur, nil
}

// resetWaveformCursor starts a new run: back to the newest upload, stamped with
// the version it is walking under, counters cleared.
func (ss *MediorumServer) resetWaveformCursor(ctx context.Context) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveform_cursor
			(id, created_at, upload_id, exhausted, version, started_at, queued, archive_skipped, updated_at)
		values (1, null, null, false, $1, now(), 0, 0, now())
		on conflict (id) do update set
			created_at = null,
			upload_id = null,
			exhausted = false,
			version = excluded.version,
			started_at = now(),
			queued = 0,
			archive_skipped = 0,
			updated_at = now()
	`, waveformVersion)
	return err
}

// setWaveformCursor checkpoints the walk and accumulates what this batch did.
// updated_at is the field that separates "working" from "wedged" on a long run,
// so it is stamped on every checkpoint.
func (ss *MediorumServer) setWaveformCursor(ctx context.Context, createdAt time.Time, uploadID string, queued, skipped int64) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveform_cursor
			(id, created_at, upload_id, exhausted, version, started_at, queued, archive_skipped, updated_at)
		values (1, $1, $2, false, $3, now(), $4, $5, now())
		on conflict (id) do update set
			created_at = excluded.created_at,
			upload_id = excluded.upload_id,
			started_at = coalesce(waveform_cursor.started_at, now()),
			queued = coalesce(waveform_cursor.queued, 0) + excluded.queued,
			archive_skipped = coalesce(waveform_cursor.archive_skipped, 0) + excluded.archive_skipped,
			updated_at = now()
	`, createdAt, uploadID, waveformVersion, queued, skipped)
	return err
}

// waveformProgress describes how far the current run has walked and how long
// the rest is likely to take.
type waveformProgress struct {
	// Fraction of history walked, 0..1.
	Fraction float64
	// Elapsed since this run began, and the projection for what is left.
	Elapsed   time.Duration
	Remaining time.Duration
	// Bounds of the audio uploads the walk covers, for rendering position.
	Oldest time.Time
	Newest time.Time
	Known  bool
}

// waveformRunProgress estimates progress from where the cursor sits in time,
// rather than by counting rows.
//
// The walk descends through created_at, so its position within the span of
// audio uploads is a usable proxy for how much is left. It assumes uploads are
// spread evenly across that span, which they are not -- a busy month walks
// slower than a quiet year -- so treat the estimate as an order of magnitude,
// not a deadline. The alternative is counting remaining rows on every render,
// which is the scan this deliberately avoids.
//
// Both bounds come from the ends of idx_uploads_waveform_scan, so this costs
// two index lookups regardless of catalog size.
func (ss *MediorumServer) waveformRunProgress(ctx context.Context, cur waveformCursor) waveformProgress {
	var p waveformProgress

	err := ss.pgPool.QueryRow(ctx, `
		select min(created_at), max(created_at) from uploads where template = 'audio'
	`).Scan(&p.Oldest, &p.Newest)
	if err != nil || p.Newest.IsZero() || !p.Newest.After(p.Oldest) {
		return p
	}
	p.Known = true

	span := p.Newest.Sub(p.Oldest)
	switch {
	case cur.Exhausted:
		p.Fraction = 1
	case cur.CreatedAt.IsZero():
		p.Fraction = 0 // not started, or just reset to the top
	default:
		p.Fraction = p.Newest.Sub(cur.CreatedAt).Seconds() / span.Seconds()
	}
	if p.Fraction < 0 {
		p.Fraction = 0
	}
	if p.Fraction > 1 {
		p.Fraction = 1
	}

	if cur.StartedAt.IsZero() {
		return p
	}
	p.Elapsed = time.Since(cur.StartedAt)

	// Below a few percent the projection is dominated by startup noise and
	// would read as a wild number, so leave it unset until there is signal.
	if p.Fraction >= 0.02 && p.Fraction < 1 {
		p.Remaining = time.Duration(float64(p.Elapsed) * (1 - p.Fraction) / p.Fraction)
	}
	return p
}

// waveformStatusProto assembles what the console shows. It is best-effort: the
// storage page is a diagnostic, so a failure here degrades to an empty section
// rather than taking the whole page down with it.
func (ss *MediorumServer) waveformStatusProto(ctx context.Context) *v1.WaveformStatus {
	status := &v1.WaveformStatus{
		Enabled:          ss.Config.WaveformEnabled,
		BackfillEnabled:  ss.Config.WaveformBackfillEnabled,
		ArchiveEnabled:   ss.Config.WaveformArchiveEnabled,
		Version:          int32(waveformVersion),
		AlgorithmVersion: int32(waveformAlgorithmVersion),
		Buckets:          int32(waveformBuckets),
		SampleRate:       int32(waveformSampleRate),
		ByUploadState:    map[string]int64{},
	}
	// Nothing has been written and no run exists, so skip the queries wholesale
	// rather than reporting zeros that look like a stalled backfill. The console
	// hides the section entirely in this case.
	if !ss.Config.WaveformEnabled {
		return status
	}

	ss.waveformRollupMu.Lock()
	for state, count := range ss.waveformRollup.byState {
		status.ByUploadState[state] = count
	}
	status.OrphanRows = ss.waveformRollup.orphanRows
	if !ss.waveformRollupAt.IsZero() {
		status.SampledAgeNs = int64(time.Since(ss.waveformRollupAt))
	}
	ss.waveformRollupMu.Unlock()

	if ss.metrics != nil {
		status.Requests = &v1.WaveformRequestStats{
			Served:     ss.metrics.waveformServed.Load(),
			Misses:     ss.metrics.waveformMisses.Load(),
			Redirected: ss.metrics.waveformRedirected.Load(),
		}
	}

	cur, err := ss.getWaveformCursor(ctx)
	if err != nil {
		ss.logger.Warn("waveform cursor read failed", zap.Error(err))
		return status
	}
	if cur.StartedAt.IsZero() && cur.CreatedAt.IsZero() && !cur.Exhausted {
		return status // no pass has begun
	}

	p := ss.waveformRunProgress(ctx, cur)
	run := &v1.WaveformRun{
		Exhausted:      cur.Exhausted,
		Queued:         cur.Queued,
		ArchiveSkipped: cur.Skipped,
		Fraction:       p.Fraction,
		ElapsedNs:      int64(p.Elapsed),
		RemainingNs:    int64(p.Remaining),
		Version:        int32(cur.Version),
	}
	if !cur.StartedAt.IsZero() {
		run.StartedAt = timestamppb.New(cur.StartedAt)
	}
	if !cur.UpdatedAt.IsZero() {
		run.UpdatedAt = timestamppb.New(cur.UpdatedAt)
	}
	if !cur.CreatedAt.IsZero() {
		run.CursorCreatedAt = timestamppb.New(cur.CreatedAt)
	}
	status.Run = run
	return status
}

// -- serving --------------------------------------------------------------

type waveformResponse struct {
	CID        string `json:"cid"`
	Version    int    `json:"version"`
	Buckets    int    `json:"buckets"`
	SampleRate int    `json:"sample_rate"`
	DurationMs int64  `json:"duration_ms"`
	// Peaks are 0-255. Divide by 255 for a 0..1 envelope; pair with
	// duration_ms to render without fetching the audio.
	Peaks []byte `json:"peaks"`
}

func (ss *MediorumServer) serveWaveform(c echo.Context) error {
	ctx := c.Request().Context()
	cid := c.Param("cid")

	row, err := ss.getWaveform(ctx, cid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		ss.logger.Warn("waveform lookup failed", zap.String("cid", cid), zap.Error(err))
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "waveform lookup failed"})
	}

	if row != nil && row.Status == waveformStatusDone && len(row.Peaks) > 0 {
		// Counted before the HEAD short-circuit: a peer probing us is still a
		// request this node answered, and treating probes as invisible would
		// understate how much of the network leans on this node.
		ss.countWaveformRequest(&ss.metrics.waveformServed)
		// CID-addressed content never changes, so the result is immutable for
		// as long as the client cares to keep it.
		c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=31536000, immutable")
		if c.Request().Method == http.MethodHead {
			return c.NoContent(http.StatusOK)
		}
		return c.JSON(http.StatusOK, waveformResponse{
			CID:        row.CID,
			Version:    row.Version,
			Buckets:    row.Buckets,
			SampleRate: row.SampleRate,
			DurationMs: row.DurationMs,
			Peaks:      row.Peaks,
		})
	}

	// localOnly is what stops a redirect chain: peer probes set it, so a node
	// that also lacks the waveform answers 404 instead of forwarding onward.
	// Same mechanism serveBlob uses.
	if localOnly, _ := strconv.ParseBool(c.QueryParam("localOnly")); localOnly {
		// A probe answering "not here" is not a client-facing miss, so it is
		// deliberately not counted -- otherwise every peer search would inflate
		// this node's miss rate.
		return c.JSON(http.StatusNotFound, map[string]string{"error": "waveform not found"})
	}

	// Deliberately no on-demand analysis here. Waveforms are computed by the
	// transcode hook and the backfill sweep, both of which respect the
	// archive-tier guard; enqueueing from an unauthenticated GET would bypass
	// it, letting anyone trigger a cold-storage retrieval on a StoreAll node,
	// with no rate limiting in front of it.
	//
	// Pointing at a node that already has the answer is better than promising
	// to compute one.
	if host := ss.findNodeToServeWaveform(ctx, cid); host != "" {
		ss.countWaveformRequest(&ss.metrics.waveformRedirected)
		dest := ss.replaceHost(c, host)
		query := dest.Query()
		query.Add("allow_unhealthy", "true") // we confirmed the node has it
		dest.RawQuery = query.Encode()
		return c.Redirect(http.StatusFound, dest.String())
	}

	ss.countWaveformRequest(&ss.metrics.waveformMisses)
	return c.JSON(http.StatusNotFound, map[string]string{"error": "waveform not found"})
}

// countWaveformRequest guards the nil metrics case, which tests construct.
func (ss *MediorumServer) countWaveformRequest(counter *atomic.Int64) {
	if ss.metrics == nil {
		return
	}
	counter.Add(1)
}

// findNodeToServeWaveform picks a peer that already holds this waveform.
//
// Mirrors findNodeToServeBlob, with one difference that matters: holding the
// blob does not imply holding the waveform. Waveforms are not replicated and a
// peer may have the feature switched off entirely, so peers are probed for the
// waveform itself rather than reusing hostHasBlob. Rendezvous order is still
// the right search order -- those nodes are the ones that should hold the blob,
// so they are the ones most likely to have analyzed it.
func (ss *MediorumServer) findNodeToServeWaveform(ctx context.Context, cid string) string {
	if host, ok := ss.waveformRedirectCache.Get(cid); ok {
		if ss.hostHasWaveform(ctx, host, cid) {
			return host
		}
		ss.waveformRedirectCache.Remove(cid)
	}

	hosts, _ := ss.rendezvousAllHosts(cid)
	attempts := 0
	for _, h := range hosts {
		if h == ss.Config.Self.Host {
			continue
		}
		if attempts >= waveformRedirectMaxProbes {
			break
		}
		attempts++
		if ss.hostHasWaveform(ctx, h, cid) {
			ss.waveformRedirectCache.Set(cid, h, imcache.WithDefaultExpiration())
			return h
		}
	}
	return ""
}

// hostHasWaveform probes a peer with HEAD and localOnly set, so the peer
// answers from its own table without forwarding the question along.
func (ss *MediorumServer) hostHasWaveform(ctx context.Context, host, cid string) bool {
	ctx, cancel := context.WithTimeout(ctx, waveformProbeTimeout)
	defer cancel()

	u := apiPath(host, "waveform", url.PathEscape(cid)) + "?localOnly=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return false
	}
	resp, err := ss.peerHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// analyzeWaveformFromFile computes and stores a waveform from a file already on
// local disk. Called from the transcode path, where the finished 320 is still
// present -- no bucket read, and therefore no egress, for new uploads.
func (ss *MediorumServer) analyzeWaveformFromFile(ctx context.Context, cid, uploadID, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	result, err := computeWaveform(ctx, f, info.Size())
	if err != nil {
		if markErr := ss.markWaveformError(ctx, cid, uploadID, err); markErr != nil {
			ss.logger.Error("failed to record waveform error", zap.String("cid", cid), zap.Error(markErr))
		}
		return err
	}
	return ss.upsertWaveform(ctx, cid, uploadID, result)
}

// setWaveformCursorExhausted marks a pass complete. updated_at is what the
// re-walk timer reads, so it must be stamped here rather than left alone.
func (ss *MediorumServer) setWaveformCursorExhausted(ctx context.Context) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveform_cursor (id, exhausted, version, updated_at)
		values (1, true, $1, now())
		on conflict (id) do update set
			exhausted = true,
			version = excluded.version,
			updated_at = now()
	`, waveformVersion)
	return err
}
