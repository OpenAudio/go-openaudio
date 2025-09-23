package common

import (
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/types"
)

type TxHash = string

// ToTxHashFromBytes creates a transaction hash from raw transaction bytes
// using CometBFT's hashing utilities for consistency with block sync
func ToTxHashFromBytes(txBytes []byte) TxHash {
	tx := types.Tx(txBytes)
	hash := tx.Hash()
	return bytes.HexBytes(hash).String()
}
