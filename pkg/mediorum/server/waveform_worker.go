package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
	"gocloud.dev/gcerrors"
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

	// Workers stay up even without backfill so the route can still service
	// on-demand analysis; only the historical walk is switched off here.
	if !ss.Config.WaveformBackfillEnabled {
		ss.logger.Info("waveform backfill disabled; live path and on-demand only")
		<-ctx.Done()
		return ctx.Err()
	}

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ticker.Reset(5 * time.Minute) // lengthen after the first pass
			ss.sweepWaveformRetries(ctx)
			ss.sweepWaveformDiscovery(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
func (ss *MediorumServer) sweepWaveformRetries(ctx context.Context) {
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
		return
	}
	defer rows.Close()

	cids := []string{}
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			ss.logger.Warn("waveform retry scan failed", zap.Error(err))
			return
		}
		cids = append(cids, cid)
	}
	if err := rows.Err(); err != nil {
		ss.logger.Warn("waveform retry sweep failed", zap.Error(err))
		return
	}

	for _, cid := range cids {
		if ctx.Err() != nil {
			return
		}
		ss.enqueueWaveformJob(waveformJob{cid: cid})
	}
}

// sweepWaveformDiscovery walks uploads newest-first looking for 320s that have
// no waveforms row yet.
//
// Newest-first matters: the recent slice is what anyone actually looks at, and
// it lands in days rather than after a full multi-week history walk.
func (ss *MediorumServer) sweepWaveformDiscovery(ctx context.Context) {
	cur, err := ss.getWaveformCursor(ctx)
	if err != nil {
		ss.logger.Warn("waveform cursor read failed", zap.Error(err))
		return
	}

	// A version change means every stored waveform was computed under different
	// rules, so the walk starts over. nextWaveformUploadBatch only counts rows
	// at the current version as present, so the re-walk finds them all.
	if cur.Version != waveformVersion {
		ss.logger.Info("waveform version changed; restarting backfill",
			zap.Int("was", cur.Version), zap.Int("now", waveformVersion))
		if err := ss.resetWaveformCursor(ctx); err != nil {
			ss.logger.Warn("waveform cursor reset failed", zap.Error(err))
			return
		}
		cur = waveformCursor{Version: waveformVersion}
	}

	// Reaching the end of history is not the end of the job. Uploads replicate
	// from peers, so this node keeps learning about older ones after the walk
	// has passed their position -- and a descending cursor never goes back for
	// them. Latching exhausted forever left those without waveforms for good.
	//
	// Re-walking is cheap despite the table size: the not-exists filter means a
	// batch skips straight to rows that still lack a waveform, so a converged
	// backfill costs one query that returns nothing rather than a full scan per
	// batch.
	if cur.Exhausted {
		if time.Since(cur.UpdatedAt) < waveformRewalkInterval {
			return
		}
		ss.logger.Info("waveform backfill re-walking history for late-arriving uploads")
		if err := ss.resetWaveformCursor(ctx); err != nil {
			ss.logger.Warn("waveform cursor reset failed", zap.Error(err))
			return
		}
		cur = waveformCursor{Version: waveformVersion}
	}

	cursorTime, cursorID := cur.CreatedAt, cur.UploadID

	uploads, err := ss.nextWaveformUploadBatch(ctx, cursorTime, cursorID, waveformDiscoveryLimit)
	if err != nil {
		ss.logger.Warn("waveform discovery query failed", zap.Error(err))
		return
	}
	if len(uploads) == 0 {
		if err := ss.setWaveformCursorExhausted(ctx); err != nil {
			ss.logger.Warn("waveform cursor update failed", zap.Error(err))
		}
		ss.logger.Info("waveform backfill reached end of history")
		return
	}

	skippedArchive := 0
	for _, upload := range uploads {
		if ctx.Err() != nil {
			return
		}
		cid, ok := upload.TranscodeResults["320"]
		if !ok || cid == "" {
			continue
		}

		// Answered from the hash ring with no bucket call, so filtering here
		// costs nothing and keeps a StoreAll backfill off cold storage.
		//
		// Recorded rather than silently skipped: the status doubles as the
		// re-sweep predicate if the operator later opts in, and it makes the
		// held-back population visible in the status endpoint before they
		// commit to paying for it.
		if !ss.Config.WaveformArchiveEnabled && ss.isArchiveCID(cid, upload.PlacementHosts) {
			if err := ss.markWaveformStatus(ctx, cid, waveformStatusArchiveSkipped, nil, waveformRetryBackoffNotLocal); err != nil {
				ss.logger.Warn("failed to record archive skip", zap.String("cid", cid), zap.Error(err))
			}
			skippedArchive++
			continue
		}

		ss.enqueueWaveformJob(waveformJob{cid: cid, placementHosts: upload.PlacementHosts})
	}

	last := uploads[len(uploads)-1]
	if err := ss.setWaveformCursor(ctx, last.CreatedAt, last.ID); err != nil {
		ss.logger.Warn("waveform cursor update failed", zap.Error(err))
	}
	if skippedArchive > 0 {
		ss.logger.Info("waveform discovery skipped archive-tier cids",
			zap.Int("skipped", skippedArchive),
			zap.Int("batch", len(uploads)))
	}
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
func (ss *MediorumServer) markWaveformStatus(ctx context.Context, cid, status string, cause error, backoff time.Duration) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms (cid, status, error, error_count, analyzed_at, next_attempt_at)
		values ($1, $2, $3, 0, now(), now() + $4::interval)
		on conflict (cid) do update set
			status = excluded.status,
			error = excluded.error,
			analyzed_at = now(),
			next_attempt_at = excluded.next_attempt_at
	`, cid, status, msg, fmt.Sprintf("%d seconds", int(backoff.Seconds())))
	return err
}

// markWaveformError records a decode failure and advances the retry counter.
func (ss *MediorumServer) markWaveformError(ctx context.Context, cid string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveforms (cid, status, error, error_count, analyzed_at, next_attempt_at)
		values ($1, $2, $3, 1, now(), now() + $4::interval)
		on conflict (cid) do update set
			status = excluded.status,
			error = excluded.error,
			error_count = coalesce(waveforms.error_count, 0) + 1,
			analyzed_at = now(),
			next_attempt_at = excluded.next_attempt_at
	`, cid, waveformStatusError, msg, fmt.Sprintf("%d seconds", int(waveformRetryBackoffError.Seconds())))
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

