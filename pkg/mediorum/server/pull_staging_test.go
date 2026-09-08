package server

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// postInternalBlobPull drives the handler directly, with the authenticated peer
// host the auth middleware would have set.
func postInternalBlobPull(t *testing.T, ss *MediorumServer, sourceHost string, request internalBlobPullRequest) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(request)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/internal/blobs/pull", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	c := echo.New().NewContext(req, rec)
	c.Set(authenticatedPeerHostKey, sourceHost)
	require.NoError(t, ss.serveInternalBlobPull(c))
	return rec
}

func TestPullStagingDirDefaultsToOSTempDir(t *testing.T) {
	ss := blobFetchTestServer(t)
	require.Equal(t, os.TempDir(), ss.pullStagingDir())

	ss.Config.PullStagingDir = "/mnt/blobs/staging"
	require.Equal(t, "/mnt/blobs/staging", ss.pullStagingDir())
}

// The blob is staged before ValidateCID and before anything reaches the bucket,
// so the directory it lands in is the one that has to have room -- and on a
// container deployment that is not the blob volume.
func TestPullStagesIntoTheConfiguredDirectory(t *testing.T) {
	source := testNetwork[0]
	target := testNetwork[1]

	staging := t.TempDir()
	original := target.Config.PullStagingDir
	target.Config.PullStagingDir = staging
	t.Cleanup(func() { target.Config.PullStagingDir = original })

	content := "staged into the configured directory"
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	putInternalBlobTestObject(t, context.Background(), source.bucket, cid, content)
	t.Cleanup(func() {
		_ = source.dropFromMyBucket(cid)
		_ = target.dropFromMyBucket(cid)
	})

	require.NoError(t, target.pullFileFromHostValidated(
		context.Background(), source.Config.Self.Host, cid, nil, "", false))

	require.True(t, target.haveInMyBucket(cid), "the pull did not store the blob")

	// The staging file is cleaned up on the way out; what matters is that it was
	// created here rather than in the OS temp dir. An empty directory that the
	// pull succeeded through is that evidence -- a stray file would mean the
	// cleanup regressed, and a file in os.TempDir() would mean the config was
	// ignored.
	entries, err := os.ReadDir(staging)
	require.NoError(t, err)
	require.Empty(t, entries, "staging file was left behind")

	strays, err := filepath.Glob(filepath.Join(os.TempDir(), "mediorum-pull-*"))
	require.NoError(t, err)
	require.Empty(t, strays, "pull staged in the OS temp dir despite PullStagingDir being set")
}

// The point of the check is that the sender hears about it. A pull admitted on
// blob-store headroom alone would return 202 and then fail on a filesystem the
// sender never had visibility into.
func TestServeInternalBlobPullRefusesWhenStagingIsFull(t *testing.T) {
	ss := testNetwork[0]

	// The staging check only runs in prod. Point the blob-store DSNs at a
	// non-file scheme so diskHasSpaceForCID passes unconditionally and the
	// refusal under test is unambiguously the staging one.
	originalEnv, originalDSN, originalArchiveDSN := ss.Config.Env, ss.Config.BlobStoreDSN, ss.Config.ArchiveBlobStoreDSN
	ss.Config.Env = "prod"
	ss.Config.BlobStoreDSN = "s3://not-a-file-dsn"
	ss.Config.ArchiveBlobStoreDSN = "s3://not-a-file-dsn"
	originalReserve := localDiskReserveBytes
	localDiskReserveBytes = math.MaxUint64 - 1
	t.Cleanup(func() {
		ss.Config.Env = originalEnv
		ss.Config.BlobStoreDSN = originalDSN
		ss.Config.ArchiveBlobStoreDSN = originalArchiveDSN
		localDiskReserveBytes = originalReserve
	})

	rec := postInternalBlobPull(t, ss, testNetwork[1].Config.Self.Host, internalBlobPullRequest{
		CID:   "QmStagingFullTestCid",
		Async: true,
	})

	require.Equal(t, http.StatusInsufficientStorage, rec.Code,
		"no room must not look like busy; they earn opposite retries")
	require.Contains(t, rec.Body.String(), "stage")
	require.Len(t, ss.asyncPullQueue, 0, "queued a transfer it has nowhere to stage")
}

func TestAsyncPullTunablesFallBackToDefaults(t *testing.T) {
	ss := blobFetchTestServer(t)

	// Unset (zero) means default, so mediorum.go does not have to restate it.
	require.Equal(t, DefaultAsyncPullWorkers, ss.asyncPullWorkers())
	require.Equal(t, DefaultAsyncPullTimeout, ss.asyncPullTimeout())

	ss.Config.AsyncPullWorkers = 12
	ss.Config.AsyncPullTimeout = 90 * time.Minute
	require.Equal(t, 12, ss.asyncPullWorkers())
	require.Equal(t, 90*time.Minute, ss.asyncPullTimeout())

	// Clamped: each worker demands staging headroom, so an unbounded value
	// would refuse every pull -- and overflow pullStagingMinFree.
	ss.Config.AsyncPullWorkers = 1_000_000
	require.Equal(t, maxAsyncPullWorkers, ss.asyncPullWorkers())

	ss.Config.AsyncPullWorkers = -1
	require.Equal(t, DefaultAsyncPullWorkers, ss.asyncPullWorkers())
}

// The point of unifying on localDirHasSpaceFor is that the pull path stops
// guessing: the sender already read the blob's attributes to choose a source
// bucket, so the receiver can ask whether this blob fits rather than whether
// some threshold is clear.
func TestServeInternalBlobPullChecksTheDeclaredSize(t *testing.T) {
	ss := testNetwork[0]

	originalEnv, originalDSN, originalArchiveDSN := ss.Config.Env, ss.Config.BlobStoreDSN, ss.Config.ArchiveBlobStoreDSN
	ss.Config.Env = "prod"
	ss.Config.BlobStoreDSN = "s3://not-a-file-dsn"
	ss.Config.ArchiveBlobStoreDSN = "s3://not-a-file-dsn"
	ss.Config.PullStagingDir = t.TempDir()
	t.Cleanup(func() {
		ss.Config.Env = originalEnv
		ss.Config.BlobStoreDSN = originalDSN
		ss.Config.ArchiveBlobStoreDSN = originalArchiveDSN
		ss.Config.PullStagingDir = ""
	})

	// A blob larger than any disk is refused on its size alone -- the reserve
	// is untouched, so nothing but the declared number can be doing the work.
	rec := postInternalBlobPull(t, ss, testNetwork[1].Config.Self.Host, internalBlobPullRequest{
		CID:   "QmDeclaredTooLargeCid",
		Size:  math.MaxInt64,
		Async: true,
	})
	require.Equal(t, http.StatusInsufficientStorage, rec.Code,
		"a blob bigger than the disk was admitted; the declared size is being ignored")

	// The same request without a size -- an older sender -- gets through,
	// which is what makes the check above attributable to Size and not to the
	// disk being full.
	rec = postInternalBlobPull(t, ss, testNetwork[1].Config.Self.Host, internalBlobPullRequest{
		CID:   "QmDeclaredNoSizeCid",
		Async: true,
	})
	require.Equal(t, http.StatusAccepted, rec.Code)
	ss.releaseAsyncPull("QmDeclaredNoSizeCid")
}
