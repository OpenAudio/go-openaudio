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
	core   corev1connect.CoreServiceClient
	logger *zap.Logger
	ch     chan prefetchedBlock
	bufSz  int
}

const defaultPrefetchBuffer = 50

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

		resp, err := p.core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{
			Height: height,
		}))
		if err != nil {
			// Block not yet available — wait briefly and retry.
			if backoff == 0 {
				backoff = 200 * time.Millisecond
			} else if backoff < 2*time.Second {
				backoff *= 2
			}
			continue
		}

		if resp.Msg.Block == nil || resp.Msg.Block.Height < 0 {
			if backoff == 0 {
				backoff = 200 * time.Millisecond
			} else if backoff < 2*time.Second {
				backoff *= 2
			}
			continue
		}

		// Reset backoff on success.
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
