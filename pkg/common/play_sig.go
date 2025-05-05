package common

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"

	storagev1 "github.com/AudiusProject/audiusd/pkg/api/storage/v1"
)

func GeneratePlaySignature(privKey *ecdsa.PrivateKey, data *storagev1.StreamTrackSignatureData) (signatureHex string, dataHash []byte, err error) {
	// Marshal protobuf to bytes
	protoBytes, err := proto.Marshal(data)
	if err != nil {
		return "", nil, err
	}

	// Use Ethereum's text-prefixed hash to be compatible with eth_sign / ecrecover
	dataHash = accounts.TextHash(protoBytes) // already includes keccak256 and the Ethereum prefix

	// Sign the hash
	sigBytes, err := crypto.Sign(dataHash, privKey)
	if err != nil {
		return "", nil, err
	}

	// Encode to hex for return (you could also return raw bytes if preferred)
	signatureHex = hex.EncodeToString(sigBytes)

	return signatureHex, dataHash, nil
}

func RecoverPlaySignature(signatureHex string, data *storagev1.StreamTrackSignatureData) (pubKey *ecdsa.PublicKey, ethAddress string, err error) {
	// Decode signature
	sigBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return nil, "", fmt.Errorf("invalid signature hex: %w", err)
	}
	if len(sigBytes) != 65 {
		return nil, "", errors.New("signature must be 65 bytes")
	}

	// Marshal protobuf to bytes
	protoBytes, err := proto.Marshal(data)
	if err != nil {
		return nil, "", fmt.Errorf("failed to marshal proto: %w", err)
	}

	// Ethereum message hash (with prefix)
	dataHash := accounts.TextHash(protoBytes)

	// Recover public key
	pubKey, err = crypto.SigToPub(dataHash, sigBytes)
	if err != nil {
		return nil, "", fmt.Errorf("failed to recover pubkey: %w", err)
	}

	// Derive Ethereum address
	ethAddr := crypto.PubkeyToAddress(*pubKey)
	ethAddress = ethAddr.Hex()

	return pubKey, ethAddress, nil
}
