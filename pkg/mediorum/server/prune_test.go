package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
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

func withTracksTable(t *testing.T, ss *MediorumServer, rows [][2]string) {
	t.Helper()
	ctx := context.Background()
	_, err := ss.pgPool.Exec(ctx, `create table if not exists tracks (
		track_id int, audio_upload_id text, is_delete bool default false)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ss.pgPool.Exec(context.Background(), `drop table if exists tracks`)
	})
	for i, r := range rows {
		_, err := ss.pgPool.Exec(ctx,
			`insert into tracks (track_id, audio_upload_id) values ($1, $2)`, i+1, r[1])
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
	withTracksTable(t, ss, [][2]string{{"1", "some-upload-id"}})

	assert.NoError(t, checkPruneIndex(context.Background(), ss.pgPool))
}

// The whole task must abort — deleting nothing — when the index can't support
// the inference, rather than proceeding and treating everything as unpublished.
func TestPruneUnpublishedAbortsWithoutIndex(t *testing.T) {
	ss := testNetwork[0]
	_, _ = ss.pgPool.Exec(context.Background(), `drop table if exists tracks`)

	res := ss.pruneUnpublishedUploads(context.Background(), true, 100)
	assert.NotEmpty(t, res.Error, "must report why it refused")
	assert.Zero(t, res.Matched, "nothing may be matched without a usable index")
	assert.Zero(t, res.Removed, "nothing may be deleted without a usable index")
	assert.Zero(t, res.SkipsAdd)
}

func TestPublishedUploadIDs(t *testing.T) {
	ss := testNetwork[0]
	withTracksTable(t, ss, [][2]string{{"1", "published-a"}, {"2", "published-b"}})

	got, err := publishedUploadIDs(context.Background(), ss.pgPool,
		[]string{"published-a", "orphan-x", "published-b", "orphan-y"})
	require.NoError(t, err)

	assert.Len(t, got, 2)
	_, okA := got["published-a"]
	_, okB := got["published-b"]
	_, okX := got["orphan-x"]
	assert.True(t, okA)
	assert.True(t, okB)
	assert.False(t, okX, "an unreferenced upload must not read as published")
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
