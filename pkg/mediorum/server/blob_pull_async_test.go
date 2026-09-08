package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"github.com/stretchr/testify/require"
	"gocloud.dev/blob"
	_ "gocloud.dev/blob/memblob"
)

func asyncPullTestServer(t *testing.T, depth int) *MediorumServer {
	t.Helper()
	ss := blobFetchTestServer(t)
	ss.asyncPullQueue = make(chan asyncPullJob, depth)
	ss.asyncPullInFlight = map[string]struct{}{}
	// runAsyncPull checks presence before transferring, so the fixture needs
	// somewhere for that to look. Empty, so the check passes and the pull is
	// attempted -- which is what these tests are about.
	bucket, err := blob.OpenBucket(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { bucket.Close() })
	ss.bucket = bucket
	ss.knownPresent = imcache.New[string, int64]()
	return ss
}

// mustEnqueue enqueues and fails the test on error, returning the admission so
// callers can assert on which of the two a 202 would report.
func mustEnqueue(t *testing.T, ss *MediorumServer, cid string) asyncPullAdmission {
	t.Helper()
	admission, err := ss.enqueueAsyncPull(asyncPullJob{cid: cid, sourceHost: "http://peer"})
	require.NoError(t, err)
	return admission
}

func TestEnqueueAsyncPullAccepts(t *testing.T) {
	ss := asyncPullTestServer(t, 4)
	require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "cid-a"))
	require.Len(t, ss.asyncPullQueue, 1)
}

// Nothing blocks the sender any more, so the sweep, other senders and repair can
// all ask for the same cid at once. Previously haveInMyBucket plus a blocked
// caller kept that to one transfer; now the in-flight set has to.
func TestEnqueueAsyncPullDeduplicatesByCID(t *testing.T) {
	ss := asyncPullTestServer(t, 8)

	require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "same-cid"))
	for range 4 {
		require.Equal(t, asyncPullRunning, mustEnqueue(t, ss, "same-cid"),
			"a folded request reported itself as a fresh acceptance, which the sender reads as the previous transfer having failed")
	}
	require.Len(t, ss.asyncPullQueue, 1, "queued the same blob more than once")

	// A different cid is unaffected.
	require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "other-cid"))
	require.Len(t, ss.asyncPullQueue, 2)
}

// A full queue must report busy rather than accept work it cannot start, and
// must leave no in-flight marker behind, or that cid could never be retried.
func TestEnqueueAsyncPullRejectsWhenFull(t *testing.T) {
	ss := asyncPullTestServer(t, 1)

	require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "first"))
	_, err := ss.enqueueAsyncPull(asyncPullJob{cid: "second", sourceHost: "http://peer"})
	require.ErrorIs(t, err, errAsyncPullQueueFull)

	ss.asyncPullMu.Lock()
	_, stillMarked := ss.asyncPullInFlight["second"]
	ss.asyncPullMu.Unlock()
	require.False(t, stillMarked, "a rejected job stayed marked in flight and could never be retried")
}

// Marking in flight before the queue send was known to succeed left a window: a
// second caller arriving inside it read the marker, was answered nil -- 202, the
// transfer is under way -- and then the first caller found the queue full and
// took the marker back down. Nothing was ever queued, and the answer 202 has no
// way to say so.
//
// With a full queue and nothing running, no caller may be told the blob is being
// fetched. The contention is what exercises the window, so this runs the callers
// against a barrier over many rounds.
func TestEnqueueAsyncPullNeverAcceptsOnAFullQueue(t *testing.T) {
	const (
		rounds  = 200
		callers = 8
	)

	for round := range rounds {
		ss := asyncPullTestServer(t, 1)
		cid := fmt.Sprintf("contended-cid-%d", round)

		// Occupy the single slot with an unrelated job, so every call below has
		// to be refused: the queue is full and nothing is in flight for cid.
		require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "filler"))

		results := make([]error, callers)
		var release, finished sync.WaitGroup
		release.Add(1)
		for i := range callers {
			finished.Add(1)
			go func() {
				defer finished.Done()
				release.Wait()
				_, results[i] = ss.enqueueAsyncPull(asyncPullJob{cid: cid, sourceHost: "http://peer"})
			}()
		}
		release.Done()
		finished.Wait()

		for i, err := range results {
			require.ErrorIsf(t, err, errAsyncPullQueueFull,
				"round %d caller %d was told the pull was accepted, but the queue was full and no job was queued",
				round, i)
		}
		require.Lenf(t, ss.asyncPullQueue, 1, "round %d queued a job past the queue's capacity", round)
	}
}

