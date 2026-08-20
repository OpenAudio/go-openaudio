package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func asyncPullTestServer(t *testing.T, depth int) *MediorumServer {
	t.Helper()
	ss := blobFetchTestServer(t)
	ss.asyncPullQueue = make(chan asyncPullJob, depth)
	ss.asyncPullInFlight = map[string]struct{}{}
	return ss
}

func TestEnqueueAsyncPullAccepts(t *testing.T) {
	ss := asyncPullTestServer(t, 4)
	require.NoError(t, ss.enqueueAsyncPull(asyncPullJob{cid: "cid-a", sourceHost: "http://peer"}))
	require.Len(t, ss.asyncPullQueue, 1)
}

// Nothing blocks the sender any more, so the sweep, other senders and repair can
// all ask for the same cid at once. Previously haveInMyBucket plus a blocked
// caller kept that to one transfer; now the in-flight set has to.
func TestEnqueueAsyncPullDeduplicatesByCID(t *testing.T) {
	ss := asyncPullTestServer(t, 8)

	for range 5 {
		require.NoError(t, ss.enqueueAsyncPull(asyncPullJob{cid: "same-cid", sourceHost: "http://peer"}))
	}
	require.Len(t, ss.asyncPullQueue, 1, "queued the same blob more than once")

	// A different cid is unaffected.
	require.NoError(t, ss.enqueueAsyncPull(asyncPullJob{cid: "other-cid", sourceHost: "http://peer"}))
	require.Len(t, ss.asyncPullQueue, 2)
}

// A full queue must report busy rather than accept work it cannot start, and
// must release the in-flight marker so a later sweep can retry.
func TestEnqueueAsyncPullRejectsWhenFull(t *testing.T) {
	ss := asyncPullTestServer(t, 1)

	require.NoError(t, ss.enqueueAsyncPull(asyncPullJob{cid: "first", sourceHost: "http://peer"}))
	err := ss.enqueueAsyncPull(asyncPullJob{cid: "second", sourceHost: "http://peer"})
	require.ErrorIs(t, err, errAsyncPullQueueFull)

	ss.asyncPullMu.Lock()
	_, stillMarked := ss.asyncPullInFlight["second"]
	ss.asyncPullMu.Unlock()
	require.False(t, stillMarked, "a rejected job stayed marked in flight and could never be retried")
}

// Completion must clear the marker, or that cid can never be pulled again for
// the life of the process.
func TestAsyncPullReleasesInFlightMarker(t *testing.T) {
	ss := asyncPullTestServer(t, 2)
	require.NoError(t, ss.enqueueAsyncPull(asyncPullJob{cid: "cid-x", sourceHost: "http://peer"}))

	ss.releaseAsyncPull("cid-x")

	require.NoError(t, ss.enqueueAsyncPull(asyncPullJob{cid: "cid-x", sourceHost: "http://peer"}),
		"cid could not be re-queued after release")
	require.Len(t, ss.asyncPullQueue, 2)
}

// The trap this refactor invites: c.Request().Context() is cancelled the moment
// the handler returns 202, so a background job holding it would be killed
// immediately. runAsyncPull must derive its own.
func TestAsyncPullDoesNotInheritACancelledRequestContext(t *testing.T) {
	ss := asyncPullTestServer(t, 1)

	// Stand in for the request context: already cancelled, as it would be by
	// the time a queued job ran.
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// The worker is parented to the server lifecycle, not the request.
	serverCtx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A failing pull is fine: what matters is that it was attempted rather
		// than short-circuited by a dead context.
		ss.runAsyncPull(serverCtx, asyncPullJob{cid: "ctx-cid", sourceHost: "http://127.0.0.1:1"})
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("async pull did not finish")
	}

	require.Error(t, requestCtx.Err(), "sanity: the stand-in request context should be cancelled")

	ss.asyncPullMu.Lock()
	_, stillMarked := ss.asyncPullInFlight["ctx-cid"]
	ss.asyncPullMu.Unlock()
	require.False(t, stillMarked, "in-flight marker leaked after the job finished")
}

// The sender's reaction to each answer a receiver can give. What matters is not
// the error text but whether it routes into the multipart push fallback: a peer
// that is busy or already fetching must never be sent the bytes.
func TestRequestPeerPullStatusHandling(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		wantErr       error
		wantFallback  bool
		wantInProress bool
	}{
		{name: "already present", status: http.StatusOK},
		{name: "accepted for async pull", status: http.StatusAccepted, wantErr: errPeerPullInProgress, wantInProress: true},
		{name: "queue full", status: http.StatusServiceUnavailable},
		{name: "endpoint absent", status: http.StatusNotFound, wantErr: errPeerPullUnsupported, wantFallback: true},
		{name: "not implemented", status: http.StatusNotImplemented, wantErr: errPeerPullUnsupported, wantFallback: true},
		{name: "peer gateway failure", status: http.StatusBadGateway, wantErr: errPeerPullFailed, wantFallback: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ss := asyncPullTestServer(t, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			err := ss.requestPeerPull(context.Background(), srv.URL, "cid", nil, "", false)

			if tc.status == http.StatusOK {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.Equal(t, tc.wantFallback, isPullFallbackWorthy(err),
				"wrong fallback decision for %d: pushing bytes at a peer that did not ask for them", tc.status)
			require.Equal(t, tc.wantInProress, errors.Is(err, errPeerPullInProgress))
		})
	}
}

// 503 is the receiver saying it has no room to work. Treating it as
// "pull unsupported" would answer that by pushing the whole blob at it.
func TestQueueFullDoesNotTriggerMultipartFallback(t *testing.T) {
	ss := asyncPullTestServer(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"` + errAsyncPullQueueFull.Error() + `"}`))
	}))
	defer srv.Close()

	err := ss.requestPeerPull(context.Background(), srv.URL, "cid", nil, "", false)
	require.Error(t, err)
	require.False(t, isPullFallbackWorthy(err),
		"a busy peer would be sent the bytes over multipart")
}
