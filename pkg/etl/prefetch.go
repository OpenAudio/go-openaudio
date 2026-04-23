package etl

import (
	"context"
	"sort"
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
	core   corev1connect.CoreServiceClient
	logger *zap.Logger
	ch     chan prefetchedBlock
	bufSz  int
}

const defaultPrefetchBuffer = 50

// batchSize is how many blocks to request per GetBlocks RPC call.
const batchSize = 50

func newPrefetcher(core corev1connect.CoreServiceClient, logger *zap.Logger) *prefetcher {
	return &prefetcher{
		core:   core,
		logger: logger,
		bufSz:  defaultPrefetchBuffer,
		ch:     make(chan prefetchedBlock, defaultPrefetchBuffer),
	}
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

		// Build a batch of heights to fetch.
		heights := make([]int64, batchSize)
		for i := range heights {
			heights[i] = height + int64(i)
		}

		resp, err := p.core.GetBlocks(ctx, connect.NewRequest(&corev1.GetBlocksRequest{
			Height: heights,
		}))
		if err != nil {
			// Batch not available — fall back to single-block fetch for the first height.
			singleResp, singleErr := p.core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{
				Height: height,
			}))
			if singleErr != nil {
				if backoff == 0 {
					backoff = 200 * time.Millisecond
				} else if backoff < 2*time.Second {
					backoff *= 2
				}
				continue
			}
			if singleResp.Msg.Block == nil || singleResp.Msg.Block.Height < 0 {
				if backoff == 0 {
					backoff = 200 * time.Millisecond
				} else if backoff < 2*time.Second {
					backoff *= 2
				}
				continue
			}
			backoff = 0
			select {
			case <-ctx.Done():
				return
			case p.ch <- prefetchedBlock{
				Block:         singleResp.Msg.Block,
				CurrentHeight: singleResp.Msg.CurrentHeight,
			}:
			}
			height++
			continue
		}

		blocks := resp.Msg.Blocks
		if len(blocks) == 0 {
			if backoff == 0 {
				backoff = 200 * time.Millisecond
			} else if backoff < 2*time.Second {
				backoff *= 2
			}
			continue
		}

		// Reset backoff on success.
		backoff = 0

		// Sort heights so we send blocks in order.
		sortedHeights := make([]int64, 0, len(blocks))
		for h := range blocks {
			sortedHeights = append(sortedHeights, h)
		}
		sort.Slice(sortedHeights, func(i, j int) bool { return sortedHeights[i] < sortedHeights[j] })

		for _, h := range sortedHeights {
			b := blocks[h]
			if b == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case p.ch <- prefetchedBlock{
				Block:         b,
				CurrentHeight: resp.Msg.CurrentHeight,
			}:
			}
		}

		// Advance height past the last block we got.
		height = sortedHeights[len(sortedHeights)-1] + 1
	}
}

// C returns the channel to read prefetched blocks from.
func (p *prefetcher) C() <-chan prefetchedBlock {
	return p.ch
}
