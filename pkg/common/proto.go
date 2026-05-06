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

// ProtoSign deterministically marshals body and signs the bytes with privKey.
// Callers pass the body (RewardBody, RewardPoolBody, etc.) — the body never
// contains its own signature, so there's no chicken-and-egg. The signature
// goes alongside the body in its envelope (RewardMessage, RewardPoolMessage)
// at transport time.
//
// Forward-compat: proto3 omits default-valued fields, so additive-only schema
// changes (new optional fields) don't shift the bytes for old signers that
// leave them unset. Existing signatures continue to verify as long as no
// caller removes / renames / changes-type-of an existing field.
//
// Cross-action replay protection: two action types share an envelope via a
// `oneof`, whose field tag is part of the marshaled bytes. Two actions with
// otherwise-identical inner shapes produce different signed bytes.
func ProtoSign(privKey *ecdsa.PrivateKey, body proto.Message) (string, error) {
	b, err := signableBytes(body)
	if err != nil {
		return "", fmt.Errorf("marshal for signing: %w", err)
	}
	return EthSign(privKey, b)
}

// ProtoRecover re-marshals body and recovers the eth address that produced
// signature. Returns the recovered address; the caller is responsible for
// any membership / authorization check.
func ProtoRecover(body proto.Message, signature string) (string, error) {
	b, err := signableBytes(body)
	if err != nil {
		return "", fmt.Errorf("marshal for recovery: %w", err)
	}
	_, address, err := EthRecover(signature, b)
	return address, err
}

func signableBytes(body proto.Message) ([]byte, error) {
	opts := proto.MarshalOptions{Deterministic: true}
	return opts.Marshal(body)
}
