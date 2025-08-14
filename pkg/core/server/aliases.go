// simple type aliases that make readability, imports, and auto complete easier
package server

import (
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

type EthAddress = string
type CometBFTAddress = string

type CometBFTRPC = rpchttp.HTTP
