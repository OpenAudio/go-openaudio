package main

import (
	"context"
	"log"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
)

func main() {
	oap := sdk.NewOpenAudioSDK("node1.oap.devnet")

	ctx := context.Background()

	stream, err := oap.Core.StreamBlocks(ctx, connect.NewRequest(&v1.StreamBlocksRequest{
		Canon: true,
	}))
	if err != nil {
		log.Fatal(err)
	}

	for {
		received := stream.Receive()

		if !received {
			log.Print("stream closed")
			return
		}

		block := stream.Msg().Block
		log.Printf("block: %d %s", block.Height, block.Hash)
	}
}
