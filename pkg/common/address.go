package common

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	gcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

// CreateAddress deterministically generates a content address for a Protobuf message.
// The returned address is the last 20 bytes of keccak256 hash, Ethereum-style.
func CreateAddress(msg proto.Message, chainId string, blockHeight int64, salt string) string {
	// Serialize the Protobuf message
	msgBytes, err := proto.Marshal(msg)
	if err != nil {
		panic("failed to marshal protobuf message: " + err.Error())
	}

	// Compute inner content hash (analogous to init_code hash in CREATE2)
	contentHash := gcrypto.Keccak256(msgBytes)

	// Derive a deterministic salt from the provided salt and blockHeight (optional)
	saltInput := []byte(salt + ":" + chainId + ":" + string(rune(blockHeight)))
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