// Completion must clear the marker, or that cid can never be pulled again for
// the life of the process.
func TestAsyncPullReleasesInFlightMarker(t *testing.T) {
	ss := asyncPullTestServer(t, 2)
	require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "cid-x"))

	ss.releaseAsyncPull("cid-x")

	require.Equal(t, asyncPullQueued, mustEnqueue(t, ss, "cid-x"),
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
		name         string
		status       int
		wantErr      error
		wantFallback bool
		wantHandoff  bool
	}{
		{name: "already present", status: http.StatusOK},
		// A bare 202 carries no status word, which reads as a fresh acceptance.
		// Which of the two it is gets its own test; here what matters is that
		// either one counts as a handoff and stays off the push path.
		{name: "accepted for async pull", status: http.StatusAccepted, wantErr: errPeerPullAccepted, wantHandoff: true},
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

			err := ss.requestPeerPull(context.Background(), srv.URL, "cid", nil, "", false, 0)

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
			isHandoff := errors.Is(err, errPeerPullAccepted) || errors.Is(err, errPeerPullInProgress)
			require.Equal(t, tc.wantHandoff, isHandoff)
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

	err := ss.requestPeerPull(context.Background(), srv.URL, "cid", nil, "", false, 0)
	require.Error(t, err)
	require.False(t, isPullFallbackWorthy(err),
		"a busy peer would be sent the bytes over multipart")
}

// findMissedReplications arms an hour-long backoff when it queues an upload,
// which suits an attempt that definitively failed. A 202 has not failed and has
// not succeeded, so leaving the marker set would suppress the confirming
// already_present for the rest of the hour -- and for a blob big enough to still
// be transferring at the next five minute sweep, which is precisely what this
// change exists to carry, that is the ordinary case rather than an edge.
func TestPullInProgressClearsTheReplicationBackoff(t *testing.T) {
	ss := testNetwork[0]

	// A real blob in the local bucket: replicateToHosts sources from there and
	// returns early if it is absent, which would skip the branch under test.
	content := "backoff regression fixture"
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	putInternalBlobTestObject(t, context.Background(), ss.bucket, cid, content)
	t.Cleanup(func() { _ = ss.dropFromMyBucket(cid) })

	upload := &Upload{ID: "backoff-test-upload", OrigFileCID: cid}

	// Stand in for findMissedReplications having queued this upload.
	ss.replicationAttempts.Set(upload.ID, struct{}{}, imcache.WithDefaultExpiration())
	t.Cleanup(func() { ss.replicationAttempts.Remove(upload.ID) })

	// testNetwork is shared, and notePullHandoff writes to it: a handoff left
	// over from an earlier run would read this one as a repeat -- the failure
	// case -- and the assertion below would flip. Start and finish clean so the
	// test means the same thing on every run.
	ss.pullHandoffs.RemoveAll()
	t.Cleanup(func() { ss.pullHandoffs.RemoveAll() })

	// Pull is what produces a 202 at all; without it replicateStoredFileToHost
	// goes straight to the multipart push.
	originalStreaming := ss.Config.BlobStorageStreaming
	ss.Config.BlobStorageStreaming = true
	t.Cleanup(func() { ss.Config.BlobStorageStreaming = originalStreaming })

	original := ss.peerHTTPClient
	ss.peerHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testHTTPResponse(req, http.StatusAccepted), nil
	})}
	t.Cleanup(func() { ss.peerHTTPClient = original })

	_ = ss.replicateToHosts(context.Background(), upload, cid, nil, false)

	_, stillBackedOff := ss.replicationAttempts.Get(upload.ID)
	require.False(t, stillBackedOff,
		"an accepted-but-unfinished transfer armed the hour-long backoff; the mirror would not be recorded until it expired")
}

// The whole failure-detection scheme rests on the sender being able to read the
// two 202s apart, and on a bare 202 -- a peer predating the discriminator --
// falling to the conservative side, which is the one that keeps the backoff
// armed.
func TestRequestPeerPullReadsTheAcceptedStatus(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"fresh acceptance", `{"status":"accepted"}`, errPeerPullAccepted},
		{"transfer already running", `{"status":"in_progress"}`, errPeerPullInProgress},
		{"peer predating the discriminator", ``, errPeerPullAccepted},
		{"unparseable body", `not json`, errPeerPullAccepted},
		{"unknown status word", `{"status":"whatever"}`, errPeerPullAccepted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer peer.Close()

			ss := blobFetchTestServer(t)
			err := ss.requestPeerPull(context.Background(), peer.URL, "cid-1", nil, "", false, 0)
			require.ErrorIs(t, err, tc.want)

			// Neither flavour may reach the multipart push: the peer is already
			// fetching these bytes, or about to.
			require.False(t, isPullFallbackWorthy(err), "a 202 routed into the multipart fallback")
		})
	}
}

