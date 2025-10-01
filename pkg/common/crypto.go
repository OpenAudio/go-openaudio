// anything related to accounts like signing, wallets, and serialization
// used by config, server, and sdk
package common

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"strings"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/ethereum/go-ethereum/crypto"
)

func EthToCometKey(privateKey *ecdsa.PrivateKey) (*ed25519.PrivKey, error) {
	hash := sha256.Sum256(privateKey.D.Bytes())
	eckey := ed25519.GenPrivKeyFromSecret(hash[:])
	return &eckey, nil
}

func EthToEthKey(privKey string) (*ecdsa.PrivateKey, error) {
	privateKey, err := crypto.HexToECDSA(privKey)
	if err != nil {
		return nil, fmt.Errorf("could not convert string to privkey: %v", err)
	}
	if privateKey.Curve != crypto.S256() {
		return nil, fmt.Errorf("private key is not secp256k1 curve")
	}
	return privateKey, nil
}

func EthSign(pkey *ecdsa.PrivateKey, data []byte) (string, error) {
	dataHash := sha256.Sum256(data)

	signature, err := crypto.Sign(dataHash[:], pkey)
	if err != nil {
		return "", fmt.Errorf("failed to signed data: %v", err)
	}

	signatureStr := hex.EncodeToString(signature)
	return signatureStr, nil
}

func EthRecover(signatureStr string, data []byte) (*ecdsa.PublicKey, string, error) {
	signature, err := hex.DecodeString(signatureStr)
	if err != nil {
		return nil, "", fmt.Errorf("could not decode signature: %v", err)
	}

	dataHash := sha256.Sum256(data)

	pubKey, err := crypto.SigToPub(dataHash[:], signature)
	if err != nil {
		return nil, "", fmt.Errorf("could not recover pubkey: %v", err)
	}

	address := crypto.PubkeyToAddress(*pubKey).Hex()

	return pubKey, address, nil
}

func SerializePublicKeyHex(pubKey *ecdsa.PublicKey) string {
	bytes := crypto.CompressPubkey(pubKey)
	return hex.EncodeToString(bytes)
}

func PrivKeyToAddress(privateKey *ecdsa.PrivateKey) string {
	publicKey := privateKey.Public().(*ecdsa.PublicKey)
	address := crypto.PubkeyToAddress(*publicKey).Hex()
	return address
}

// for parity with the web3.js web3.utils.utf8ToHex() call
func Utf8ToHex(s string) [32]byte {
	hex := [32]byte{}
	copy(hex[:], s)
	return hex
}

// reverse of Utf8ToHex
func HexToUtf8(hex [32]byte) string {
	var end int
	for end = range hex {
		if hex[end] == 0 {
			break
		}
	}
	return string(hex[:end])
}

func LoadPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	keyBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read private key file: %w", err)
	}
	key, err := crypto.HexToECDSA(string(keyBytes))
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	return key, nil
}

// TxHashSign signs a transaction hash with the given private key
// Validates that txHash is a proper hex-encoded hash
func TxHashSign(pkey *ecdsa.PrivateKey, txHash string) (string, error) {
	// Validate txHash format (should be hex string, typically 64 characters)
	if !strings.HasPrefix(txHash, "0x") && len(txHash) != 64 {
		return "", fmt.Errorf("invalid txHash format: expected 64-character hex string")
	}

	// Remove 0x prefix if present
	cleanHash := strings.TrimPrefix(txHash, "0x")

	// Validate hex encoding
	hashBytes, err := hex.DecodeString(cleanHash)
	if err != nil {
		return "", fmt.Errorf("invalid txHash: not valid hex: %w", err)
	}

	if len(hashBytes) != 32 {
		return "", fmt.Errorf("invalid txHash: expected 32 bytes, got %d", len(hashBytes))
	}

	return EthSign(pkey, hashBytes)
}

// TxHashRecover recovers the signer address from a transaction hash and signature
func TxHashRecover(txHash, signature string) (*ecdsa.PublicKey, string, error) {
	// Validate txHash format
	if !strings.HasPrefix(txHash, "0x") && len(txHash) != 64 {
		return nil, "", fmt.Errorf("invalid txHash format: expected 64-character hex string")
	}

	// Remove 0x prefix if present
	cleanHash := strings.TrimPrefix(txHash, "0x")

	// Validate hex encoding
	hashBytes, err := hex.DecodeString(cleanHash)
	if err != nil {
		return nil, "", fmt.Errorf("invalid txHash: not valid hex: %w", err)
	}

	if len(hashBytes) != 32 {
		return nil, "", fmt.Errorf("invalid txHash: expected 32 bytes, got %d", len(hashBytes))
	}

	return EthRecover(signature, hashBytes)
}
