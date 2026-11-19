package mirror

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/stretchr/testify/require"
)

// slowNode answers ForwardTransaction after a delay, or with err.
type slowNode struct {
	v1connect.UnimplementedCoreServiceHandler
	delay time.Duration
	err   error
	calls atomic.Int32
}

func (n *slowNode) ForwardTransaction(ctx context.Context, _ *connect.Request[corev1.ForwardTransactionRequest]) (*connect.Response[corev1.ForwardTransactionResponse], error) {
	n.calls.Add(1)
	time.Sleep(n.delay)
	if n.err != nil {
		return nil, n.err
	}
	return connect.NewResponse(&corev1.ForwardTransactionResponse{}), nil
}

func serveNodes(t *testing.T, nodes ...*slowNode) *ConnectTarget {
	t.Helper()
	var urls []string
	for _, n := range nodes {
		_, h := v1connect.NewCoreServiceHandler(n)
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		urls = append(urls, srv.URL)
	}
	target, err := NewConnectTarget("test-chain", urls, http.DefaultClient)
	require.NoError(t, err)
	return target
}

func TestForwardReachesValidatorsConcurrently(t *testing.T) {
	const delay = 300 * time.Millisecond
	nodes := []*slowNode{{delay: delay}, {delay: delay}, {delay: delay}}
	target := serveNodes(t, nodes...)

	start := time.Now()
	require.NoError(t, target.Forward(context.Background(), &corev1.SignedTransaction{}))
	elapsed := time.Since(start)

	for _, n := range nodes {
		require.EqualValues(t, 1, n.calls.Load())
	}
	// One at a time would take 3×delay.
	require.Less(t, elapsed, 2*delay)
}

func TestForwardReportsTheFirstNodesError(t *testing.T) {
	refused := connect.NewError(connect.CodeInvalidArgument, errors.New("refused"))
	busy := connect.NewError(connect.CodeUnavailable, errors.New("busy"))
	nodes := []*slowNode{{}, {err: refused}, {err: busy}}
	target := serveNodes(t, nodes...)

	err := target.Forward(context.Background(), &corev1.SignedTransaction{})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	// Every node is still asked, even when one refuses.
	for _, n := range nodes {
		require.EqualValues(t, 1, n.calls.Load())
	}
}
