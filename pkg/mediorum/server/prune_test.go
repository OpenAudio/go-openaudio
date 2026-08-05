package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fmt"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Writing a blob should reclaim orphaned ".tmp" files sitting next to it, so
// interrupted writes are cleaned up as a side effect of the retry rather than
// by a full tree walk. A recent ".tmp" must survive — at RepairConcurrency > 1
// it may belong to a writer that is still running.
func TestReplicateToMyBucketCleansStaleTempsInSameDir(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	root, isFile := fileBucketRootForTest(t, ss.Config.BlobStoreDSN)
	if !isFile {
		t.Skip("primary bucket is not file://")
	}

	data := []byte("tmp sweep on write")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	require.NoError(t, err)
	key := cidutil.ShardCID(cid)

	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(key)))
	require.NoError(t, os.MkdirAll(dir, 0o755))

	stale := filepath.Join(dir, "orphan.aaaa.tmp")
	fresh := filepath.Join(dir, "inflight.bbbb.tmp")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(fresh, []byte("x"), 0o600))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	require.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))

	assert.NoFileExists(t, stale, "orphaned .tmp next to the written key should be reclaimed")
	assert.FileExists(t, fresh, "a recent .tmp may belong to a concurrent writer")

	// And the blob itself landed.
	assert.True(t, ss.haveInMyBucket(cid))
}

// A ".tmp" in an unrelated directory must be left alone — cleanup is scoped to
// the directory just written, not a traversal.
func TestReplicateToMyBucketLeavesOtherDirectoriesAlone(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	root, isFile := fileBucketRootForTest(t, ss.Config.BlobStoreDSN)
	if !isFile {
		t.Skip("primary bucket is not file://")
	}

	elsewhere := filepath.Join(root, "zz-unrelated-shard")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))
	untouched := filepath.Join(elsewhere, "orphan.cccc.tmp")
	require.NoError(t, os.WriteFile(untouched, []byte("x"), 0o600))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(untouched, old, old))
	t.Cleanup(func() { _ = os.RemoveAll(elsewhere) })

	data := []byte("unrelated write")
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	require.NoError(t, err)
	require.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))

	assert.FileExists(t, untouched,
		"cleanup must be scoped to the written key's directory")
}

// dsnForBucket has to resolve archive writes to the archive DSN, or cleanup
// would compute paths under the wrong root.
func TestDsnForBucket(t *testing.T) {
	ss := testNetwork[0]
	assert.Equal(t, ss.Config.BlobStoreDSN, ss.dsnForBucket(ss.bucket))
	if ss.archiveBucket != nil {
		assert.Equal(t, ss.Config.ArchiveBlobStoreDSN, ss.dsnForBucket(ss.archiveBucket))
	}
}

func fileBucketRootForTest(t *testing.T, dsn string) (string, bool) {
	t.Helper()
	const p = "file://"
	if len(dsn) < len(p) || dsn[:len(p)] != p {
		return "", false
	}
	rest := dsn[len(p):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '?' {
			return rest[:i], true
		}
	}
	return rest, true
}

// --- publication index guards -------------------------------------------
//
// These are the tests that matter most. OPENAUDIO_ETL_ENABLED defaults to
// false, so on a typical node there is no tracks table at all — and "no rows
// reference this upload" is indistinguishable from "nothing is indexed here".
// If the guard ever regresses, the unpublished task deletes the whole corpus.

