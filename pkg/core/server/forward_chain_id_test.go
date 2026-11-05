package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/safemap"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestCheckForwardChainID(t *testing.T) {
	const (
		newChain = "audius-mainnet-beta"
		oldChain = "audius-mainnet-alpha-beta"
	)
	require.True(t, config.ForwardChainIDRequired(newChain), "the new chain must refuse unstamped forwards from the start")
	require.False(t, config.ForwardChainIDRequired(oldChain), "the old chain upgrades in place and must keep accepting unstamped forwards")

	for _, tc := range []struct {
		name      string
		own, from string
		refused   bool
	}{
		// The genesis-rollover leak: an old-chain node, too old to stamp,
		// gossiping to a node that has moved to the new chain.
		{name: "unstamped forward on a chain that requires stamping", own: newChain, from: "", refused: true},
		// An old-chain node on a release that stamps, gossiping to a moved node.
		{name: "forward from another chain", own: newChain, from: oldChain, refused: true},
		{name: "forward from another chain, the other way round", own: oldChain, from: newChain, refused: true},
		{name: "forward from the same chain", own: newChain, from: newChain},
		// A rolling upgrade on a chain that does not yet require stamping: the
		// peer has not upgraded, and refusing it would only throttle gossip.
		{name: "unstamped forward on a chain that does not require stamping", own: oldChain, from: ""},
		{name: "unknown own chain accepts whatever it is sent", own: "", from: oldChain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkForwardChainID(tc.own, tc.from)
			if tc.refused {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// recordingCore captures the chain ID on each forwarded transaction.
type recordingCore struct {
	corev1connect.UnimplementedCoreServiceHandler
	got chan string
}

func (r *recordingCore) ForwardTransaction(_ context.Context, req *connect.Request[v1.ForwardTransactionRequest]) (*connect.Response[v1.ForwardTransactionResponse], error) {
	r.got <- req.Msg.GetChainId()
	return connect.NewResponse(&v1.ForwardTransactionResponse{}), nil
}

// A node must stamp its own chain on every forward, or receivers on chains that
// require stamping turn its gossip away.
func TestBroadcastStampsChainID(t *testing.T) {
	peer := &recordingCore{got: make(chan string, 1)}
	mux := http.NewServeMux()
	path, handler := corev1connect.NewCoreServiceHandler(peer)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s := &Server{
		logger:          zap.NewNop(),
		config:          &config.Config{GenesisFile: &cmttypes.GenesisDoc{ChainID: "audius-mainnet-beta"}},
		connectRPCPeers: safemap.New[EthAddress, corev1connect.CoreServiceClient](),
	}
	s.connectRPCPeers.Set("0xpeer", corev1connect.NewCoreServiceClient(srv.Client(), srv.URL))

	s.broadcastMempoolTransaction("key", &MempoolTransaction{
		Tx: &v1.SignedTransaction{Transaction: &v1.SignedTransaction_Plays{Plays: &v1.TrackPlays{}}},
	})

	select {
	case chainID := <-peer.got:
		require.Equal(t, "audius-mainnet-beta", chainID)
	case <-time.After(5 * time.Second):
		t.Fatal("peer never received the forwarded transaction")
	}
}
