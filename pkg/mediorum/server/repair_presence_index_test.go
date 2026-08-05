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
	sawEscaped, err := walkFileBucketIntoIndex(ctx, dir, bucket, viaWalk)
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
	sawEscaped, err := walkFileBucketIntoIndex(ctx, dir, bucket, index)
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
	_, err := walkFileBucketIntoIndex(context.Background(),
		filepath.Join(t.TempDir(), "not-mounted"), nil, index)
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
