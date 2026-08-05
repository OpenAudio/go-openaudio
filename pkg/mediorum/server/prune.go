package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
	"gocloud.dev/blob"
)

// The prune job reclaims storage and stops repair from chasing content that is
// never coming back. It runs only on an explicit operator request -- every task
// here is either expensive (a full tree walk, a network probe per CID) or
// destructive, so none of it belongs on a timer.
//
// Nothing in this file deletes an `uploads` row. Upload is a crudr-registered
// model, so crud.Delete emits an op that gossips to every peer -- one node's
// janitorial decision would become a network-wide deletion. Instead the job
// deletes *local blobs* and records CIDs in the local `prune_skips` table,
// which repair consults to stop re-pulling them. That achieves the goal (no
// endless repair churn, space reclaimed) without touching replicated state.

type pruneTask string

const (
	// pruneTaskTmp removes orphaned fileblob ".tmp" files left by writes that
	// died before Close could rename them into place.
	pruneTaskTmp pruneTask = "tmp"

	// pruneTaskUnpublished removes blobs for uploads that never finished
	// transcoding and are old enough that they never will.
	pruneTaskUnpublished pruneTask = "unpublished"
)

// Detecting content that no longer exists anywhere used to be a prune task
// here, probing peers per CID. It now lives in repair (see
// repair_data_loss.go): repair already attempts those pulls, and failures
// accumulated across many cycles on different days are much stronger evidence
// than a single burst of probes, which a transient outage reproduces exactly.

const (
	// unpublishedUploadAge is how long an upload must have gone unreferenced by
	// any track before its blobs are considered abandoned. Publishing normally
	// follows an upload within minutes; a month is a wide margin for a client
	// that uploaded, stalled, and came back.
	unpublishedUploadAge = 30 * 24 * time.Hour

	// prunePageSize bounds a single scan so a prune never holds a huge result
	// set or runs unbounded against a 2M+ row table.
	prunePageSize = 1000
)

// pruneRequest selects which tasks to run. DryRun is the default: the caller
// must opt in to mutation, because two of the three tasks delete data.
type pruneRequest struct {
	Tasks []pruneTask `json:"tasks"`
	// Templates scopes the unpublished task by upload type. Defaults to audio
	// alone: it is the best-evidenced case, and each template needs its own
	// reference check against different tables.
	Templates []JobTemplate `json:"templates"`
	Commit    bool          `json:"commit"`
	Limit     int           `json:"limit"`
}

// pruneTemplates resolves the requested scope, defaulting to audio.
func (req pruneRequest) pruneTemplates() []JobTemplate {
	if len(req.Templates) == 0 {
		return []JobTemplate{JobTemplateAudio}
	}
	return req.Templates
}

type pruneResult struct {
	Task     pruneTask     `json:"task"`
	DryRun   bool          `json:"dryRun"`
	Scanned  int           `json:"scanned"`
	Matched  int           `json:"matched"`
	Removed  int           `json:"removed"`
	SkipsAdd int           `json:"skipsAdded"`
	Duration time.Duration `json:"-"`
	Took     string        `json:"took"`
	Error    string        `json:"error,omitempty"`
}

