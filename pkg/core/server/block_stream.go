package server

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"google.golang.org/protobuf/proto"
)

// StreamBlocks implements v1connect.CoreServiceHandler.
func (c *CoreService) StreamBlocks(ctx context.Context, req *connect.Request[v1.StreamBlocksRequest], stream *connect.ServerStream[v1.StreamBlocksResponse]) error {
	canon := req.Msg.Canon

	blockChan := c.core.blockPubsub.Subscribe(BlockPubsubTopic)
	defer c.core.blockPubsub.Unsubscribe(BlockPubsubTopic, blockChan)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case b := <-blockChan:
			block := proto.Clone(b).(*v1.Block)
			if !canon {
				// sorts transactions by entity manager priority, not how they ended up in the block
				block.Transactions = sortTransactionResponse(block.Transactions)
			}
			err := stream.Send(&v1.StreamBlocksResponse{Block: block})
			if err != nil {
				return connect.NewError(connect.CodeAborted, fmt.Errorf("error sending block: %w", err))
			}
		}
	}
}
