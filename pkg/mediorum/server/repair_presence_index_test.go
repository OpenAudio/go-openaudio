package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileBucketDir(t *testing.T) {
	tests := []struct {
		dsn     string
		wantDir string
		wantOK  bool
	}{
		{"file:///archive-blobs", "/archive-blobs", true},
		{"file:///archive-blobs?no_tmp_dir=true", "/archive-blobs", true},
		{"file:///tmp/mediorum/blobs?no_tmp_dir=true&create_dir=true", "/tmp/mediorum/blobs", true},
		{"file://./relative/blobs", "relative/blobs", true},
		{"s3://some-bucket?region=us-west-2", "", false},
		{"gs://some-bucket", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		dir, ok := fileBucketDir(tt.dsn)
		assert.Equal(t, tt.wantOK, ok, "ok for %q", tt.dsn)
		assert.Equal(t, tt.wantDir, dir, "dir for %q", tt.dsn)
	}
}

// The direct filesystem walk replaces gocloud's List for file:// buckets, so
// it has to produce exactly what List produced — identical keys, sizes and mod
// times. Any divergence makes repair believe it is missing a blob it already
// holds, and re-pull it.
func TestWalkFileBucketMatchesGocloudList(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	// Every key shape mediorum writes: sharded CIDv0, sharded CIDv1, and
	// legacy qm_cids keys that carry a '/' and a file extension.
	keys := []string{
		cidutil.ShardCID("QmY7Yh4UquoXHLPFo2XbhXkhBvFoPwmQUSa92pxnxjQuPU"),
		cidutil.ShardCID("baeaaaiqsecffzabbj7utfkkmywbhlls46twtaq3fbvpbozvugl4bqszfru7u2"),
		"QmZZzzXyvAKjGp1uN7oeBSCv1G958kZ6naoMSZPt68vtjf/original.jpg",
		"QmZZzzXyvAKjGp1uN7oeBSCv1G958kZ6naoMSZPt68vtjf/1000x1000.jpg",
	}
	for i, k := range keys {
		require.NoError(t, bucket.WriteAll(ctx, k, bytes.Repeat([]byte("x"), i+1), nil))
	}

	viaList := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	require.NoError(t, listIntoIndex(ctx, bucket, viaList))

	viaWalk := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	sawEscaped, err := walkFileBucketSerial(ctx, dir, bucket, viaWalk, nil)
	require.NoError(t, err)
	assert.False(t, sawEscaped, "plain ShardCID keys are never hex-escaped")

	assert.Len(t, viaList.entries, len(keys), "List should see every key and no .attrs sidecars")
	assert.Equal(t, viaList.entries, viaWalk.entries, "walk and List must agree exactly")

	// And the walk must be usable for the lookups repair actually performs.
	for _, k := range keys {
		_, ok := viaWalk.Lookup(k, bucket)
		assert.True(t, ok, "Lookup(%q) after walk", k)
	}
}

// A hex-escaped path can only be decoded by gocloud's internal escape package,
// so the walk must report it rather than index a wrong key.
func TestWalkFileBucketReportsEscapedPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	// fileblob would write this name for a key containing a control character.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "weird__0x1f_key"), []byte("x"), 0o600))

	index := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	sawEscaped, err := walkFileBucketSerial(ctx, dir, bucket, index, nil)
	require.NoError(t, err)
	assert.True(t, sawEscaped, "escaped path must trigger the bucket.List fallback")
}

// An unmounted or missing blob dir must be an error, not an empty index.
// filepath.WalkDir surfaces a bad root through the callback's err argument,
// which the walk deliberately ignores for individual entries — without an
// explicit root check, a dropped archive mount would yield zero entries and
// repair would conclude it holds nothing and re-pull the entire corpus.
func TestWalkFileBucketErrorsOnMissingDir(t *testing.T) {
	index := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	_, err := walkFileBucketSerial(context.Background(),
		filepath.Join(t.TempDir(), "not-mounted"), nil, index, nil)
	assert.Error(t, err)
	assert.Empty(t, index.entries)
}

// The uploads scan must reach every row, not just the first page. With
// "id > cursor" paired with ORDER BY DESC the cursor moved up into rows it had
// already read, so the scan terminated having visited only the top `limit`
// uploads — 1,000 of 2.4M in production — while burning ~limit²/2 duplicate
// visits getting there.
func TestNextUploadBatchVisitsEveryUpload(t *testing.T) {
	ss := testNetwork[0]

	const (
		batchSize = 50
		numRows   = 250
	)
	// Prefix sorts high under en_US collation so these rows land near the top
	// of the ordering — the most favorable case for the buggy scan. Even here
	// it only reaches the first page.
	const prefix = "zzcursorscan-"

	t.Cleanup(func() {
		ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{})
	})

	want := make(map[string]bool, numRows)
	for i := 0; i < numRows; i++ {
		id := fmt.Sprintf("%s%04d", prefix, i)
		require.NoError(t, ss.crud.DB.Create(&Upload{
			ID:        id,
			Template:  JobTemplateAudio,
			Status:    JobStatusDone,
			CreatedAt: time.Now(),
		}).Error)
		want[id] = true
	}

	seen := map[string]int{}
	cursor := ""
	for iter := 0; ; iter++ {
		require.Less(t, iter, numRows*2, "scan is not terminating; cursor is not advancing past read rows")

		uploads, err := ss.nextUploadBatch(cursor, batchSize)
		require.NoError(t, err)
		if len(uploads) == 0 {
			break
		}
		for _, u := range uploads {
			seen[u.ID]++
		}
		cursor = uploads[len(uploads)-1].ID
	}

	var missing []string
	for id := range want {
		if seen[id] == 0 {
			missing = append(missing, id)
		}
	}
	assert.Empty(t, missing, "%d of %d inserted uploads were never visited", len(missing), numRows)

	repeated := 0
	for _, n := range seen {
		if n > 1 {
			repeated++
		}
	}
	assert.Zero(t, repeated, "%d uploads visited more than once; the cursor is re-reading rows", repeated)
}