// startPruner waits for prune requests and runs them under the lifecycle
// context so a long walk or probe stops promptly on shutdown.
func (ss *MediorumServer) startPruner(ctx context.Context) error {
	for {
		select {
		case req := <-ss.pruneTrigger:
			ss.runPrune(ctx, req)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// pruneProgressInterval is how often an in-flight task flushes its counters to
// prune_runs. Frequent enough that a stalled task is obvious within a minute,
// rare enough to be invisible next to a tree walk or a network probe.
const pruneProgressInterval = 10 * time.Second

// pruneRun owns the prune_runs row for one task and flushes progress to it.
// Without this a long task is silent until it finishes -- and if the node
// restarts mid-run, silent forever.
type pruneRun struct {
	ss        *MediorumServer
	id        int64
	res       pruneResult
	lastFlush time.Time
}

func (ss *MediorumServer) beginPruneRun(ctx context.Context, task pruneTask, dryRun bool) *pruneRun {
	run := &pruneRun{ss: ss, lastFlush: time.Now()}
	run.res.Task = task
	run.res.DryRun = dryRun

	err := ss.pgPool.QueryRow(ctx,
		`insert into prune_runs (task, dry_run) values ($1, $2) returning id`,
		string(task), dryRun).Scan(&run.id)
	if err != nil {
		// Losing the history row must not stop the actual work; the task still
		// runs and still logs, we just cannot report progress for it.
		ss.logger.Warn("could not record prune run", zap.Error(err))
	}
	return run
}

// tick flushes counters if enough time has passed. Safe to call in a tight
// loop -- it throttles internally.
func (run *pruneRun) tick(ctx context.Context) {
	if run.id == 0 || time.Since(run.lastFlush) < pruneProgressInterval {
		return
	}
	run.flush(ctx, false)
}

func (run *pruneRun) flush(ctx context.Context, done bool) {
	if run.id == 0 {
		return
	}
	run.lastFlush = time.Now()

	// Use a background context for the final write: a cancelled prune should
	// still record how far it got, otherwise a shutdown mid-run looks like a
	// run that never happened.
	writeCtx := ctx
	if done {
		writeCtx = context.Background()
	}
	_, err := run.ss.pgPool.Exec(writeCtx, `
		update prune_runs
		   set updated_at = now(),
		       finished_at = case when $2 then now() else finished_at end,
		       scanned = $3, matched = $4, removed = $5, skips_added = $6, error = $7
		 where id = $1`,
		run.id, done, run.res.Scanned, run.res.Matched, run.res.Removed,
		run.res.SkipsAdd, run.res.Error)
	if err != nil {
		run.ss.logger.Warn("could not update prune run", zap.Error(err))
	}
}

func (ss *MediorumServer) runPrune(ctx context.Context, req pruneRequest) []pruneResult {
	limit := req.Limit
	if limit <= 0 {
		limit = prunePageSize
	}

	results := make([]pruneResult, 0, len(req.Tasks))
	for _, task := range req.Tasks {
		if ctx.Err() != nil {
			return results
		}
		logger := ss.logger.With(
			zap.String("task", "prune"),
			zap.String("prune_task", string(task)),
			zap.Bool("dryRun", !req.Commit))
		logger.Info("prune task starting")

		start := time.Now()
		run := ss.beginPruneRun(ctx, task, !req.Commit)

		switch task {
		case pruneTaskTmp:
			ss.pruneTmpFiles(ctx, run, req.Commit)
		case pruneTaskUnpublished:
			ss.pruneUnpublishedUploads(ctx, run, req.Commit, limit, req.pruneTemplates())
		default:
			run.res.Error = "unknown prune task"
		}

		res := run.res
		res.Duration = time.Since(start)
		res.Took = res.Duration.String()
		run.res = res
		run.flush(ctx, true)

		if res.Error != "" {
			logger.Warn("prune task failed",
				zap.String("error", res.Error), zap.Duration("took", res.Duration))
		} else {
			logger.Info("prune task complete",
				zap.Int("scanned", res.Scanned), zap.Int("matched", res.Matched),
				zap.Int("removed", res.Removed), zap.Int("skipsAdded", res.SkipsAdd),
				zap.Duration("took", res.Duration))
		}
		results = append(results, res)
	}
	return results
}

// pruneTmpFiles walks every file:// bucket removing orphaned ".tmp" files.
//
// Most orphans are already reclaimed opportunistically by
// cleanupStaleTempsNearKey as a side effect of writing. This full traversal
// only catches the residue that leaves behind: a directory never written to
// again. It is expensive -- a recursive walk of the whole tree -- which is why
// it is on-demand rather than scheduled.
func (ss *MediorumServer) pruneTmpFiles(ctx context.Context, run *pruneRun, commit bool) {
	dsns := []string{ss.Config.BlobStoreDSN}
	if ss.Config.ArchiveBlobStoreDSN != "" {
		dsns = append(dsns, ss.Config.ArchiveBlobStoreDSN)
	}

	baseScanned, baseMatched := 0, 0
	for _, dsn := range dsns {
		if ctx.Err() != nil {
			return
		}
		if _, isFile := persistence.FileDirFromDSN(dsn); !isFile {
			// Cloud backends have no local tree. Their equivalent artifact is an
			// incomplete multipart upload, invisible to the keyspace and handled
			// by a bucket lifecycle rule rather than by us.
			continue
		}

		// The walk is the longest thing a prune does -- an hour on a
		// million-object archive. Stream counts out of it so the run row shows
		// movement instead of nothing until it completes.
		progress := func(scanned, matched int) {
			run.res.Scanned = baseScanned + scanned
			run.res.Matched = baseMatched + matched
			if commit {
				run.res.Removed = run.res.Matched
			}
			run.tick(ctx)
		}

		n, scanned, err := persistence.SweepStaleTempFiles(ctx, dsn, persistence.DefaultStaleTempFileAge, !commit, progress)
		baseScanned += scanned
		baseMatched += n
		run.res.Scanned = baseScanned
		run.res.Matched = baseMatched
		if commit {
			run.res.Removed = baseMatched
		}
		if err != nil {
			run.res.Error = err.Error()
			return
		}
	}
}

// pruneUnpublishedUploads reclaims blobs for audio uploads no track references.
//
// "Unpublished" means none of the upload's CIDs appears in tracks.track_cid,
// orig_file_cid or preview_cid -- no CreateTrack transaction ever pointed at
// the bytes. They were uploaded and abandoned, so nothing will serve them.
//
// Three floors keep this conservative: the upload must be older than
// unpublishedUploadAge, checkPruneIndex must certify the index is present,
// fresh, and carrying track_cid on nearly every row, and only audio templates
// are considered.
//
// The upload row itself is left alone (see the note at the top of this file);
// only local blobs go, and the CIDs are skip-listed so repair does not pull
// them straight back from a peer that hasn't pruned.
func (ss *MediorumServer) pruneUnpublishedUploads(ctx context.Context, run *pruneRun, commit bool, limit int, templates []JobTemplate) {
	pool, release, err := ss.openPruneIndex(ctx)
	if err != nil {
		run.res.Error = err.Error()
		return
	}
	defer release()

	for _, tmpl := range templates {
		if ctx.Err() != nil {
			return
		}
		// Each template is verified separately: audio and art are referenced
		// from different tables, so an index adequate for one says nothing
		// about the other.
		if err := checkPruneIndexFor(ctx, pool, tmpl); err != nil {
			run.res.Error = err.Error()
			return
		}
		if err := ss.pruneUnpublishedTemplate(ctx, run, pool, commit, limit, tmpl); err != nil {
			run.res.Error = err.Error()
			return
		}
	}
}

func (ss *MediorumServer) pruneUnpublishedTemplate(ctx context.Context, run *pruneRun, pool *pgxpool.Pool, commit bool, limit int, tmpl JobTemplate) error {
	cutoff := time.Now().Add(-unpublishedUploadAge)
	var uploads []Upload
	if err := ss.crud.DB.
		Where("template = ? AND created_at < ?", tmpl, cutoff).
		Order("created_at").
		Limit(limit).
		Find(&uploads).Error; err != nil {
		return err
	}
	run.res.Scanned += len(uploads)
	if len(uploads) == 0 {
		return nil
	}

	isAudio := tmpl == JobTemplateAudio

	// Audio is referenced by CID only; art by CID or upload ID.
	refsFor := uploadRefs
	if isAudio {
		refsFor = uploadCIDs
	}

	all := make([]string, 0, len(uploads)*3)
	for _, u := range uploads {
		all = append(all, refsFor(u)...)
	}

	var published map[string]struct{}
	var err error
	if isAudio {
		published, err = publishedCIDs(ctx, pool, all)
	} else {
		published, err = publishedArtRefs(ctx, pool, all)
	}
	if err != nil {
		return err
	}

	for _, u := range uploads {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		run.tick(ctx)

		cids := uploadCIDs(u)
		if len(cids) == 0 {
			continue
		}
		// A single reference means the upload published; never prune such a
		// set piecemeal.
		referenced := false
		for _, ref := range refsFor(u) {
			if _, ok := published[ref]; ok {
				referenced = true
				break
			}
		}
		if referenced {
			continue
		}
		run.res.Matched++

		if !commit {
			continue
		}
		for _, cid := range cids {
			if ss.haveInMyBucket(cid) {
				if err := ss.dropFromMyBucket(cid); err == nil {
					run.res.Removed++
				}
			}
		}
		if n, err := ss.addPruneSkips(ctx, cids, "unpublished"); err == nil {
			run.res.SkipsAdd += n
		}
	}
	return nil
}

// Publication is determined by matching an upload's CIDs against the ETL's
// `tracks` table, which is built from CreateTrack entity-manager transactions.
//
// It deliberately does NOT use tracks.audio_upload_id. That column is
// client-supplied metadata carried in CreateTrack rather than something the
// protocol derives, and it is nullable. Measured against a production snapshot
// (1,463,753 current, non-deleted tracks):
//
//	audio_upload_id    39.1%
//	track_cid          99.96%
//	orig_file_cid      99.90%
//	preview_cid         2.1%   (previews are genuinely rare)
//
// Relying on audio_upload_id would classify the ~61% of published tracks that
// lack it as unpublished, and delete their audio. track_cid and orig_file_cid
// are both near-complete, so publication is matched on CIDs.
//
// Scope is audio only, and that is a safety property rather than an omission.
// Image CIDs live in tracks.cover_art_sizes, users.profile_picture_sizes,
// users.cover_photo_sizes and playlists.playlist_image_sizes_multihash -- never
// in track_cid. Running this over img_square/img_backdrop uploads would find no
// match for any of them and delete every piece of cover art on the node.
// Pruning art needs its own reference check across those tables.

// pruneIndexStaleAfter is how far behind the ETL may be before its answers stop
// counting as evidence. A stale index still lists old tracks correctly, but it
// would report recently published uploads as unpublished.
const pruneIndexStaleAfter = 24 * time.Hour

// minTrackCidCoverage is the share of current tracks that must carry a
// track_cid before the column is trusted. This is the guard that audio_upload_id
// would have failed: a fully populated tracks table whose signal column is
// empty otherwise reads as "nothing is published".
const minTrackCidCoverage = 0.90

// minArtReferences is the floor for artwork references before art pruning is
// allowed. A real index carries millions -- a production snapshot has ~3.6M
// across tracks, users and playlists -- so anything near zero means this node
// is not indexing, not that nothing has artwork.
const minArtReferences = 100_000

// openPruneIndex returns a pool for publication lookups plus a release func.
// When OPENAUDIO_PRUNE_INDEX_DB_URL is set it dials that database (for nodes
// that don't run the ETL themselves); otherwise it reuses the local pool.
func (ss *MediorumServer) openPruneIndex(ctx context.Context) (*pgxpool.Pool, func(), error) {
	dsn := env.String("OPENAUDIO_PRUNE_INDEX_DB_URL")
	if dsn == "" {
		return ss.pgPool, func() {}, nil
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("OPENAUDIO_PRUNE_INDEX_DB_URL: %w", err)
	}
	return pool, pool.Close, nil
}

// checkPruneIndexFor verifies the index can answer for a given upload type.
// Audio and art are referenced from different tables, so adequacy for one
// implies nothing about the other.
func checkPruneIndexFor(ctx context.Context, pool *pgxpool.Pool, tmpl JobTemplate) error {
	if tmpl == JobTemplateAudio {
		return checkPruneIndex(ctx, pool)
	}
	return checkArtPruneIndex(ctx, pool)
}

// checkArtPruneIndex refuses when the artwork columns are too sparse to
// distinguish "not referenced" from "not indexed". The failure mode mirrors the
// audio_upload_id one: a populated tracks table whose signal column is blank
// would authorise deleting every image on the node.
func checkArtPruneIndex(ctx context.Context, pool *pgxpool.Pool) error {
	var refs int64
	err := pool.QueryRow(ctx, `
		select (select count(*) from tracks    where is_current and coalesce(cover_art_sizes,'') <> '')
		     + (select count(*) from users     where is_current and coalesce(profile_picture_sizes,'') <> '')
		     + (select count(*) from playlists where is_current and coalesce(playlist_image_sizes_multihash,'') <> '')`).
		Scan(&refs)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return fmt.Errorf("artwork reference tables unavailable: this node does not index the chain. " +
				"Enable OPENAUDIO_ETL_ENABLED, or set OPENAUDIO_PRUNE_INDEX_DB_URL to a node that does")
		}
		return err
	}
	if refs < minArtReferences {
		return fmt.Errorf("only %d artwork references indexed (need %d): "+
			"too sparse to distinguish unreferenced art from an unindexed node", refs, minArtReferences)
	}
	return nil
}

// checkPruneIndex refuses to authorise deletion from an index that cannot
// support the inference. Each failure mode here would otherwise read as
// "nothing is published".
func checkPruneIndex(ctx context.Context, pool *pgxpool.Pool) error {
	var total, withCid int64
	err := pool.QueryRow(ctx, `
		select count(*),
		       count(*) filter (where track_cid is not null and track_cid <> '')
		  from tracks where is_current = true`).Scan(&total, &withCid)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return fmt.Errorf("no tracks table: this node does not index the chain. " +
				"Enable OPENAUDIO_ETL_ENABLED, or set OPENAUDIO_PRUNE_INDEX_DB_URL to a node that does")
		}
		return err
	}
	if total == 0 {
		return fmt.Errorf("tracks table is empty: refusing to treat every upload as unpublished")
	}
	if coverage := float64(withCid) / float64(total); coverage < minTrackCidCoverage {
		return fmt.Errorf("only %.1f%% of %d tracks carry a track_cid (need %.0f%%): "+
			"the signal column is too sparse to distinguish unpublished from unindexed",
			coverage*100, total, minTrackCidCoverage*100)
	}

	// Freshness is best-effort: an index without etl_blocks still answers
	// correctly for older uploads, and the age floor on candidates already
	// excludes anything recent.
	var newest *time.Time
	if err := pool.QueryRow(ctx, `select max(block_time) from etl_blocks`).Scan(&newest); err == nil {
		if newest != nil && time.Since(*newest) > pruneIndexStaleAfter {
			return fmt.Errorf("index is %s behind (newest block %s): too stale to judge publication",
				time.Since(*newest).Truncate(time.Minute), newest.Format(time.RFC3339))
		}
	}
	return nil
}

