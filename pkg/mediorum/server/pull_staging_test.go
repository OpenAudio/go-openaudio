package server

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestStagingHasSpace(t *testing.T) {
	t.Run("non-prod is never gated", func(t *testing.T) {
		ss := blobFetchTestServer(t)
		ss.Config.Env = "dev"
		ss.Config.PullStagingDir = "/definitely/not/a/real/path"
		require.True(t, ss.stagingHasSpace())
	})

	t.Run("an unmeasurable directory is allowed, not refused", func(t *testing.T) {
		ss := blobFetchTestServer(t)
		ss.Config.Env = "prod"
		ss.Config.PullStagingDir = "/definitely/not/a/real/path"
		require.True(t, ss.stagingHasSpace(), "a failed statfs must not stop replication outright")
	})

	t.Run("a real directory with room is allowed", func(t *testing.T) {
		ss := blobFetchTestServer(t)
		ss.Config.Env = "prod"
		ss.Config.PullStagingDir = t.TempDir()
		require.True(t, ss.stagingHasSpace())
	})

	t.Run("below threshold is refused", func(t *testing.T) {
		ss := blobFetchTestServer(t)
		ss.Config.Env = "prod"
		ss.Config.PullStagingDir = t.TempDir()

		original := stagingBytesPerWorker
		stagingBytesPerWorker = 1 << 60
		t.Cleanup(func() { stagingBytesPerWorker = original })

		require.False(t, ss.stagingHasSpace(),
			"no filesystem has an exabyte free, so this must refuse")
	})
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
	originalPerWorker := stagingBytesPerWorker
	stagingBytesPerWorker = 1 << 60
	t.Cleanup(func() {
		ss.Config.Env = originalEnv
		ss.Config.BlobStoreDSN = originalDSN
		ss.Config.ArchiveBlobStoreDSN = originalArchiveDSN
		stagingBytesPerWorker = originalPerWorker
	})

	rec := postInternalBlobPull(t, ss, testNetwork[1].Config.Self.Host, internalBlobPullRequest{
		CID:   "QmStagingFullTestCid",
		Async: true,
	})

	require.Equal(t, http.StatusInsufficientStorage, rec.Code,
		"no room must not look like busy; they earn opposite retries")
	require.Contains(t, rec.Body.String(), "staging")
	require.Len(t, ss.asyncPullQueue, 0, "queued a transfer it has nowhere to stage")
}

// The threshold and the worker count are one decision: every worker can be
// staging a whole blob at once, so raising the count without raising the
// headroom would let a node accept work it has nowhere to put.
func TestPullStagingMinFreeScalesWithWorkers(t *testing.T) {
	ss := blobFetchTestServer(t)

	ss.Config.AsyncPullWorkers = 3
	atThree := ss.pullStagingMinFree()
	ss.Config.AsyncPullWorkers = 12
	atTwelve := ss.pullStagingMinFree()

	require.Greater(t, atTwelve, atThree, "more workers must demand more headroom")
	require.Equal(t, atThree+9*stagingBytesPerWorker, atTwelve)

	// The default configuration must ask for exactly what the fixed threshold
	// this replaced asked for. Otherwise the derivation quietly raises the bar
	// and a node with headroom that was sufficient yesterday starts refusing
	// every pull, with replication stopping rather than anything crashing.
	ss.Config.AsyncPullWorkers = 0
	require.Equal(t, uint64(10<<30), ss.pullStagingMinFree(),
		"the default no longer matches the 10GiB threshold it replaced")
}

func TestAsyncPullTunablesFallBackToDefaults(t *testing.T) {
	ss := blobFetchTestServer(t)

	// Unset (zero) means default, so mediorum.go does not have to restate it.
	require.Equal(t, defaultAsyncPullWorkers, ss.asyncPullWorkers())
	require.Equal(t, defaultAsyncPullTimeout, ss.asyncPullTimeout())

	ss.Config.AsyncPullWorkers = 12
	ss.Config.AsyncPullTimeout = 90 * time.Minute
	require.Equal(t, 12, ss.asyncPullWorkers())
	require.Equal(t, 90*time.Minute, ss.asyncPullTimeout())

	// Clamped: each worker demands staging headroom, so an unbounded value
	// would refuse every pull -- and overflow pullStagingMinFree.
	ss.Config.AsyncPullWorkers = 1_000_000
	require.Equal(t, maxAsyncPullWorkers, ss.asyncPullWorkers())

	ss.Config.AsyncPullWorkers = -1
	require.Equal(t, defaultAsyncPullWorkers, ss.asyncPullWorkers())
}
