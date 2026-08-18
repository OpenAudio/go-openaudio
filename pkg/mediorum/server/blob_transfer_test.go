package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The whole point of the guard is that these two cases are told apart: both go
// well past the stall window, and only the one making no progress is cut off.

func TestTransferGuardCancelsWhenNoBytesMove(t *testing.T) {
	guard := newTransferGuard(context.Background(), 100*time.Millisecond, time.Minute)
	defer guard.release()

	select {
	case <-guard.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("guard did not cancel a stalled transfer")
	}
	require.ErrorIs(t, guard.ctx.Err(), context.Canceled)
}

func TestTransferGuardSurvivesSlowButMovingTransfer(t *testing.T) {
	stall := 100 * time.Millisecond
	guard := newTransferGuard(context.Background(), stall, time.Minute)
	defer guard.release()

	// Five stall windows' worth of elapsed time, with a trickle of bytes
	// throughout -- a long transfer, not a stalled one.
	reader := guard.reader(&trickleReader{chunks: 10, delay: stall / 2})
	n, err := io.Copy(io.Discard, reader)
	require.NoError(t, err)
	require.EqualValues(t, 10, n)
	require.NoError(t, guard.ctx.Err())
}

func TestTransferGuardEnforcesCeilingDespiteProgress(t *testing.T) {
	guard := newTransferGuard(context.Background(), time.Minute, 150*time.Millisecond)
	defer guard.release()

	select {
	case <-guard.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("guard did not enforce its ceiling")
	}
	require.ErrorIs(t, guard.ctx.Err(), context.DeadlineExceeded)
}

func TestTransferGuardSettleStopsStallDetection(t *testing.T) {
	stall := 100 * time.Millisecond
	guard := newTransferGuard(context.Background(), stall, time.Minute)
	defer guard.release()

	// The payload is on the wire and the peer is committing it. Silence past
	// the stall window is expected here and must not cancel the request.
	guard.settle()

	time.Sleep(4 * stall)
	require.NoError(t, guard.ctx.Err())
}

func TestTransferGuardWriterReportsProgress(t *testing.T) {
	stall := 100 * time.Millisecond
	guard := newTransferGuard(context.Background(), stall, time.Minute)
	defer guard.release()

	var sink bytes.Buffer
	writer := guard.writer(&sink)
	for range 10 {
		time.Sleep(stall / 2)
		_, err := writer.Write([]byte("x"))
		require.NoError(t, err)
	}

	require.NoError(t, guard.ctx.Err())
	require.Equal(t, 10, sink.Len())
}

func TestGuardedBodyReleasesGuardOnClose(t *testing.T) {
	guard := newTransferGuard(context.Background(), time.Minute, time.Minute)
	body := &guardedBody{
		Reader: guard.reader(strings.NewReader("payload")),
		body:   io.NopCloser(strings.NewReader("")),
		guard:  guard,
	}

	require.NoError(t, guard.ctx.Err())
	require.NoError(t, body.Close())
	require.ErrorIs(t, guard.ctx.Err(), context.Canceled)
}

// trickleReader returns one byte at a time, pausing before each.
type trickleReader struct {
	chunks int
	delay  time.Duration
}

func (r *trickleReader) Read(b []byte) (int, error) {
	if r.chunks == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.chunks--
	b[0] = 'x'
	return 1, nil
}

// withStallTimeout shrinks the stall window for the duration of a test.
func withStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	original := blobTransferStallTimeout
	blobTransferStallTimeout = d
	t.Cleanup(func() { blobTransferStallTimeout = original })
}

// A guard that cancels a context nobody is watching would be worse than the
// timeout it replaces: the transfer would hang instead of failing. These two
// tests run against a real HTTP server to prove the bound reaches the request.

func TestOpenBlobFromHostAbandonsPeerThatGoesQuiet(t *testing.T) {
	withStallTimeout(t, 200*time.Millisecond)

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // then say nothing more
	}))
	defer peer.Close()

	body, err := testNetwork[0].openBlobFromHost(context.Background(), peer.URL, "quiet-peer-cid")
	require.NoError(t, err)
	defer body.Close()

	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(io.Discard, body)
		done <- copyErr
	}()

	select {
	case copyErr := <-done:
		require.Error(t, copyErr, "read should have been abandoned, not hung")
	case <-time.After(5 * time.Second):
		t.Fatal("read hung past the stall window; the guard is not bound to the request")
	}
}

func TestOpenBlobFromHostCompletesSlowTransfer(t *testing.T) {
	stall := 200 * time.Millisecond
	withStallTimeout(t, stall)

	// Ten chunks at half the stall window each: five times longer than the
	// stall bound overall, but never quiet for long enough to trip it.
	payload := strings.Repeat("y", 10)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := range payload {
			time.Sleep(stall / 2)
			_, _ = w.Write([]byte{payload[i]})
			w.(http.Flusher).Flush()
		}
	}))
	defer peer.Close()

	body, err := testNetwork[0].openBlobFromHost(context.Background(), peer.URL, "slow-peer-cid")
	require.NoError(t, err)
	defer body.Close()

	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, payload, string(got))
}