// publishedCIDs returns the subset of cids referenced by any track. Batched
// per page rather than per upload: only track_cid is indexed, so the
// orig_file_cid and preview_cid arms each cost a scan.
func publishedCIDs(ctx context.Context, pool *pgxpool.Pool, cids []string) (map[string]struct{}, error) {
	published := map[string]struct{}{}
	if len(cids) == 0 {
		return published, nil
	}
	rows, err := pool.Query(ctx, `
		select track_cid     from tracks where track_cid     = any($1)
		union
		select orig_file_cid from tracks where orig_file_cid = any($1)
		union
		select preview_cid   from tracks where preview_cid   = any($1)`, cids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid *string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		if cid != nil && *cid != "" {
			published[*cid] = struct{}{}
		}
	}
	return published, rows.Err()
}

// artReferenceColumns are every place an image upload can be referenced.
// The "_sizes"/"_multihash" variants dominate; the bare columns are legacy but
// still carry a few thousand rows.
var artReferenceColumns = []struct{ table, column string }{
	{"tracks", "cover_art_sizes"},
	{"tracks", "cover_art"},
	{"users", "profile_picture_sizes"},
	{"users", "profile_picture"},
	{"users", "cover_photo_sizes"},
	{"users", "cover_photo"},
	{"playlists", "playlist_image_sizes_multihash"},
	{"playlists", "playlist_image_multihash"},
}

