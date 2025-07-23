package integration_tests

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	systemv1 "github.com/AudiusProject/audiusd/pkg/api/system/v1"
	connectv1system "github.com/AudiusProject/audiusd/pkg/api/system/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/integration_tests/utils"
)

func TestConnectRPC(t *testing.T) {
	t.Run("should return a health response", func(t *testing.T) {
		ctx := context.Background()

		sdk := utils.ContentThree
		res, err := sdk.System.GetHealth(ctx, connect.NewRequest(&systemv1.GetHealthRequest{}))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if res.Msg.Status != "up" {
			t.Errorf("expected status 'up', got %q", res.Msg.Status)
		}
	})
}

func TestGRPC(t *testing.T) {
	t.Run("should return a health response", func(t *testing.T) {
		ctx := context.Background()

		endpoint := utils.EnsureProtocol(utils.DiscoveryOneRPC)
		client := connectv1system.NewSystemServiceClient(http.DefaultClient, endpoint, connect.WithGRPC())
		res, err := client.GetHealth(ctx, connect.NewRequest(&systemv1.GetHealthRequest{}))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if res.Msg.Status != "up" {
			t.Errorf("expected status 'up', got %q", res.Msg.Status)
		}

		endpoint = utils.EnsureProtocol(utils.DiscoveryOneRPC) + ":50051"
		client = connectv1system.NewSystemServiceClient(http.DefaultClient, endpoint, connect.WithGRPC())
		res, err = client.GetHealth(ctx, connect.NewRequest(&systemv1.GetHealthRequest{}))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if res.Msg.Status != "up" {
			t.Errorf("expected status 'up', got %q", res.Msg.Status)
		}
	})
}

func TestConnectGRPCWeb(t *testing.T) {
	t.Run("should return a health response", func(t *testing.T) {
		ctx := context.Background()

		endpoint := utils.EnsureProtocol(utils.DiscoveryOneRPC)
		client := connectv1system.NewSystemServiceClient(http.DefaultClient, endpoint, connect.WithGRPCWeb())
		res, err := client.GetHealth(ctx, connect.NewRequest(&systemv1.GetHealthRequest{}))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if res.Msg.Status != "up" {
			t.Errorf("expected status 'up', got %q", res.Msg.Status)
		}
	})
}
