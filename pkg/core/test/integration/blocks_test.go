package integration_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/core/test/integration/utils"
)

func TestBlockCreation(t *testing.T) {
	ctx := context.Background()
	sdk := utils.DiscoveryOne

	_, err := sdk.Core.Ping(ctx, connect.NewRequest(&corev1.PingRequest{}))
	assert.NoError(t, err)

	timeout := time.After(30 * time.Second)
	for {
		select {
		case <-timeout:
			assert.Fail(t, "timed out waiting for discovery node to be ready")
		default:
		}
		status, err := sdk.Core.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
		assert.NoError(t, err)
		if status.Msg.Ready {
			break
		}
		time.Sleep(2 * time.Second)
	}

	var blockOne *corev1.Block
	var blockTwo *corev1.Block
	var blockThree *corev1.Block

	// index first three blocks
	// we return a success response with -1 if block does not exist
	for {
		blockOneRes, err := sdk.Core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{Height: 1}))
		assert.NoError(t, err)
		if blockOneRes.Msg.Block != nil {
			blockOne = blockOneRes.Msg.Block
		}

		blockTwoRes, err := sdk.Core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{Height: 2}))
		assert.NoError(t, err)
		if blockTwoRes.Msg.Block != nil {
			blockTwo = blockTwoRes.Msg.Block
		}

		blockThreeRes, err := sdk.Core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{Height: 3}))
		assert.NoError(t, err)
		if blockThreeRes.Msg.Block != nil {
			blockThree = blockThreeRes.Msg.Block
		}

		if blockOne != nil && blockTwo != nil && blockThree != nil {
			break
		}
	}

	assert.Equal(t, int64(1), blockOne.Height)
	assert.Equal(t, blockOne.ChainId, blockTwo.ChainId)
	assert.Equal(t, int64(2), blockTwo.Height)
	assert.Equal(t, blockOne.ChainId, blockThree.ChainId)
	assert.Equal(t, int64(3), blockThree.Height)
}
