package etl

import (
	"context"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"go.uber.org/zap"
)

// prefetchedBlock holds a fetched block ready for processing.
type prefetchedBlock struct {
	Block         *corev1.Block
	CurrentHeight int64
}

// prefetcher fetches blocks ahead of the indexer, buffering them in a channel
// so RPC latency and DB processing overlap.
type prefetcher struct {
	core         corev1connect.CoreServiceClient
	logger       *zap.Logger
	ch           chan prefetchedBlock
	bufSz        int
	batchEnabled bool
}

const defaultPrefetchBuffer = 50

// batchSize is how many blocks to request per GetBlocks RPC call.
const batchSize = 50

const maxBackoff = 2 * time.Second

func newPrefetcher(core corev1connect.CoreServiceClient, logger *zap.Logger) *prefetcher {
	return &prefetcher{
		core:         core,
		logger:       logger,
		bufSz:        defaultPrefetchBuffer,
		ch:           make(chan prefetchedBlock, defaultPrefetchBuffer),
		batchEnabled: true,
	}
}

func increaseBackoff(current time.Duration) time.Duration {
	if current == 0 {
		return 200 * time.Millisecond
	}
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

// run fetches blocks starting from startHeight and sends them to the channel.
// It blocks until ctx is cancelled. Callers read from C().
func (p *prefetcher) run(ctx context.Context, startHeight int64) {
	defer close(p.ch)

	height := startHeight
	backoff := time.Duration(0)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}

		if p.batchEnabled {
			// Build a batch of heights to fetch.
			heights := make([]int64, batchSize)
			for i := range heights {
				heights[i] = height + int64(i)
			}

			resp, err := p.core.GetBlocks(ctx, connect.NewRequest(&corev1.GetBlocksRequest{
				Height: heights,
			}))
			if err != nil {
				// Detect permanent failures and disable batching for the rest of the run.
				if code := connect.CodeOf(err); code == connect.CodeUnimplemented || code == connect.CodeInvalidArgument {
					p.logger.Warn("GetBlocks not supported by endpoint, disabling batch mode", zap.Error(err))
					p.batchEnabled = false
				} else {
					p.logger.Debug("GetBlocks failed, falling back to single-block fetch", zap.Error(err))
				}
				// Fall through to single-block fetch below.
			} else {
				blocks := resp.Msg.Blocks
				if len(blocks) == 0 {
					backoff = increaseBackoff(backoff)
					continue
				}

				// Emit only a contiguous run starting from the requested height.
				// Stop at the first gap to avoid skipping blocks.
				emitted := 0
				for _, h := range heights {
					b, ok := blocks[h]
					if !ok || b == nil {
						break
					}
					select {
					case <-ctx.Done():
						return
					case p.ch <- prefetchedBlock{
						Block:         b,
						CurrentHeight: resp.Msg.CurrentHeight,
					}:
					}
					height = h + 1
					emitted++
				}

				if emitted > 0 {
					backoff = 0
					continue
				}
				// Batch returned data but not for our height — fall through to single fetch.
			}
		}

		// Single-block fetch.
		resp, err := p.core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{
			Height: height,
		}))
		if err != nil {
			backoff = increaseBackoff(backoff)
			continue
		}
		if resp.Msg.Block == nil || resp.Msg.Block.Height < 0 {
			backoff = increaseBackoff(backoff)
			continue
		}

		backoff = 0
		select {
		case <-ctx.Done():
			return
		case p.ch <- prefetchedBlock{
			Block:         resp.Msg.Block,
			CurrentHeight: resp.Msg.CurrentHeight,
		}:
		}
		height++
	}
}

// C returns the channel to read prefetched blocks from.
func (p *prefetcher) C() <-chan prefetchedBlock {
	return p.ch
}
