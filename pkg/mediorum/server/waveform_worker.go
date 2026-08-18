package server

import (
	"context"
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
)

// waveformJob is one unit of work: analyze the audio behind a CID.
type waveformJob struct {
	cid string
	// placementHosts feeds isArchiveCID, which needs it to tell "this node
	// holds the CID only because StoreAll" from an explicit placement.
	placementHosts []string
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

	// Workers stay up even without backfill so the live path keeps working;
	// only the historical walk is switched off here.
	if !ss.Config.WaveformBackfillEnabled {
		ss.logger.Info("waveform backfill disabled; live path only")
		<-ctx.Done()
		return ctx.Err()
	}

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

	busy := ss.sweepWaveformRetries(ctx)
	return ss.sweepWaveformDiscovery(ctx) || busy
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
	if !ss.Config.WaveformArchiveEnabled && ss.isArchiveCID(job.cid, job.placementHosts) {
		if err := ss.markWaveformStatus(ctx, job.cid, waveformStatusArchiveSkipped, nil, waveformRetryBackoffNotLocal); err != nil {
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
		return ss.recordWaveformReadFailure(ctx, job.cid, err)
	}

	r, _, err := ss.readBlob(ctx, key)
	if err != nil {
		return ss.recordWaveformReadFailure(ctx, job.cid, err)
	}
	defer r.Close()

	result, err := computeWaveform(ctx, r, attrs.Size)
	if err != nil {
		// We read bytes and failed to make sense of them. That is a property
		// of the content, so it counts against the retry cap.
		if markErr := ss.markWaveformError(ctx, job.cid, err); markErr != nil {
			ss.logger.Error("failed to record waveform error", zap.String("cid", job.cid), zap.Error(markErr))
		}
		return err
	}

	return ss.upsertWaveform(ctx, job.cid, result)
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
func (ss *MediorumServer) recordWaveformReadFailure(ctx context.Context, cid string, readErr error) error {
	if gcerrors.Code(readErr) == gcerrors.NotFound {
		if err := ss.markWaveformStatus(ctx, cid, waveformStatusNotLocal, readErr, waveformRetryBackoffNotLocal); err != nil {
			return err
		}
		return fmt.Errorf("waveform: blob not on this node: %w", readErr)
	}
	if err := ss.markWaveformStatus(ctx, cid, waveformStatusUnavailable, readErr, waveformRetryBackoffUnavailable); err != nil {
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
// Returns whether it queued anything, which keeps the sweep interval short
// while a backlog is draining.
func (ss *MediorumServer) sweepWaveformRetries(ctx context.Context) bool {
	rows, err := ss.pgPool.Query(ctx, `
		select cid from waveforms
		where status <> $1
		  and (status <> $2 or $3)
		  and coalesce(error_count, 0) < $4
		  and next_attempt_at is not null
		  and next_attempt_at <= now()
		order by next_attempt_at
		limit $5
	`, waveformStatusDone, waveformStatusArchiveSkipped, ss.Config.WaveformArchiveEnabled, waveformMaxTries, waveformSweepBatchLimit)
	if err != nil {
		ss.logger.Warn("waveform retry sweep failed", zap.Error(err))
		return false
	}
	defer rows.Close()

	cids := []string{}
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			ss.logger.Warn("waveform retry scan failed", zap.Error(err))
			return false
		}
		cids = append(cids, cid)
	}
	if err := rows.Err(); err != nil {
		ss.logger.Warn("waveform retry sweep failed", zap.Error(err))
		return false
	}

	queued := 0
	for _, cid := range cids {
		if ctx.Err() != nil {
			break
		}
		// A full queue is not a problem here: these rows carry their own
		// next_attempt_at, so whatever does not fit is simply picked up again.
		if !ss.enqueueWaveformJob(waveformJob{cid: cid}) {
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

			cid, ok := upload.TranscodeResults["320"]
			if !ok || cid == "" {
				advanceTo = &upload
				continue
			}

			// An optimization, not the guarantee -- analyzeWaveform re-checks
			// immediately before its first bucket call, since a cid's tier can
			// change between being queued and being read. Filtering here just
			// keeps work that would be skipped anyway out of the queue.
			//
			// Recorded rather than silently skipped: the status doubles as the
			// re-sweep predicate if the operator later opts in, and it makes
			// the held-back population visible in the status endpoint before
			// they commit to paying for it.
			if !ss.Config.WaveformArchiveEnabled && ss.isArchiveCID(cid, upload.PlacementHosts) {
				if err := ss.markWaveformStatus(ctx, cid, waveformStatusArchiveSkipped, nil, waveformRetryBackoffNotLocal); err != nil {
					ss.logger.Warn("failed to record archive skip", zap.String("cid", cid), zap.Error(err))
				}
				skippedArchive++
				batchSkipped++
				advanceTo = &upload
				continue
			}

			if !ss.enqueueWaveformJob(waveformJob{cid: cid, placementHosts: upload.PlacementHosts}) {
				full = true
				break
			}
			queued++
			batchQueued++
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
		// A row stamped with a different version was computed under different
		// rules, so it counts as absent and gets recomputed. This is the whole
		// re-backfill mechanism: no separate sweep, no table rewrite.
		Where(`not exists (
			select 1 from waveforms w
			where w.cid = transcode_results::jsonb ->> '320'
			  and w.version = ?
		)`, waveformVersion)

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

func (ss *MediorumServer) upsertWaveform(ctx context.Context, cid string, result *waveformResult) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms
			(cid, peaks, buckets, version, sample_rate, sample_count, duration_ms,
			 status, error, error_count, analyzed_at, next_attempt_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, '', 0, now(), null)
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
			analyzed_at = now(),
			next_attempt_at = null
	`, cid, result.Peaks, waveformBuckets, waveformVersion,
		result.SampleRate, result.SampleCount, result.DurationMs, waveformStatusDone)
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
func (ss *MediorumServer) markWaveformStatus(ctx context.Context, cid, status string, cause error, backoff time.Duration) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms (cid, status, error, error_count, version, analyzed_at, next_attempt_at)
		values ($1, $2, $3, 0, $4, now(), now() + $5::interval)
		on conflict (cid) do update set
			status = excluded.status,
			error = excluded.error,
			version = excluded.version,
			analyzed_at = now(),
			next_attempt_at = excluded.next_attempt_at
	`, cid, status, msg, waveformVersion, fmt.Sprintf("%d seconds", int(backoff.Seconds())))
	return err
}

// markWaveformError records a decode failure and advances the retry counter.
// Stamped with the current version for the same reason as markWaveformStatus:
// otherwise discovery re-enqueues the row on every re-walk and the backoff
// never applies. A row at the retry cap then stays put rather than being
// retried forever by the back door.
func (ss *MediorumServer) markWaveformError(ctx context.Context, cid string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms (cid, status, error, error_count, version, analyzed_at, next_attempt_at)
		values ($1, $2, $3, 1, $4, now(), now() + $5::interval)
		on conflict (cid) do update set
			status = excluded.status,
			error = excluded.error,
			error_count = coalesce(waveforms.error_count, 0) + 1,
			version = excluded.version,
			analyzed_at = now(),
			next_attempt_at = excluded.next_attempt_at
	`, cid, waveformStatusError, msg, waveformVersion, fmt.Sprintf("%d seconds", int(waveformRetryBackoffError.Seconds())))
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
		ByStatus:         map[string]int64{},
	}
	// Nothing has been written and no run exists, so skip the queries wholesale
	// rather than reporting zeros that look like a stalled backfill. The console
	// hides the section entirely in this case.
	if !ss.Config.WaveformEnabled {
		return status
	}

	if ss.metrics != nil {
		status.Requests = &v1.WaveformRequestStats{
			Served:     ss.metrics.waveformServed.Load(),
			Misses:     ss.metrics.waveformMisses.Load(),
			Redirected: ss.metrics.waveformRedirected.Load(),
		}
	}

	rows, err := ss.pgPool.Query(ctx, `
		select status, count(*)::bigint,
		       count(*) filter (where status = $1 and version <> $2)::bigint
		from waveforms group by status
	`, waveformStatusDone, waveformVersion)
	if err != nil {
		ss.logger.Warn("waveform status query failed", zap.Error(err))
		return status
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var count, stale int64
		if err := rows.Scan(&name, &count, &stale); err != nil {
			ss.logger.Warn("waveform status scan failed", zap.Error(err))
			return status
		}
		status.ByStatus[name] = count
		status.StaleVersion += stale
	}
	if err := rows.Err(); err != nil {
		ss.logger.Warn("waveform status query failed", zap.Error(err))
		return status
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
func (ss *MediorumServer) analyzeWaveformFromFile(ctx context.Context, cid, path string) error {
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
		if markErr := ss.markWaveformError(ctx, cid, err); markErr != nil {
			ss.logger.Error("failed to record waveform error", zap.String("cid", cid), zap.Error(markErr))
		}
		return err
	}
	return ss.upsertWaveform(ctx, cid, result)
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
