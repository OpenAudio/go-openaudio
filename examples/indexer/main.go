package main

import (
	"context"
	"log"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
)

func main() {
	sdk := sdk.NewAudiusdSDK("node1.audiusd.devnet")

	height := int64(1)
	for {
		block, err := sdk.Core.GetBlock(context.Background(), connect.NewRequest(&v1.GetBlockRequest{
			Height: height,
		}))
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("block: %+v", block)

		height++
	}
}
