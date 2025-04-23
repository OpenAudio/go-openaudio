package integration_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	systemv1 "github.com/AudiusProject/audiusd/pkg/api/system/v1"
	"github.com/AudiusProject/audiusd/pkg/core/test/integration/utils"
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
