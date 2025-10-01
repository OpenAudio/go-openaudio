package common

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/types"
	"google.golang.org/protobuf/proto"
)

type TxHash = string

// ToTxHashFromBytes creates a transaction hash from raw transaction bytes
// using CometBFT's hashing utilities for consistency with block sync
func ToTxHashFromBytes(txBytes []byte) TxHash {
	tx := types.Tx(txBytes)
	hash := tx.Hash()
	return bytes.HexBytes(hash).String()
}

// ProtoTxSign signs a protobuf message for transaction purposes
// WARNING: This should only be used when sending transactions and not replaying them
// as it's not safe from protobuf evolution. The message structure could change
// between versions, making signatures invalid.
func ProtoSign(pkey *ecdsa.PrivateKey, msg proto.Message) (string, error) {
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protobuf message: %w", err)
	}

	return EthSign(pkey, msgBytes)
}

// ProtoTxRecover recovers the signer address from a protobuf message and signature
// WARNING: This should only be used when verifying transactions and not replaying them
// as it's not safe from protobuf evolution. The message structure could change
// between versions, making signatures invalid.
func ProtoRecover(msg proto.Message, signature string) (string, error) {
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal protobuf message: %w", err)
	}

	_, address, err := EthRecover(signature, msgBytes)
	return address, err
}
