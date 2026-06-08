package etl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"go.uber.org/zap"
)

// startStreamBlocksServer mounts only the StreamBlocks procedure with the given
// handler and returns a CoreServiceClient pointed at it (Connect protocol over
// HTTP/1.1, which supports server streaming — the streamSource code path is
// identical regardless of wire protocol).
func startStreamBlocksServer(t *testing.T, handler func(ctx context.Context, req *connect.Request[corev1.StreamBlocksRequest], stream *connect.ServerStream[corev1.StreamBlocksResponse]) error) corev1connect.CoreServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(corev1connect.CoreServiceStreamBlocksProcedure, connect.NewServerStreamHandler(
		corev1connect.CoreServiceStreamBlocksProcedure, handler,
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return corev1connect.NewCoreServiceClient(srv.Client(), srv.URL)
}

// TestStreamSource_ForwardsAndResumes verifies blocks are forwarded to the
// channel and that, after each stream ends (EOF), the source reconnects
// resuming from the next height.
func TestStreamSource_ForwardsAndResumes(t *testing.T) {
	var mu sync.Mutex
	var requestedStarts []int64

	// Each connection sends exactly one block at the requested start height,
	// then returns (EOF). The source must then reconnect from start+1.
	handler := func(ctx context.Context, req *connect.Request[corev1.StreamBlocksRequest], stream *connect.ServerStream[corev1.StreamBlocksResponse]) error {
		h := req.Msg.StartHeight
		mu.Lock()
		requestedStarts = append(requestedStarts, h)
		mu.Unlock()
		return stream.Send(&corev1.StreamBlocksResponse{Block: &corev1.Block{Height: h}})
	}

	client := startStreamBlocksServer(t, handler)
	s := newStreamSource(client, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.run(ctx, 5)

	var heights []int64
	timeout := time.After(5 * time.Second)
	for len(heights) < 5 {
		select {
		case pb := <-s.C():
			heights = append(heights, pb.Block.Height)
		case <-timeout:
			t.Fatalf("timed out, got heights %v", heights)
		}
	}
	cancel()

	want := []int64{5, 6, 7, 8, 9}
	for i, h := range want {
		if heights[i] != h {
			t.Fatalf("block heights = %v, want prefix %v", heights, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i, h := range want {
		if i >= len(requestedStarts) || requestedStarts[i] != h {
			t.Fatalf("resume start heights = %v, want prefix %v", requestedStarts, want)
		}
	}
}

// TestStreamSource_FallsBackToPolling verifies that when StreamBlocks reports
// Unimplemented, the source switches to the polling prefetcher (which then
// drives blocks off GetBlocks/GetBlock on the same client).
func TestStreamSource_FallsBackToPolling(t *testing.T) {
	streamCalled := make(chan struct{}, 1)
	handler := func(ctx context.Context, req *connect.Request[corev1.StreamBlocksRequest], stream *connect.ServerStream[corev1.StreamBlocksResponse]) error {
		select {
		case streamCalled <- struct{}{}:
		default:
		}
		return connect.NewError(connect.CodeUnimplemented, nil)
	}
	client := startStreamBlocksServer(t, handler)
	s := newStreamSource(client, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.run(ctx, 5); close(done) }()

	select {
	case <-streamCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("StreamBlocks was never called")
	}
	// After Unimplemented, run() hands off to the prefetcher (GetBlocks/GetBlock
	// are unmounted here, so no blocks arrive — just assert no blocks and a clean
	// shutdown on cancel rather than a panic or busy loop).
	select {
	case pb, ok := <-s.C():
		if ok {
			t.Fatalf("unexpected block after fallback: %d", pb.Block.Height)
		}
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
