package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func countReplicateFileToHostGoroutines() int {
	var stacks bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
	return strings.Count(stacks.String(), "replicateFileToHost.func1")
}

func TestReplicateFileToHostClosesPipeOnEarlyHTTPError(t *testing.T) {
	ss := testNetwork[0]
	originalClient := ss.peerHTTPClient
	ss.peerHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
	}
	t.Cleanup(func() {
		ss.peerHTTPClient = originalClient
	})

	before := countReplicateFileToHostGoroutines()
	err := ss.replicateFileToHost(
		context.Background(),
		"http://unread-peer.test",
		"leak-regression-cid",
		strings.NewReader(strings.Repeat("x", 1024*1024)),
		nil,
	)
	assert.Error(t, err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if countReplicateFileToHostGoroutines() <= before {
			return
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, countReplicateFileToHostGoroutines(), before)
}

// The sender now hands the transfer off and returns immediately, so both of
// these assert against the receiver's settled state rather than the sender's
// error. waitForAsyncPull makes that deterministic: the in-flight marker is set
// before the handler answers 202 and cleared when the job finishes, so there is
// no window where the test could observe an unstarted transfer.
func waitForAsyncPull(t *testing.T, ss *MediorumServer, cid string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ss.asyncPullMu.Lock()
		_, running := ss.asyncPullInFlight[cid]
		ss.asyncPullMu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async pull of %s did not finish", cid)
}

func TestRequestPeerPullStoresValidatedBlob(t *testing.T) {
	source := testNetwork[0]
	target := testNetwork[1]
	content := "receiver pull replication"
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)

	putInternalBlobTestObject(t, context.Background(), source.bucket, cid, content)
	t.Cleanup(func() {
		_ = source.dropFromMyBucket(cid)
		_ = target.dropFromMyBucket(cid)
	})

	err = source.requestPeerPull(
		context.Background(),
		target.Config.Self.Host,
		cid,
		[]string{target.Config.Self.Host},
		"",
		true,
	)
	// Accepted, not complete: the bytes have not moved yet.
	require.ErrorIs(t, err, errPeerPullInProgress)

	waitForAsyncPull(t, target, cid)

	reader, _, err := target.readBlob(context.Background(), cidutil.ShardCID(cid))
	require.NoError(t, err)
	defer reader.Close()
	stored, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, content, string(stored))
}

// Validation still happens on the receiver; what changed is that its verdict is
// no longer reported to the sender. The guarantee that matters -- a blob whose
// content does not match its CID is never stored -- is unaffected, and the
// sender learns of the failure by the peer not answering already_present on the
// next sweep.
func TestRequestPeerPullRejectsCIDMismatch(t *testing.T) {
	source := testNetwork[0]
	target := testNetwork[1]
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte("expected")))
	require.NoError(t, err)

	putInternalBlobTestObject(t, context.Background(), source.bucket, cid, "different")
	t.Cleanup(func() {
		_ = source.dropFromMyBucket(cid)
		_ = target.dropFromMyBucket(cid)
	})

	err = source.requestPeerPull(
		context.Background(),
		target.Config.Self.Host,
		cid,
		[]string{target.Config.Self.Host},
		"",
		true,
	)
	require.ErrorIs(t, err, errPeerPullInProgress)

	waitForAsyncPull(t, target, cid)
	require.False(t, target.haveInMyBucket(cid), "stored a blob that failed CID validation")
}

func TestReplicateStoredFileToHostUsesPullWithoutReadingSourceBucket(t *testing.T) {
	ss := &MediorumServer{
		Config: MediorumConfig{
			Self:                 testNetwork[0].Config.Self,
			BlobStorageStreaming: true,
		},
		logger: zap.NewNop(),
		peerHTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "/internal/blobs/pull", req.URL.Path)
			return testHTTPResponse(req, http.StatusOK), nil
		})},
	}

	require.NoError(t, ss.replicateStoredFileToHost(
		context.Background(),
		"http://pull-peer.test",
		"direct-cid",
		nil,
		"unused-source-key",
		nil,
		"",
		true,
	))
}

func TestReplicateStoredFileToHostFallsBackForOlderPeer(t *testing.T) {
	bucket := openMemBucket(t)
	content := "multipart fallback"
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	key := putInternalBlobTestObject(t, context.Background(), bucket, cid, content)

	requests := 0
	ss := &MediorumServer{
		Config: MediorumConfig{
			Self:                 testNetwork[0].Config.Self,
			BlobStorageStreaming: true,
		},
		logger: zap.NewNop(),
		peerHTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			switch req.URL.Path {
			case "/internal/blobs/pull":
				var payload internalBlobPullRequest
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, cid, payload.CID)
				return testHTTPResponse(req, http.StatusNotFound), nil
			case "/internal/blobs":
				multipartReader, err := req.MultipartReader()
				require.NoError(t, err)
				part, err := multipartReader.NextPart()
				require.NoError(t, err)
				require.Equal(t, cid, part.FileName())
				body, err := io.ReadAll(part)
				require.NoError(t, err)
				require.Equal(t, content, string(body))
				return testHTTPResponse(req, http.StatusOK), nil
			default:
				t.Fatalf("unexpected request path %s", req.URL.Path)
				return nil, nil
			}
		})},
	}

	require.NoError(t, ss.replicateStoredFileToHost(
		context.Background(),
		"http://old-peer.test",
		cid,
		bucket,
		key,
		nil,
		"",
		true,
	))
	require.Equal(t, 2, requests)
}

func TestReplicateStoredFileToHostDoesNotFallbackOnValidationFailure(t *testing.T) {
	requests := 0
	ss := &MediorumServer{
		Config: MediorumConfig{
			Self:                 testNetwork[0].Config.Self,
			BlobStorageStreaming: true,
		},
		logger: zap.NewNop(),
		peerHTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return testHTTPResponse(req, http.StatusUnprocessableEntity), nil
		})},
	}

	err := ss.replicateStoredFileToHost(
		context.Background(),
		"http://pull-peer.test",
		"invalid-cid",
		nil,
		"unused-source-key",
		nil,
		"",
		true,
	)
	require.Error(t, err)
	require.Equal(t, 1, requests)
}

func testHTTPResponse(req *http.Request, statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
}