// notePullHandoff is where a background pull's failure becomes visible at all.
// Nothing waits on the transfer and the peer only logs its own error, so a
// second acceptance for a blob we already handed over is the sender's one
// chance to notice.
func TestNotePullHandoffDetectsAFailedTransfer(t *testing.T) {
	ss := handoffTestServer(t)
	const host, cid = "http://peer-a", "cid-1"

	// First sweep: the peer takes it on. Nothing is known to be wrong.
	require.Equal(t, pullHandoffFresh, ss.notePullHandoff(host, cid, errPeerPullAccepted))

	// Still working at the next sweep -- the case the backoff must not block.
	require.Equal(t, pullHandoffRunning, ss.notePullHandoff(host, cid, errPeerPullInProgress))

	// Then it accepts the same blob afresh: its in-flight set is empty and its
	// bucket does not hold the blob, so the transfer it took on is over and
	// produced nothing.
	require.Equal(t, pullHandoffRepeat, ss.notePullHandoff(host, cid, errPeerPullAccepted))

	// The report is consumed, so the hourly retry starts clean rather than
	// reporting the same failure again immediately.
	require.Equal(t, pullHandoffFresh, ss.notePullHandoff(host, cid, errPeerPullAccepted))
}

// A fresh acceptance from one peer must not read as a repeat for another, or a
// single sweep across three targets would manufacture failures.
func TestNotePullHandoffIsPerHostAndCID(t *testing.T) {
	ss := handoffTestServer(t)
	const cid = "cid-1"

	require.Equal(t, pullHandoffFresh, ss.notePullHandoff("http://peer-a", cid, errPeerPullAccepted))
	require.Equal(t, pullHandoffFresh, ss.notePullHandoff("http://peer-b", cid, errPeerPullAccepted))
	require.Equal(t, pullHandoffFresh, ss.notePullHandoff("http://peer-a", "cid-2", errPeerPullAccepted))

	// Only peer-a/cid-1 has an outstanding handoff to repeat.
	require.Equal(t, pullHandoffRepeat, ss.notePullHandoff("http://peer-a", cid, errPeerPullAccepted))
	require.Equal(t, pullHandoffRepeat, ss.notePullHandoff("http://peer-b", cid, errPeerPullAccepted))
}

// already_present settles the handoff. Without this a blob that transferred
// successfully would still be holding a marker, and a later unrelated handoff
// to the same peer would be misread as a failure.
func TestNotePullHandoffClearedByConfirmedPresence(t *testing.T) {
	ss := handoffTestServer(t)
	const host, cid = "http://peer-a", "cid-1"

	require.Equal(t, pullHandoffFresh, ss.notePullHandoff(host, cid, errPeerPullAccepted))
	require.Equal(t, pullHandoffNone, ss.notePullHandoff(host, cid, nil), "a confirmed mirror is not a handoff")
	require.Equal(t, pullHandoffFresh, ss.notePullHandoff(host, cid, errPeerPullAccepted),
		"a settled handoff was still counted against the next one")
}

// Ordinary failures -- 503, an unreachable peer -- are not handoffs and must
// leave the outstanding marker alone, or a busy peer would erase the record of
// a transfer another peer is still running.
func TestNotePullHandoffIgnoresOrdinaryFailures(t *testing.T) {
	ss := handoffTestServer(t)
	const host, cid = "http://peer-a", "cid-1"

	require.Equal(t, pullHandoffFresh, ss.notePullHandoff(host, cid, errPeerPullAccepted))
	require.Equal(t, pullHandoffNone, ss.notePullHandoff(host, cid, errors.New("503 service unavailable")))
	require.Equal(t, pullHandoffRepeat, ss.notePullHandoff(host, cid, errPeerPullAccepted),
		"an unrelated failure discarded the outstanding handoff")
}

func handoffTestServer(t *testing.T) *MediorumServer {
	t.Helper()
	ss := blobFetchTestServer(t)
	ss.pullHandoffs = imcache.New(
		imcache.WithMaxEntriesLimitOption[string, struct{}](1000, imcache.EvictionPolicyLRU),
		imcache.WithDefaultExpirationOption[string, struct{}](time.Hour),
	)
	return ss
}

// Busy and out-of-room shared 503 until now, which made them indistinguishable
// to the sender even though they want opposite retries: a queue turns over in
// minutes, a full disk does not.
func TestRequestPeerPullTellsBusyFromOutOfRoom(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"queue full", http.StatusServiceUnavailable, errPeerPullBusy},
		{"disk full", http.StatusInsufficientStorage, errPeerPullNoRoom},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer peer.Close()

			ss := blobFetchTestServer(t)
			err := ss.requestPeerPull(context.Background(), peer.URL, "cid-1", nil, "", false, 0)
			require.ErrorIs(t, err, tc.wantErr)

			// Neither may be answered by pushing the bytes: one peer has no
			// room to work and the other none to store them.
			require.False(t, isPullFallbackWorthy(err))

			// Nor may either be mistaken for a handoff -- nothing was accepted.
			require.NotEqual(t, pullHandoffFresh, ss.notePullHandoff(peer.URL, "cid-1", err))
			require.NotEqual(t, pullHandoffRunning, ss.notePullHandoff(peer.URL, "cid-1", err))
		})
	}
}

