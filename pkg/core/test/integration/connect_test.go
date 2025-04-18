package integration_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	systemv1 "github.com/AudiusProject/audiusd/pkg/api/system/v1"
	systemv1connect "github.com/AudiusProject/audiusd/pkg/api/system/v1/v1connect"
)

func TestConnectRPC(t *testing.T) {
	t.Run("should return a health response", func(t *testing.T) {
		ctx := context.Background()

		systemClient := systemv1connect.NewSystemServiceClient(http.DefaultClient, "http://audiusd-1")
		res, err := systemClient.GetHealth(ctx, connect.NewRequest(&systemv1.GetHealthRequest{}))
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if res.Msg.Status != "up" {
			t.Errorf("expected status 'up', got %q", res.Msg.Status)
		}
	})
}
