package server

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// StreamBlocks implements v1connect.CoreServiceHandler.
func (c *CoreService) StreamBlocks(context.Context, *connect.Request[v1.StreamBlocksRequest], *connect.ServerStream[v1.StreamBlocksResponse]) error {
	panic("unimplemented")
}
