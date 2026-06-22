package etl

import (
	"context"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"go.uber.org/zap"
)

// blockSource produces blocks for the indexer to process. Both the polling
// prefetcher and the gRPC streamSource satisfy it, so indexBlocks can switch
// between them without any other change.
type blockSource interface {
	run(ctx context.Context, startHeight int64)
	C() <-chan prefetchedBlock
}

// streamSource consumes blocks from CoreService.StreamBlocks (a gRPC server
// stream) instead of polling GetBlocks. The server replays history from
// startHeight then live-tails, so this is a single push connection rather than
// a height-incrementing poll. On stream error it reconnects from the last block
// received; if the server doesn't support streaming catch-up it falls back to
// the polling prefetcher.
type streamSource struct {
	client corev1connect.CoreServiceClient // must be built with connect.WithGRPC()
	logger *zap.Logger
	ch     chan prefetchedBlock
	bufSz  int
}

func newStreamSource(client corev1connect.CoreServiceClient, logger *zap.Logger) *streamSource {
	return &streamSource{
		client: client,
		logger: logger,
		ch:     make(chan prefetchedBlock, defaultPrefetchBuffer),
		bufSz:  defaultPrefetchBuffer,
	}
}

// C returns the channel to read streamed blocks from.
func (s *streamSource) C() <-chan prefetchedBlock { return s.ch }

// run opens the block stream from startHeight and forwards blocks to the
// channel, reconnecting from the last height received on any error. It blocks
// until ctx is cancelled. Callers read from C().
func (s *streamSource) run(ctx context.Context, startHeight int64) {
	defer close(s.ch)

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

		stream, err := s.client.StreamBlocks(ctx, connect.NewRequest(&corev1.StreamBlocksRequest{
			StartHeight: height,
			Canon:       true,
		}))
		if err != nil {
			if s.fellBackToPolling(ctx, err, height) {
				return
			}
			s.logger.Debug("StreamBlocks open failed, will retry", zap.Error(err))
			backoff = increaseBackoff(backoff)
			continue
		}

		for stream.Receive() {
			b := stream.Msg().Block
			if b == nil {
				continue
			}
			// The stream keeps us at the head, so blocks_behind is effectively 0;
			// CurrentHeight mirrors the block height for the progress log.
			select {
			case <-ctx.Done():
				_ = stream.Close()
				return
			case s.ch <- prefetchedBlock{Block: b, CurrentHeight: b.Height}:
			}
			height = b.Height + 1
			backoff = 0
		}

		recvErr := stream.Err()
		_ = stream.Close()
		if recvErr != nil {
			if s.fellBackToPolling(ctx, recvErr, height) {
				return
			}
			s.logger.Warn("block stream error, reconnecting",
				zap.Int64("resume_height", height), zap.Error(recvErr))
		} else {
			s.logger.Debug("block stream closed, reconnecting", zap.Int64("resume_height", height))
		}
		backoff = increaseBackoff(backoff)
	}
}

// fellBackToPolling switches to the polling prefetcher (writing into the same
// channel) when the endpoint doesn't support StreamBlocks, e.g. an older node.
// Returns true if it took over (caller should return).
func (s *streamSource) fellBackToPolling(ctx context.Context, err error, height int64) bool {
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		return false
	}
	s.logger.Warn("StreamBlocks not supported by endpoint, falling back to polling",
		zap.Int64("resume_height", height), zap.Error(err))
	newPrefetcher(s.client, s.logger).runInto(ctx, height, s.ch)
	return true
}
