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
