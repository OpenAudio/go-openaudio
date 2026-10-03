package common

import "github.com/ethereum/go-ethereum/crypto"

// Keccak256Concat returns the legacy Keccak-256 digest of the byte-for-byte
// concatenation of parts. It is the shared protocol helper for hashes whose
// encodings are defined as domain || field || field.
func Keccak256Concat(parts ...[]byte) [32]byte {
	state := crypto.NewKeccakState()
	for _, part := range parts {
		_, _ = state.Write(part)
	}
	var digest [32]byte
	_, _ = state.Read(digest[:])
	return digest
}
