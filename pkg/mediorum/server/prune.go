package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
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

	// pruneTaskUnrecoverable skip-lists CIDs that no reachable peer can serve,
	// so repair stops retrying them every cycle forever.
	pruneTaskUnrecoverable pruneTask = "unrecoverable"
)

const (
	// unpublishedUploadAge is how long an upload must have gone unreferenced by
	// any track before its blobs are considered abandoned. Publishing normally
	// follows an upload within minutes; a month is a wide margin for a client
	// that uploaded, stalled, and came back.
	unpublishedUploadAge = 30 * 24 * time.Hour

	// prunePageSize bounds a single scan so a prune never holds a huge result
	// set or runs unbounded against a 2M+ row table.
	prunePageSize = 1000

	// minReachablePeersForAbsence is how many peers must answer before absence
	// is treated as evidence. Concluding "no one has this" from a node that
	// cannot reach the network would skip-list the entire corpus.
	minReachablePeersForAbsence = 5
)

// pruneRequest selects which tasks to run. DryRun is the default: the caller
// must opt in to mutation, because two of the three tasks delete data.
type pruneRequest struct {
	Tasks  []pruneTask `json:"tasks"`
	Commit bool        `json:"commit"`
	Limit  int         `json:"limit"`
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

		start := time.Now()
		var res pruneResult
		switch task {
		case pruneTaskTmp:
			res = ss.pruneTmpFiles(ctx, req.Commit)
		case pruneTaskUnpublished:
			res = ss.pruneUnpublishedUploads(ctx, req.Commit, limit)
		case pruneTaskUnrecoverable:
			res = ss.pruneUnrecoverableUploads(ctx, req.Commit, limit)
		default:
			res = pruneResult{Error: "unknown prune task"}
		}
		res.Task = task
		res.DryRun = !req.Commit
		res.Duration = time.Since(start)
		res.Took = res.Duration.String()

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
func (ss *MediorumServer) pruneTmpFiles(ctx context.Context, commit bool) pruneResult {
	var res pruneResult

	dsns := []string{ss.Config.BlobStoreDSN}
	if ss.Config.ArchiveBlobStoreDSN != "" {
		dsns = append(dsns, ss.Config.ArchiveBlobStoreDSN)
	}

	for _, dsn := range dsns {
		if ctx.Err() != nil {
			return res
		}
		if _, isFile := persistence.FileDirFromDSN(dsn); !isFile {
			// Cloud backends have no local tree. Their equivalent artifact is an
			// incomplete multipart upload, invisible to the keyspace and handled
			// by a bucket lifecycle rule rather than by us.
			continue
		}
		n, err := persistence.SweepStaleTempFiles(ctx, dsn, persistence.DefaultStaleTempFileAge, !commit)
		res.Matched += n
		if commit {
			res.Removed += n
		}
		if err != nil {
			res.Error = err.Error()
			return res
		}
	}
	return res
}

// pruneUnpublishedUploads reclaims blobs for uploads no track ever referenced.
//
// "Unpublished" means no row in the ETL's `tracks` table carries this upload's
// ID in audio_upload_id -- i.e. no CreateTrack transaction ever pointed at it.
// The bytes were uploaded and then abandoned, so nothing will ever serve them.
//
// Two floors keep this conservative: the upload must be older than
// unpublishedUploadAge, and checkPruneIndex must certify the index is present
// and fresh. Without the second, a node that simply doesn't index the chain
// would classify the entire corpus as unpublished.
//
// The upload row itself is left alone (see the note at the top of this file);
// only local blobs go, and the CIDs are skip-listed so repair does not pull
// them straight back from a peer that hasn't pruned.
func (ss *MediorumServer) pruneUnpublishedUploads(ctx context.Context, commit bool, limit int) pruneResult {
	var res pruneResult

	pool, release, err := ss.openPruneIndex(ctx)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer release()
	if err := checkPruneIndex(ctx, pool); err != nil {
		res.Error = err.Error()
		return res
	}

	cutoff := time.Now().Add(-unpublishedUploadAge)
	var uploads []Upload
	if err := ss.crud.DB.
		Where("created_at < ?", cutoff).
		Order("created_at").
		Limit(limit).
		Find(&uploads).Error; err != nil {
		res.Error = err.Error()
		return res
	}
	res.Scanned = len(uploads)
	if len(uploads) == 0 {
		return res
	}

	ids := make([]string, 0, len(uploads))
	for _, u := range uploads {
		ids = append(ids, u.ID)
	}
	published, err := publishedUploadIDs(ctx, pool, ids)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	for _, u := range uploads {
		if ctx.Err() != nil {
			return res
		}
		if _, ok := published[u.ID]; ok {
			continue
		}
		cids := uploadCIDs(u)
		if len(cids) == 0 {
			continue
		}
		res.Matched++

		if !commit {
			continue
		}
		for _, cid := range cids {
			if ss.haveInMyBucket(cid) {
				if err := ss.dropFromMyBucket(cid); err == nil {
					res.Removed++
				}
			}
		}
		if n, err := ss.addPruneSkips(ctx, cids, "unpublished"); err == nil {
			res.SkipsAdd += n
		}
	}
	return res
}

// pruneUnrecoverableUploads skip-lists CIDs that no reachable peer can serve.
//
// Repair otherwise retries these every cycle forever: the pull fails against
// every host, the CID stays missing, and the next cycle tries again. Recording
// them locally converts an unbounded retry loop into a single decision.
//
// Absence is only concluded from a network that actually answered. If too few
// peers are reachable the task aborts rather than skip-listing content that is
// merely unreachable right now.
func (ss *MediorumServer) pruneUnrecoverableUploads(ctx context.Context, commit bool, limit int) pruneResult {
	var res pruneResult

	healthy := ss.findHealthyPeers(time.Hour)
	if len(healthy) < minReachablePeersForAbsence {
		res.Error = fmt.Sprintf("only %d reachable peers (need %d): cannot distinguish missing content from an unreachable network",
			len(healthy), minReachablePeersForAbsence)
		return res
	}

	var uploads []Upload
	err := ss.crud.DB.
		Where("status = ?", JobStatusDone).
		Order("created_at").
		Limit(limit).
		Find(&uploads).Error
	if err != nil {
		res.Error = err.Error()
		return res
	}

	for _, u := range uploads {
		if ctx.Err() != nil {
			return res
		}
		res.Scanned++

		for _, cid := range uploadCIDs(u) {
			if ctx.Err() != nil {
				return res
			}
			// Anything we hold is by definition recoverable.
			if ss.haveInMyBucket(cid) {
				continue
			}
			if ss.anyPeerHasBlob(cid) {
				continue
			}
			res.Matched++
			if !commit {
				continue
			}
			if n, err := ss.addPruneSkips(ctx, []string{cid}, "unrecoverable"); err == nil {
				res.SkipsAdd += n
			}
		}
	}
	return res
}

// anyPeerHasBlob probes the hosts most likely to hold a CID. Bounded on
// purpose: the rendezvous ranking is every node on the network, and a full
// sweep per CID would make this task quadratic in the corpus.
func (ss *MediorumServer) anyPeerHasBlob(cid string) bool {
	key := cidutil.ShardCID(cid)
	hosts, _ := ss.rendezvousAllHosts(cid)

	probes := ss.Config.ReplicationFactor * 2
	if probes < 4 {
		probes = 4
	}
	if len(hosts) > probes {
		hosts = hosts[:probes]
	}

	for _, host := range hosts {
		if host == ss.Config.Self.Host {
			continue
		}
		if ss.hostHasBlob(host, key) {
			return true
		}
	}
	return false
}

// Publication is determined from the ETL's `tracks` table, which is built from
// CreateTrack entity-manager transactions. tracks.audio_upload_id is a direct
// join to an upload's ID -- far better than matching CIDs, and unlike
// sound_recordings it is actually populated.
//
// The catch: OPENAUDIO_ETL_ENABLED defaults to false, so most nodes have no
// tracks table at all. On such a node every upload would look unpublished and
// this task would delete the entire corpus. Hence openPruneIndex, which refuses
// to proceed unless a populated, reasonably fresh index is available -- locally
// or via OPENAUDIO_PRUNE_INDEX_DB_URL pointing at a node that does index.

// pruneIndexStaleAfter is how far behind the ETL may be before its answers stop
// counting as evidence. A stale index still lists old tracks correctly, but it
// would report recently published uploads as unpublished.
const pruneIndexStaleAfter = 24 * time.Hour

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

// checkPruneIndex refuses to authorise deletion from an index that cannot
// support the inference. Each failure mode here would otherwise read as
// "nothing is published".
func checkPruneIndex(ctx context.Context, pool *pgxpool.Pool) error {
	var tracks int64
	if err := pool.QueryRow(ctx, `select count(*) from tracks`).Scan(&tracks); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return fmt.Errorf("no tracks table: this node does not index the chain. " +
				"Enable OPENAUDIO_ETL_ENABLED, or set OPENAUDIO_PRUNE_INDEX_DB_URL to a node that does")
		}
		return err
	}
	if tracks == 0 {
		return fmt.Errorf("tracks table is empty: refusing to treat every upload as unpublished")
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

// publishedUploadIDs returns the subset of ids that a track references. Batched
// deliberately -- one query per page rather than per upload.
func publishedUploadIDs(ctx context.Context, pool *pgxpool.Pool, ids []string) (map[string]struct{}, error) {
	published := map[string]struct{}{}
	if len(ids) == 0 {
		return published, nil
	}
	rows, err := pool.Query(ctx,
		`select distinct audio_upload_id from tracks where audio_upload_id = any($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id *string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != nil && *id != "" {
			published[*id] = struct{}{}
		}
	}
	return published, rows.Err()
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
			"error": fmt.Sprintf("specify tasks: any of %q, %q, %q",
				pruneTaskTmp, pruneTaskUnpublished, pruneTaskUnrecoverable),
		})
	}
	for _, t := range req.Tasks {
		switch t {
		case pruneTaskTmp, pruneTaskUnpublished, pruneTaskUnrecoverable:
		default:
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "unknown task: " + string(t)})
		}
	}

	select {
	case ss.pruneTrigger <- req:
		return c.JSON(http.StatusAccepted, map[string]any{
			"status": "prune queued; results are logged with task=prune",
			"tasks":  req.Tasks,
			"dryRun": !req.Commit,
		})
	default:
		return c.JSON(http.StatusConflict, map[string]string{
			"error": "a prune is already queued or running",
		})
	}
}