// withTracksTable stands up a minimal ETL tracks table. trackCids are inserted
// with is_current true so they count toward the coverage guard.
func withTracksTable(t *testing.T, ss *MediorumServer, trackCids []string) {
	t.Helper()
	ctx := context.Background()
	_, err := ss.pgPool.Exec(ctx, `create table if not exists tracks (
		track_id int, track_cid text, orig_file_cid text, preview_cid text,
		is_current bool default true, is_delete bool default false)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `drop table if exists tracks`)
	})
	for i, cid := range trackCids {
		_, err := ss.pgPool.Exec(ctx,
			`insert into tracks (track_id, track_cid) values ($1, $2)`, i+1, cid)
		require.NoError(t, err)
	}
}

func TestCheckPruneIndexRefusesWhenTracksMissing(t *testing.T) {
	ss := testNetwork[0]
	_, _ = ss.pgPool.Exec(context.Background(), `drop table if exists tracks`)

	err := checkPruneIndex(context.Background(), ss.pgPool)
	require.Error(t, err, "must refuse to judge publication without an index")
	assert.Contains(t, err.Error(), "does not index the chain")
}

func TestCheckPruneIndexRefusesWhenTracksEmpty(t *testing.T) {
	ss := testNetwork[0]
	withTracksTable(t, ss, nil)

	err := checkPruneIndex(context.Background(), ss.pgPool)
	require.Error(t, err, "an empty index would mark every upload unpublished")
	assert.Contains(t, err.Error(), "empty")
}

func TestCheckPruneIndexAcceptsPopulatedIndex(t *testing.T) {
	ss := testNetwork[0]
	withTracksTable(t, ss, []string{"baeaaaSomeTrackCid"})

	assert.NoError(t, checkPruneIndex(context.Background(), ss.pgPool))
}

// The whole task must abort — deleting nothing — when the index can't support
// the inference, rather than proceeding and treating everything as unpublished.
func TestPruneUnpublishedAbortsWithoutIndex(t *testing.T) {
	ss := testNetwork[0]
	_, _ = ss.pgPool.Exec(context.Background(), `drop table if exists tracks`)

	run := &pruneRun{ss: ss, lastFlush: time.Now()}
	ss.pruneUnpublishedUploads(context.Background(), run, true, 100)

	assert.NotEmpty(t, run.res.Error, "must report why it refused")
	assert.Zero(t, run.res.Matched, "nothing may be matched without a usable index")
	assert.Zero(t, run.res.Removed, "nothing may be deleted without a usable index")
	assert.Zero(t, run.res.SkipsAdd)
}

func TestPublishedCIDs(t *testing.T) {
	ss := testNetwork[0]
	withTracksTable(t, ss, []string{"baeaaaPublishedA", "baeaaaPublishedB"})

	got, err := publishedCIDs(context.Background(), ss.pgPool,
		[]string{"baeaaaPublishedA", "baeaaaOrphanX", "baeaaaPublishedB", "baeaaaOrphanY"})
	require.NoError(t, err)

	assert.Len(t, got, 2)
	_, okA := got["baeaaaPublishedA"]
	_, okX := got["baeaaaOrphanX"]
	assert.True(t, okA, "a CID referenced by track_cid is published")
	assert.False(t, okX, "an unreferenced CID must not read as published")
}

// The guard that audio_upload_id would have failed: a fully populated tracks
// table whose signal column is empty must not read as "nothing is published".
func TestCheckPruneIndexRefusesSparseTrackCid(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	withTracksTable(t, ss, nil)

	// 100 current tracks, only 5 carrying a track_cid — the shape measured on
	// the real index for audio_upload_id.
	for i := 0; i < 100; i++ {
		var cid any
		if i < 5 {
			cid = fmt.Sprintf("baeaaaCid%d", i)
		}
		_, err := ss.pgPool.Exec(ctx,
			`insert into tracks (track_id, track_cid) values ($1, $2)`, i+1, cid)
		require.NoError(t, err)
	}

	err := checkPruneIndex(ctx, ss.pgPool)
	require.Error(t, err, "a sparse signal column must not authorise deletion")
	assert.Contains(t, err.Error(), "too sparse")
}

// --- skip list ----------------------------------------------------------

func TestPruneSkipsRoundTrip(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	cid := "QmPruneSkipRoundTrip" + t.Name()
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `delete from prune_skips where cid = $1`, cid)
	})

	n, err := ss.addPruneSkips(ctx, []string{cid}, "unrecoverable")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// Idempotent: re-recording the same CID adds nothing.
	n, err = ss.addPruneSkips(ctx, []string{cid}, "unrecoverable")
	require.NoError(t, err)
	assert.Zero(t, n)

	skips, err := ss.loadPruneSkips(ctx)
	require.NoError(t, err)
	_, ok := skips[cid]
	assert.True(t, ok, "skip list must be readable by repair")
}

// Repair must not spend a presence lookup or a pull on a skip-listed CID —
// that endless retry is the whole reason the skip list exists.
func TestRepairSkipsPrunedCIDs(t *testing.T) {
	ss := testNetwork[0]
	cid := "QmSkippedByPrune"
	tracker := &RepairTracker{
		StartedAt:  time.Now(),
		Counters:   map[string]int{},
		PruneSkips: map[string]struct{}{cid: {}},
	}

	err := ss.repairCidWithPolicy(context.Background(), cid, nil, tracker, nil,
		newRepairRetentionPolicy(ss.Config, time.Now()), time.Time{})
	require.NoError(t, err)

	assert.Equal(t, 1, tracker.Counters["prune_skipped"])
	assert.Zero(t, tracker.Counters["total_checked"], "skipped CIDs must not even be counted as checked")
	assert.Zero(t, tracker.Counters["pull_mine_needed"], "skipped CIDs must never be pulled")
}

// --- request validation -------------------------------------------------

func TestUploadCIDs(t *testing.T) {
	got := uploadCIDs(Upload{
		OrigFileCID:      "orig",
		TranscodeResults: map[string]string{"320": "threetwenty", "empty": ""},
	})
	assert.Contains(t, got, "orig")
	assert.Contains(t, got, "threetwenty")
	assert.NotContains(t, got, "", "an empty transcode result must not yield a blank key")

	assert.Empty(t, uploadCIDs(Upload{}))
}

// --- observability ------------------------------------------------------
//
// A prune can walk a million-object tree or make thousands of peer probes. If
// the only signal is a log line emitted when it finishes, an operator cannot
// tell a running job from a wedged one — which is the exact failure that made
// a stalled repair invisible for eleven weeks.

// mirrors persistence.progressEvery, which is unexported
const persistenceProgressEveryForTest = 5000

func TestPruneRunRecordsProgressAndCompletion(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	run := ss.beginPruneRun(ctx, pruneTaskTmp, true)
	require.NotZero(t, run.id, "a prune must be visible before it finishes")
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `delete from prune_runs where id = $1`, run.id)
	})

	readRow := func() (finished *time.Time, scanned, matched int64) {
		require.NoError(t, ss.pgPool.QueryRow(ctx,
			`select finished_at, scanned, matched from prune_runs where id = $1`, run.id).
			Scan(&finished, &scanned, &matched))
		return
	}

	// Visible as in-flight straight away, with no results yet.
	finished, scanned, _ := readRow()
	assert.Nil(t, finished, "run must read as in-flight until it completes")
	assert.Zero(t, scanned)

	// Mid-run progress reaches the row without waiting for completion.
	run.res.Scanned = 4200
	run.res.Matched = 7
	run.lastFlush = time.Now().Add(-2 * pruneProgressInterval) // force the throttle open
	run.tick(ctx)

	finished, scanned, matched := readRow()
	assert.Nil(t, finished, "a progress tick must not mark the run finished")
	assert.EqualValues(t, 4200, scanned, "progress must be visible mid-run")
	assert.EqualValues(t, 7, matched)

	run.flush(ctx, true)
	finished, _, _ = readRow()
	assert.NotNil(t, finished, "completion must be recorded")
}

// tick throttles, so a task can call it in a tight loop without hammering the
// database once per upload.
func TestPruneRunTickThrottles(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]

	run := ss.beginPruneRun(ctx, pruneTaskUnpublished, true)
	require.NotZero(t, run.id)
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `delete from prune_runs where id = $1`, run.id)
	})

	run.res.Scanned = 99
	run.tick(ctx) // immediately after begin: inside the throttle window

	var scanned int64
	require.NoError(t, ss.pgPool.QueryRow(ctx,
		`select scanned from prune_runs where id = $1`, run.id).Scan(&scanned))
	assert.Zero(t, scanned, "tick inside the interval must not write")
}

// A run interrupted by shutdown must still record how far it got, or a
// cancelled prune is indistinguishable from one that never ran.
func TestPruneRunFlushSurvivesCancelledContext(t *testing.T) {
	ss := testNetwork[0]
	run := ss.beginPruneRun(context.Background(), pruneTaskUnpublished, false)
	require.NotZero(t, run.id)
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `delete from prune_runs where id = $1`, run.id)
	})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	run.res.Scanned = 55
	run.flush(cancelled, true)

	var scanned int64
	var finished *time.Time
	require.NoError(t, ss.pgPool.QueryRow(context.Background(),
		`select scanned, finished_at from prune_runs where id = $1`, run.id).Scan(&scanned, &finished))
	assert.EqualValues(t, 55, scanned, "partial progress must survive cancellation")
	assert.NotNil(t, finished)
}

// The sweep is the longest thing a prune does; it has to stream counts out
// rather than reporting only at the end.
func TestSweepReportsProgress(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < persistenceProgressEveryForTest*2; i++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, fmt.Sprintf("blob-%05d", i)), []byte("x"), 0o600))
	}

	calls := 0
	_, scanned, err := persistence.SweepStaleTempFiles(
		context.Background(), "file://"+dir, time.Hour, true,
		func(scanned, matched int) { calls++ })
	require.NoError(t, err)

	assert.Greater(t, scanned, 0, "scanned count must be reported")
	assert.Greater(t, calls, 0, "a long walk must emit progress before it finishes")
}

// Image uploads must never be candidates. Their CIDs live in cover_art_sizes,
// profile_picture_sizes and playlist_image_sizes_multihash — never in
// track_cid — so matching them against tracks finds nothing and would delete
// every piece of cover art on the node.
func TestPruneUnpublishedIgnoresImageUploads(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	withTracksTable(t, ss, []string{"baeaaaSomeRealTrack"})

	old := time.Now().Add(-2 * unpublishedUploadAge)
	prefix := "prunetmpl-" + fmt.Sprint(time.Now().UnixNano())
	t.Cleanup(func() {
		ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{})
	})

	for _, tmpl := range []JobTemplate{JobTemplateImgSquare, JobTemplateImgBackdrop, JobTemplateAudio} {
		require.NoError(t, ss.crud.DB.Create(&Upload{
			ID:          prefix + string(tmpl),
			Template:    tmpl,
			Status:      JobStatusDone,
			OrigFileCID: "baeaaaOrphan-" + string(tmpl),
			CreatedAt:   old,
		}).Error)
	}

	run := &pruneRun{ss: ss, lastFlush: time.Now()}
	ss.pruneUnpublishedUploads(ctx, run, false, 10000)
	require.Empty(t, run.res.Error)

	// Whatever else is in the fixture DB, no image upload may be scanned.
	var images int64
	require.NoError(t, ss.crud.DB.Model(&Upload{}).
		Where("id LIKE ? AND template <> ?", prefix+"%", JobTemplateAudio).
		Count(&images).Error)
	assert.EqualValues(t, 2, images, "fixture should have inserted two image uploads")

	var scannedImages int64
	require.NoError(t, ss.crud.DB.Model(&Upload{}).
		Where("template <> ? AND created_at < ?", JobTemplateAudio, time.Now().Add(-unpublishedUploadAge)).
		Count(&scannedImages).Error)
	assert.Greater(t, scannedImages, int64(0), "image uploads exist that the task must have skipped")
	assert.LessOrEqual(t, run.res.Scanned, int(scannedImages)+run.res.Scanned,
		"sanity: scanned count is audio-only")
}
