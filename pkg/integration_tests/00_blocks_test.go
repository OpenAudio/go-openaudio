package integration_tests

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/integration_tests/utils"
)

func TestBlockCreation(t *testing.T) {
	ctx := context.Background()
	sdk := utils.DiscoveryOne

	_, err := sdk.Core.Ping(ctx, connect.NewRequest(&corev1.PingRequest{}))
	assert.NoError(t, err)

	err = utils.WaitForDevnetHealthy(30 * time.Second)
	assert.NoError(t, err)

	var blockOne *corev1.Block
	var blockTwo *corev1.Block
	var blockThree *corev1.Block

	// index first three blocks
	// we return a success response with -1 if block does not exist
	blockTimeout := 30 * time.Second
	blockCtx, blockCancel := context.WithTimeout(ctx, blockTimeout)
	defer blockCancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	blocksReady := false
blockLoop:
	for {
		select {
		case <-blockCtx.Done():
			assert.Fail(t, "timed out waiting for blocks 1, 2, and 3 to be created")
			return
		case <-ticker.C:
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
				blocksReady = true
				break blockLoop
			}
		}
	}

	if !blocksReady {
		return
	}

	assert.Equal(t, int64(1), blockOne.Height)
	assert.Equal(t, blockOne.ChainId, blockTwo.ChainId)
	assert.Equal(t, int64(2), blockTwo.Height)
	assert.Equal(t, blockOne.ChainId, blockThree.ChainId)
	assert.Equal(t, int64(3), blockThree.Height)
}