// The walk fans out across shard directories, so concurrency must not change
// what it produces. This pins the parallel walk to both the serial walk and to
// gocloud's List — the same invariant TestWalkFileBucketMatchesGocloudList
// asserts, but over a tree big enough to span several ReadDir batches and to
// actually run workers side by side. Run with -race to catch unsynchronized
// writes into the shared index.
func TestWalkFileBucketParallelMatchesSerialAndList(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	// Several shards with several blobs each, plus keys that cidutil.ShardCID
	// passes through unchanged and so land directly in the bucket root.
	var keys []string
	for shard := 0; shard < 12; shard++ {
		for blob := 0; blob < 3; blob++ {
			keys = append(keys, fmt.Sprintf("sh%02d/blob-%d", shard, blob))
		}
	}
	keys = append(keys, "unsharded-key-a", "unsharded-key-b")
	for i, k := range keys {
		require.NoError(t, bucket.WriteAll(ctx, k, bytes.Repeat([]byte("x"), i+1), nil))
	}

	viaList := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	require.NoError(t, listIntoIndex(ctx, bucket, viaList))
	require.Len(t, viaList.entries, len(keys), "List should see every key and no .attrs sidecars")

	// batch smaller than the number of shards forces multiple ReadDir rounds.
	serial := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	sawEscaped, err := walkFileBucketConcurrent(ctx, dir, bucket, serial, nil, 1, 1)
	require.NoError(t, err)
	assert.False(t, sawEscaped)

	parallel := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	sawEscaped, err = walkFileBucketConcurrent(ctx, dir, bucket, parallel, nil, 16, 4)
	require.NoError(t, err)
	assert.False(t, sawEscaped)

	assert.Equal(t, viaList.entries, serial.entries, "serial walk must match List")
	assert.Equal(t, viaList.entries, parallel.entries, "parallel walk must match List")
	assert.Equal(t, serial.entries, parallel.entries, "concurrency must not change the result")
}

// The escaped-path check runs per file, so it has to fire for a file nested in
// a shard directory and not just one sitting in the bucket root — the fan-out
// evaluates those on different goroutines.
func TestWalkFileBucketReportsEscapedPathInsideShard(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	require.NoError(t, bucket.WriteAll(ctx, "shard/ok-key", []byte("x"), nil))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "shard2"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "shard2", "weird__0x1f_key"), []byte("x"), 0o600))

	index := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	sawEscaped, err := walkFileBucketSerial(ctx, dir, bucket, index, nil)
	require.NoError(t, err)
	assert.True(t, sawEscaped, "escaped path inside a shard must trigger the fallback")
}

// A cancelled context must stop the walk rather than run it to completion.
func TestWalkFileBucketHonorsContextCancellation(t *testing.T) {
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	for shard := 0; shard < 8; shard++ {
		require.NoError(t, bucket.WriteAll(context.Background(),
			fmt.Sprintf("sh%02d/blob", shard), []byte("x"), nil))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	index := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	_, err = walkFileBucketConcurrent(ctx, dir, bucket, index, nil, 4, 2)
	assert.ErrorIs(t, err, context.Canceled)
}

// The concurrent walk is opt-in. At the default the selector must run the
// serial walk, so a node that sets nothing keeps the behaviour it had before
// the option existed — and whichever path runs, the index must be the same.
func TestWalkFileBucketSelectorDefaultsToSerial(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	bucket, err := persistence.Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	for shard := 0; shard < 6; shard++ {
		for blob := 0; blob < 3; blob++ {
			require.NoError(t, bucket.WriteAll(ctx,
				fmt.Sprintf("sh%02d/blob-%d", shard, blob), bytes.Repeat([]byte("x"), blob+1), nil))
		}
	}
	require.NoError(t, bucket.WriteAll(ctx, "unsharded", []byte("x"), nil))

	viaList := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
	require.NoError(t, listIntoIndex(ctx, bucket, viaList))

	// Config only — walkFileBucket reads nothing else off the server, and this
	// avoids mutating the shared test network.
	for _, concurrency := range []int{0, 1, 8} {
		ss := &MediorumServer{Config: MediorumConfig{PresenceWalkConcurrency: concurrency}}
		index := &repairPresenceIndex{entries: map[indexKey]presenceEntry{}}
		sawEscaped, err := ss.walkFileBucket(ctx, dir, bucket, index, nil)
		require.NoError(t, err, "concurrency=%d", concurrency)
		assert.False(t, sawEscaped)
		assert.Equal(t, viaList.entries, index.entries,
			"concurrency=%d must produce the same index", concurrency)
	}
}
