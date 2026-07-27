package etl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"go.uber.org/zap"
)

func startNodeInfoServer(t *testing.T, handler func(context.Context, *connect.Request[corev1.GetNodeInfoRequest]) (*connect.Response[corev1.GetNodeInfoResponse], error)) corev1connect.CoreServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(corev1connect.CoreServiceGetNodeInfoProcedure, connect.NewUnaryHandler(
		corev1connect.CoreServiceGetNodeInfoProcedure,
		handler,
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return corev1connect.NewCoreServiceClient(srv.Client(), srv.URL)
}

func TestInitializeChainIDUsesCoreNodeInfo(t *testing.T) {
	client := startNodeInfoServer(t, func(context.Context, *connect.Request[corev1.GetNodeInfoRequest]) (*connect.Response[corev1.GetNodeInfoResponse], error) {
		return connect.NewResponse(&corev1.GetNodeInfoResponse{Chainid: "audius-mainnet-alpha-beta"}), nil
	})
	ix := New(client, zap.NewNop())

	if err := ix.InitializeChainID(context.Background()); err != nil {
		t.Fatalf("InitializeChainID: %v", err)
	}
	if ix.ChainID != "audius-mainnet-alpha-beta" {
		t.Fatalf("ChainID = %q, want audius-mainnet-alpha-beta", ix.ChainID)
	}
}

func TestInitializeChainIDRetriesTransientCoreNodeInfoFailure(t *testing.T) {
	attempts := 0
	client := startNodeInfoServer(t, func(context.Context, *connect.Request[corev1.GetNodeInfoRequest]) (*connect.Response[corev1.GetNodeInfoResponse], error) {
		attempts++
		if attempts == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("core unavailable"))
		}
		return connect.NewResponse(&corev1.GetNodeInfoResponse{Chainid: "audius-mainnet-alpha-beta"}), nil
	})
	ix := New(client, zap.NewNop())

	if err := ix.initializeChainID(context.Background(), 2, 0); err != nil {
		t.Fatalf("InitializeChainID: %v", err)
	}
	if ix.ChainID != "audius-mainnet-alpha-beta" {
		t.Fatalf("ChainID = %q, want audius-mainnet-alpha-beta", ix.ChainID)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestInitializeChainIDFailsWhenCoreNodeInfoFails(t *testing.T) {
	client := startNodeInfoServer(t, func(context.Context, *connect.Request[corev1.GetNodeInfoRequest]) (*connect.Response[corev1.GetNodeInfoResponse], error) {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("core unavailable"))
	})
	ix := New(client, zap.NewNop())

	err := ix.initializeChainID(context.Background(), 1, 0)
	if err == nil {
		t.Fatal("InitializeChainID returned nil, want error")
	}
	if !strings.Contains(err.Error(), "get node info") {
		t.Fatalf("error = %q, want get node info context", err.Error())
	}
	if ix.ChainID != "" {
		t.Fatalf("ChainID = %q, want unset", ix.ChainID)
	}
}

func TestInitializeChainIDFailsWhenCoreReturnsEmptyChainID(t *testing.T) {
	client := startNodeInfoServer(t, func(context.Context, *connect.Request[corev1.GetNodeInfoRequest]) (*connect.Response[corev1.GetNodeInfoResponse], error) {
		return connect.NewResponse(&corev1.GetNodeInfoResponse{Chainid: "   "}), nil
	})
	ix := New(client, zap.NewNop())

	err := ix.initializeChainID(context.Background(), 1, 0)
	if err == nil {
		t.Fatal("InitializeChainID returned nil, want error")
	}
	if !strings.Contains(err.Error(), "empty chain ID") {
		t.Fatalf("error = %q, want empty chain ID context", err.Error())
	}
	if ix.ChainID != "" {
		t.Fatalf("ChainID = %q, want unset", ix.ChainID)
	}
}
