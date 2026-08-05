package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileDirFromDSN(t *testing.T) {
	tests := []struct {
		dsn     string
		wantDir string
		wantOK  bool
	}{
		{"file:///archive-blobs", "/archive-blobs", true},
		{"file:///archive-blobs?no_tmp_dir=true", "/archive-blobs", true},
		{"file:///tmp/mediorum/blobs?no_tmp_dir=true&create_dir=true", "/tmp/mediorum/blobs", true},
		{"s3://some-bucket?region=us-west-2", "", false},
		{"gs://some-bucket", "", false},
		{"azblob://container", "", false},
		{"file://", "", false},
		{"not-a-dsn", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		dir, ok := FileDirFromDSN(tt.dsn)
		assert.Equal(t, tt.wantOK, ok, "ok for %q", tt.dsn)
		assert.Equal(t, tt.wantDir, dir, "dir for %q", tt.dsn)
	}
}

// A ".tmp" file that is still being written is indistinguishable from an
// orphan by name alone. Since the sweep now runs concurrently with live
// traffic rather than before startup, it must leave recent files alone —
// deleting one would corrupt an in-flight write.
func TestSweepStaleTempFilesLeavesRecentFiles(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, "shard", "blob.abc123.tmp")
	fresh := filepath.Join(dir, "shard", "blob.def456.tmp")
	blob := filepath.Join(dir, "shard", "QmSomeRealBlob")

	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	for _, p := range []string{stale, fresh, blob} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	// Backdate the stale one well past the cutoff.
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	removed, err := SweepStaleTempFiles(context.Background(), "file://"+dir, time.Hour, false)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	assert.NoFileExists(t, stale, "stale .tmp should be swept")
	assert.FileExists(t, fresh, "recent .tmp may be an in-flight write")
	assert.FileExists(t, blob, "real blobs must never be touched")
}

// Cloud backends have no local tree; the sweep must be a silent no-op rather
// than an error, since it runs unconditionally for every configured bucket.
func TestSweepStaleTempFilesSkipsNonFileBackends(t *testing.T) {
	removed, err := SweepStaleTempFiles(context.Background(), "s3://some-bucket?region=us-west-2", time.Hour, false)
	assert.NoError(t, err)
	assert.Zero(t, removed)
}

// An unmounted or missing bucket dir must be an error, not a clean sweep.
// WalkDir reports a bad root through the callback's err argument, which the
// sweep deliberately ignores per-entry — without an explicit root check this
// would silently report success on a dropped mount.
func TestSweepStaleTempFilesErrorsOnMissingDir(t *testing.T) {
	_, err := SweepStaleTempFiles(context.Background(),
		"file://"+filepath.Join(t.TempDir(), "not-mounted"), time.Hour, false)
	assert.Error(t, err)
}

// One unreadable entry must not abandon the rest of the sweep. The previous
// implementation returned the error straight out of the WalkDir callback,
// which aborted the whole pass.
func TestSweepStaleTempFilesContinuesPastBadEntries(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)

	for _, name := range []string{"a/one.tmp", "b/two.tmp", "c/three.tmp"} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(p, old, old))
	}
	// Make one subtree unreadable so WalkDir reports an error for it.
	unreadable := filepath.Join(dir, "b")
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	removed, err := SweepStaleTempFiles(context.Background(), "file://"+dir, time.Hour, false)
	require.NoError(t, err)
	// a and c are swept even though b could not be read. Running as root
	// defeats the chmod, in which case all three go.
	assert.GreaterOrEqual(t, removed, 2)
}

// Open must no longer block on the sweep. It used to walk the whole tree
// before returning, which kept mediorum from initializing for hours on a
// large archive.
func TestOpenDoesNotSweep(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "blob.abc123.tmp")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o600))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	bucket, err := Open("file://" + dir + "?no_tmp_dir=true")
	require.NoError(t, err)
	defer bucket.Close()

	assert.FileExists(t, stale, "Open must not do the sweep inline")
}
