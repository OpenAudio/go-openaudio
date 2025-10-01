package common

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	gcrypto "github.com/ethereum/go-ethereum/crypto"
)

// CreateAddress deterministically generates a content address from a txhash and txindex.
// The returned address is the last 20 bytes of keccak256 hash, Ethereum-style.
// Using txhash instead of protobuf marshaling ensures stability across schema evolution.
// The txindex ensures uniqueness even if identical messages are submitted in the same block.
// The discriminator parameter can be used to create different addresses for sub-objects
// within the same transaction (e.g., "party:0", "resource:1", etc.)
func CreateAddress(txhash []byte, chainId string, blockHeight int64, txindex int64, discriminator string) string {
	// Compute inner content hash using the txhash directly
	contentHash := gcrypto.Keccak256(txhash)

	// Derive a deterministic salt from txindex, discriminator, and blockHeight
	saltInput := []byte(discriminator + ":" + chainId + ":" + string(rune(blockHeight)) + ":" + string(rune(txindex)))
	saltHash := sha256.Sum256(saltInput)

	// CREATE2-style preimage: 0xff || chainId || saltHash || contentHash
	preimage := append([]byte{0xff}, []byte(chainId)...)
	preimage = append(preimage, saltHash[:]...)
	preimage = append(preimage, contentHash...)

	// Final address hash
	addressBytes := gcrypto.Keccak256(preimage)

	// Return last 20 bytes as hex-encoded Ethereum-style address
	return "0x" + hex.EncodeToString(addressBytes[12:])
}

func HexToBytes(addr string) ([]byte, error) {
	// Remove "0x" prefix if present
	clean := strings.TrimPrefix(addr, "0x")
	return hex.DecodeString(clean)
}

func BytesToHex(bytes []byte) string {
	return "0x" + hex.EncodeToString(bytes)
}
