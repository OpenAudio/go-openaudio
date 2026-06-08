package server

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"google.golang.org/protobuf/proto"
)

// StreamBlocks implements v1connect.CoreServiceHandler.
//
// With StartHeight == 0 it streams only blocks committed after subscription
// (live-tail, the original behavior). With StartHeight > 0 it is indexer-grade:
// it first replays committed blocks from StartHeight to the chain head out of
// Postgres, then bridges to the live feed, gap-filling any heights the
// in-process pubsub dropped (pubsub.Publish is best-effort and drops to a slow
// subscriber). The result is an in-order, gap-free stream from StartHeight
// onward that a consumer can resume from its cursor on reconnect.
func (c *CoreService) StreamBlocks(ctx context.Context, req *connect.Request[v1.StreamBlocksRequest], stream *connect.ServerStream[v1.StreamBlocksResponse]) error {
	canon := req.Msg.Canon
	startHeight := req.Msg.StartHeight

	// Subscribe before reading the head so blocks committed during catch-up are
	// buffered (or, if dropped, recovered by gap-fill in the live loop).
	blockChan := c.core.blockPubsub.Subscribe(BlockPubsubTopic)
	defer c.core.blockPubsub.Unsubscribe(BlockPubsubTopic, blockChan)

	// sendLive forwards a block from the pubsub feed. The pubsub message is a
	// shared pointer, so clone before mutating transaction order.
	sendLive := func(b *v1.Block) error {
		block := proto.Clone(b).(*v1.Block)
		if !canon {
			// sorts transactions by entity manager priority, not how they ended up in the block
			block.Transactions = sortTransactionResponse(block.Transactions)
		}
		if err := stream.Send(&v1.StreamBlocksResponse{Block: block}); err != nil {
			return connect.NewError(connect.CodeAborted, fmt.Errorf("error sending block: %w", err))
		}
		return nil
	}

	// Live-only mode preserves the original behavior exactly.
	if startHeight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case b := <-blockChan:
				if err := sendLive(b); err != nil {
					return err
				}
			}
		}
	}

	// Indexer mode. emitHeight reads a single committed block from Postgres
	// (catch-up and gap-fill). GetBlock returns a fresh block already ordered
	// per canon, so no clone/re-sort is needed.
	emitHeight := func(h int64) error {
		block, err := c.core.GetBlock(ctx, h, canon)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("error reading block %d: %w", h, err))
		}
		if err := stream.Send(&v1.StreamBlocksResponse{Block: block}); err != nil {
			return connect.NewError(connect.CodeAborted, fmt.Errorf("error sending block %d: %w", h, err))
		}
		return nil
	}

	head := c.core.cache.currentHeight.Load()
	return runIndexerStream(ctx, startHeight, head, blockChan, emitHeight, sendLive)
}

// runIndexerStream is the testable core of StreamBlocks indexer mode: it replays
// [startHeight, head] via emitHeight (a Postgres read), then tails the live feed,
// gap-filling dropped heights via emitHeight and skipping duplicates, forwarding
// each live block via emitLive. It returns when ctx is cancelled or a callback
// errors.
func runIndexerStream(
	ctx context.Context,
	startHeight, head int64,
	live <-chan *v1.Block,
	emitHeight func(h int64) error,
	emitLive func(b *v1.Block) error,
) error {
	lastSent := startHeight - 1

	// Catch-up: replay [startHeight, head].
	for h := startHeight; h <= head; h++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emitHeight(h); err != nil {
			return err
		}
		lastSent = h
	}

	// Live tail with gap-fill and dedup.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b := <-live:
			if b.Height <= lastSent {
				continue // already sent during catch-up, or a duplicate
			}
			// Fill any heights the pubsub dropped between lastSent and this block.
			for h := lastSent + 1; h < b.Height; h++ {
				if err := emitHeight(h); err != nil {
					return err
				}
			}
			if err := emitLive(b); err != nil {
				return err
			}
			lastSent = b.Height
		}
	}
}