// publishedArtRefs returns which of refs are referenced as artwork anywhere.
//
// Art is referenced by EITHER a CID or a mediorum upload ID, depending on which
// client era wrote it. Measured on a production snapshot, roughly a third to a
// half of every art column holds a ULID-form upload ID rather than a CID:
//
//	tracks.cover_art_sizes        65.3% CIDv0, 3.1% CIDv1, 31.6% ULID
//	users.profile_picture_sizes   53.0% CIDv0, 0.8% CIDv1, 46.2% ULID
//
// Matching only CIDs would therefore read ~40% of published artwork as
// orphaned. Callers pass both the upload ID and its CIDs.
//
// None of these columns is indexed, so each arm is a scan -- hence batching per
// page rather than per upload.
func publishedArtRefs(ctx context.Context, pool *pgxpool.Pool, refs []string) (map[string]struct{}, error) {
	published := map[string]struct{}{}
	if len(refs) == 0 {
		return published, nil
	}

	parts := make([]string, 0, len(artReferenceColumns))
	for _, c := range artReferenceColumns {
		parts = append(parts, fmt.Sprintf("select %s as ref from %s where %s = any($1)", c.column, c.table, c.column))
	}
	rows, err := pool.Query(ctx, strings.Join(parts, "\nunion\n"), refs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref *string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		if ref != nil && *ref != "" {
			published[*ref] = struct{}{}
		}
	}
	return published, rows.Err()
}

