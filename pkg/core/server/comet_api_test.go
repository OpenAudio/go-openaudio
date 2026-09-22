package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/monitorrpc"
	"github.com/OpenAudio/go-openaudio/pkg/safemap"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/rpc/client"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type monitoringBackend struct {
	client.Client
	calls int
}

func (b *monitoringBackend) Status(context.Context) (*ctypes.ResultStatus, error) {
	b.calls++
	return &ctypes.ResultStatus{SyncInfo: ctypes.SyncInfo{LatestBlockHeight: 42}}, nil
}

func TestCachedCometStatusPreservesRequestIDs(t *testing.T) {
	backend := &monitoringBackend{}
	s := &Server{config: &config.Config{StateSync: &config.StateSyncConfig{}}, rpc: monitorrpc.New().Wrap(backend), awaitRpcReady: make(chan struct{}), logger: zap.NewNop()}
	s.config.StateSync.ServeSnapshots = true
	close(s.awaitRpcReady)
	for _, id := range []string{`1`, `"another-client"`} {
		req := httptest.NewRequest(http.MethodPost, "/core/crpc", strings.NewReader(`{"jsonrpc":"2.0","id":`+id+`,"method":"status","params":{}}`))
		rec := httptest.NewRecorder()
		require.NoError(t, s.proxyCometRequest(echo.New().NewContext(req, rec)))
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.JSONEq(t, id, string(response.ID))
		require.Contains(t, string(response.Result), `"42"`)
	}
	require.Equal(t, 1, backend.calls)
}

func TestCometValidatorParams(t *testing.T) {
	for _, tc := range []struct{ name, method, url, body string }{
		{"get", http.MethodGet, "/core/crpc/validators?height=12&page=2&per_page=50", ""},
		{"named", http.MethodPost, "/core/crpc", `{"height":"12","page":"2","per_page":"50"}`},
		{"positional", http.MethodPost, "/core/crpc", `["12","2","50"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, p, n, err := cometValidatorParams(httptest.NewRequest(tc.method, tc.url, nil), json.RawMessage(tc.body))
			require.NoError(t, err)
			require.EqualValues(t, 12, *h)
			require.Equal(t, 2, *p)
			require.Equal(t, 50, *n)
		})
	}
	_, _, _, err := cometValidatorParams(httptest.NewRequest(http.MethodGet, "/validators?height=bad", nil), nil)
	require.Error(t, err)
	_, _, _, err = cometValidatorParams(httptest.NewRequest(http.MethodPost, "/", nil), json.RawMessage(`["12"]`))
	require.Error(t, err)
}

func TestNormalizeCometNodeID(t *testing.T) {
	require.Equal(t, "abc", normalizeCometNodeID("ABC"))
	require.Equal(t, "abc", normalizeCometNodeID("ABC@host:26656"))
}

type netInfoBackend struct{ client.Client }

func (*netInfoBackend) NetInfo(context.Context) (*ctypes.ResultNetInfo, error) {
	return &ctypes.ResultNetInfo{Peers: []ctypes.Peer{{NodeInfo: p2p.DefaultNodeInfo{DefaultNodeID: p2p.ID("abc")}}}}, nil
}

func TestP2PConnectionsUseLocalNetInfo(t *testing.T) {
	s := &Server{rpc: monitorrpc.New().Wrap(&netInfoBackend{}), peerStatus: safemap.New[EthAddress, *v1.GetStatusResponse_PeerInfo_Peer]()}
	s.peerStatus.Set("connected", &v1.GetStatusResponse_PeerInfo_Peer{EthAddress: "connected", CometAddress: "ABC@host:26656"})
	s.peerStatus.Set("disconnected", &v1.GetStatusResponse_PeerInfo_Peer{EthAddress: "disconnected", CometAddress: "def", P2PConnected: true})
	// No remote RPC clients: a refresh must still accurately update both peers.
	require.NoError(t, s.refreshP2PConnections(context.Background(), zap.NewNop()))
	connected, _ := s.peerStatus.Get("connected")
	disconnected, _ := s.peerStatus.Get("disconnected")
	require.True(t, connected.P2PConnected)
	require.False(t, disconnected.P2PConnected)
}
