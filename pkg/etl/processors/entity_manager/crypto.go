package entity_manager

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

// recoverETHAddress recovers the Ethereum address that signed a personal_sign message.
// The signature should be hex-encoded (with or without 0x prefix), 65 bytes (r, s, v).
func recoverETHAddress(message, signatureHex string) (string, error) {
	signatureHex = strings.TrimPrefix(signatureHex, "0x")
	sigBytes, err := hex.DecodeString(signatureHex)
	if err != nil {
		return "", fmt.Errorf("invalid signature hex: %w", err)
	}
	if len(sigBytes) != 65 {
		return "", fmt.Errorf("signature must be 65 bytes, got %d", len(sigBytes))
	}

	// Adjust v value: MetaMask uses 27/28, go-ethereum expects 0/1
	if sigBytes[64] >= 27 {
		sigBytes[64] -= 27
	}

	hash := accounts.TextHash([]byte(message))
	pubKey, err := crypto.SigToPub(hash, sigBytes)
	if err != nil {
		return "", fmt.Errorf("ecrecover failed: %w", err)
	}

	return crypto.PubkeyToAddress(*pubKey).Hex(), nil
}