// uploadRefs returns everything that could identify an upload in an entity
// reference: its ID plus all its CIDs.
func uploadRefs(u Upload) []string {
	refs := uploadCIDs(u)
	if u.ID != "" {
		refs = append(refs, u.ID)
	}
	return refs
}

// uploadCIDs returns every CID an upload owns: the original plus any transcode
// outputs. Empty entries are skipped so a partially transcoded upload does not
// yield blank keys.
func uploadCIDs(u Upload) []string {
	cids := make([]string, 0, 1+len(u.TranscodeResults))
	if u.OrigFileCID != "" {
		cids = append(cids, u.OrigFileCID)
	}
	for _, cid := range u.TranscodeResults {
		if cid != "" {
			cids = append(cids, cid)
		}
	}
	return cids
}

func (ss *MediorumServer) addPruneSkips(ctx context.Context, cids []string, reason string) (int, error) {
	added := 0
	for _, cid := range cids {
		if cid == "" {
			continue
		}
		tag, err := ss.pgPool.Exec(ctx,
			`insert into prune_skips (cid, reason) values ($1, $2) on conflict (cid) do nothing`,
			cid, reason)
		if err != nil {
			return added, err
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

// loadPruneSkips reads the skip list into memory for a repair cycle. It is
// small by construction -- only CIDs a prune has explicitly judged -- so a map
// beats a query per CID across millions of repair checks.
func (ss *MediorumServer) loadPruneSkips(ctx context.Context) (map[string]struct{}, error) {
	rows, err := ss.pgPool.Query(ctx, `select cid from prune_skips`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	skips := map[string]struct{}{}
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		skips[cid] = struct{}{}
	}
	return skips, rows.Err()
}

// cleanupStaleTempsNearKey removes orphaned ".tmp" files from the directory a
// blob was just written into. Called after every successful local write.
//
// The directory is already hot from the write and holds only a handful of
// entries, so this is a cheap readdir. It self-targets: an interrupted write
// leaves both an orphan and a still-missing key, and repair's retry writes that
// key back into the same directory.
//
// The age filter is not about this write -- the temp for it has already been
// renamed away -- but about concurrent writers, which at RepairConcurrency > 1
// may legitimately hold a live ".tmp" in the same directory.
func (ss *MediorumServer) cleanupStaleTempsNearKey(bucket *blob.Bucket, key string) {
	root, isFile := persistence.FileDirFromDSN(ss.dsnForBucket(bucket))
	if !isFile {
		return
	}

	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(key)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-persistence.DefaultStaleTempFileAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err == nil {
			ss.logger.Debug("removed orphaned temp file", zap.String("path", path))
		}
	}
}

// dsnForBucket maps an open bucket back to the DSN it was opened from, so
// callers can resolve filesystem paths for file:// backends.
func (ss *MediorumServer) dsnForBucket(bucket *blob.Bucket) string {
	if ss.archiveBucket != nil && bucket == ss.archiveBucket {
		return ss.Config.ArchiveBlobStoreDSN
	}
	return ss.Config.BlobStoreDSN
}

// servePrune queues a prune. The trigger channel holds one slot so a request
// arriving while a prune is running is rejected rather than stacking expensive
// traversals and network probes.
//
// Defaults to a dry run: `commit` must be set explicitly, since two of the
// three tasks delete data.
func (ss *MediorumServer) servePrune(c echo.Context) error {
	req := pruneRequest{}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
	}
	if len(req.Tasks) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("specify tasks: any of %q, %q",
				pruneTaskTmp, pruneTaskUnpublished),
		})
	}
	for _, t := range req.Tasks {
		switch t {
		case pruneTaskTmp, pruneTaskUnpublished:
		default:
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "unknown task: " + string(t)})
		}
	}

	for _, tmpl := range req.Templates {
		if err := validateJobTemplate(tmpl); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("unknown template %q: expected %q, %q or %q",
					tmpl, JobTemplateAudio, JobTemplateImgSquare, JobTemplateImgBackdrop),
			})
		}
	}

	select {
	case ss.pruneTrigger <- req:
		return c.JSON(http.StatusAccepted, map[string]any{
			"status":    "prune queued; poll GET /internal/prune for progress",
			"tasks":     req.Tasks,
			"templates": req.pruneTemplates(),
			"dryRun":    !req.Commit,
		})
	default:
		return c.JSON(http.StatusConflict, map[string]string{
			"error": "a prune is already queued or running",
		})
	}
}