// waveformCursor is where the newest-first backfill walk has reached, and the
// waveform version it was walking under.
type waveformCursor struct {
	CreatedAt time.Time
	UploadID  string
	Exhausted bool
	Version   int
	UpdatedAt time.Time
}

func (ss *MediorumServer) getWaveformCursor(ctx context.Context) (waveformCursor, error) {
	var (
		createdAt *time.Time
		uploadID  *string
		version   *int
		cur       waveformCursor
	)
	err := ss.pgPool.QueryRow(ctx,
		`select created_at, upload_id, exhausted, version, updated_at from waveform_cursor where id = 1`,
	).Scan(&createdAt, &uploadID, &cur.Exhausted, &version, &cur.UpdatedAt)
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
	return cur, nil
}

// resetWaveformCursor sends the walk back to the newest upload and stamps the
// version it is now walking under.
func (ss *MediorumServer) resetWaveformCursor(ctx context.Context) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveform_cursor (id, created_at, upload_id, exhausted, version, updated_at)
		values (1, null, null, false, $1, now())
		on conflict (id) do update set
			created_at = null,
			upload_id = null,
			exhausted = false,
			version = excluded.version,
			updated_at = now()
	`, waveformVersion)
	return err
}

func (ss *MediorumServer) setWaveformCursor(ctx context.Context, createdAt time.Time, uploadID string) error {
	_, err := ss.pgPool.Exec(ctx, `
		insert into waveform_cursor (id, created_at, upload_id, exhausted, updated_at)
		values (1, $1, $2, false, now())
		on conflict (id) do update set
			created_at = excluded.created_at,
			upload_id = excluded.upload_id,
			updated_at = now()
	`, createdAt, uploadID)
	return err
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
		dest := ss.replaceHost(c, host)
		query := dest.Query()
		query.Add("allow_unhealthy", "true") // we confirmed the node has it
		dest.RawQuery = query.Encode()
		return c.Redirect(http.StatusFound, dest.String())
	}

	return c.JSON(http.StatusNotFound, map[string]string{"error": "waveform not found"})
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