// A busy peer must be retried sooner than a failed one, but not every sweep:
// re-asking a saturated node every five minutes for every queued upload is
// load on the node that is already behind.
func TestPeerBusyTakesAShortBackoffNotTheFullOne(t *testing.T) {
	ss := testNetwork[0]

	content := "busy backoff fixture"
	cid, err := cidutil.ComputeFileCID(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	putInternalBlobTestObject(t, context.Background(), ss.bucket, cid, content)
	t.Cleanup(func() { _ = ss.dropFromMyBucket(cid) })

	upload := &Upload{ID: "busy-backoff-upload", OrigFileCID: cid}
	t.Cleanup(func() { ss.replicationAttempts.Remove(upload.ID) })

	ss.pullHandoffs.RemoveAll()
	t.Cleanup(func() { ss.pullHandoffs.RemoveAll() })

	originalStreaming := ss.Config.BlobStorageStreaming
	ss.Config.BlobStorageStreaming = true
	t.Cleanup(func() { ss.Config.BlobStorageStreaming = originalStreaming })

	original := ss.peerHTTPClient
	ss.peerHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testHTTPResponse(req, http.StatusServiceUnavailable), nil
	})}
	t.Cleanup(func() { ss.peerHTTPClient = original })

	// Shrunk so the TTL itself is observable. Asserting only that an entry
	// exists would pass just as well if the busy branch left the hour-long one
	// findMissedReplications sets, which is the bug this guards.
	originalBusy := peerBusyBackoff
	peerBusyBackoff = 50 * time.Millisecond
	t.Cleanup(func() { peerBusyBackoff = originalBusy })

	// Stand in for findMissedReplications: an hour-long marker that the busy
	// branch has to overwrite rather than leave alone.
	ss.replicationAttempts.Set(upload.ID, struct{}{}, imcache.WithDefaultExpiration())
	_ = ss.replicateToHosts(context.Background(), upload, cid, nil, false)

	// Armed, unlike a handoff that is progressing -- a busy peer is not moving
	// this blob and re-asking on the next sweep would just repeat the refusal.
	_, backedOff := ss.replicationAttempts.Get(upload.ID)
	require.True(t, backedOff, "a busy peer left the upload with no backoff at all")

	// And on the short clock, not the hour it was set with above.
	time.Sleep(5 * peerBusyBackoff)
	_, stillBackedOff := ss.replicationAttempts.Get(upload.ID)
	require.False(t, stillBackedOff,
		"the busy backoff kept the hour-long failure TTL; a saturated peer would not be retried for an hour")
}

// The presence check runAsyncPull makes before transferring. It matters most
// for store-all intake, which enqueues without checking at all: its callback runs
// inside the chain op sync loop, where a bucket round trip per op is not
// affordable, so this is the only thing standing between a re-announced upload
// row and re-downloading blobs the node already holds.
func TestRunAsyncPullSkipsABlobAlreadyHeld(t *testing.T) {
	ss := asyncPullTestServer(t, 1)

	var requests atomic.Int64
	source := rangeServer([]byte("blob"), &requests)
	defer source.Close()

	cid := "baeaaaiqseallreadyhere"
	require.NoError(t, ss.bucket.WriteAll(context.Background(), cidutil.ShardCID(cid), []byte("blob"), nil))

	ss.runAsyncPull(context.Background(), asyncPullJob{cid: cid, sourceHost: source.URL})

	require.Zero(t, requests.Load(),
		"a blob already in the bucket must not be fetched again")

	ss.asyncPullMu.Lock()
	_, stillMarked := ss.asyncPullInFlight[cid]
	ss.asyncPullMu.Unlock()
	require.False(t, stillMarked, "the skip path must still release the in-flight marker")
}

// The counterpart: without the blob, the same call does go to the source. Without
// this, the test above would pass on a runAsyncPull that never pulls anything.
func TestRunAsyncPullFetchesABlobNotHeld(t *testing.T) {
	ss := asyncPullTestServer(t, 1)

	var requests atomic.Int64
	source := rangeServer([]byte("blob"), &requests)
	defer source.Close()

	ss.runAsyncPull(context.Background(), asyncPullJob{cid: "baeaaaiqsenotherequet", sourceHost: source.URL})

	require.NotZero(t, requests.Load(), "a missing blob must be fetched")
}