// PruneRunStatus is one row of prune history. UpdatedAt is the field that
// matters while a run is in flight: started_at can be hours old on a tree walk,
// so only the checkpoint age distinguishes progress from a wedged job.
type PruneRunStatus struct {
	ID         int64      `json:"id"`
	Task       string     `json:"task"`
	DryRun     bool       `json:"dryRun"`
	StartedAt  time.Time  `json:"startedAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Running    bool       `json:"running"`
	StaleFor   string     `json:"staleFor,omitempty"`
	Scanned    int64      `json:"scanned"`
	Matched    int64      `json:"matched"`
	Removed    int64      `json:"removed"`
	SkipsAdded int64      `json:"skipsAdded"`
	Error      string     `json:"error,omitempty"`
}

// servePruneStatus reports recent prune runs, newest first.
func (ss *MediorumServer) servePruneStatus(c echo.Context) error {
	limit := 20
	if v, err := strconv.Atoi(c.QueryParam("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}

	rows, err := ss.pgPool.Query(c.Request().Context(), `
		select id, task, dry_run, started_at, updated_at, finished_at,
		       scanned, matched, removed, skips_added, error
		  from prune_runs
		 order by started_at desc
		 limit $1`, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer rows.Close()

	out := []PruneRunStatus{}
	for rows.Next() {
		var r PruneRunStatus
		if err := rows.Scan(&r.ID, &r.Task, &r.DryRun, &r.StartedAt, &r.UpdatedAt,
			&r.FinishedAt, &r.Scanned, &r.Matched, &r.Removed, &r.SkipsAdded, &r.Error); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		r.Running = r.FinishedAt == nil
		if r.Running {
			// Surfaced explicitly so an operator does not have to subtract
			// timestamps to answer "is this still moving?".
			r.StaleFor = time.Since(r.UpdatedAt).Truncate(time.Second).String()
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, out)
}
