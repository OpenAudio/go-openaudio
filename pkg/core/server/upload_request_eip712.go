package server

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// EIP-712 typed data for the signature a client presents when starting an
// upload, proving which wallet is asking before any content exists to name.
//
// Typed data rather than hashed JSON: the type definition is the encoding, so
// there is no canonicalization for client and server to agree on and drift
// over, and the domain stops a signature made for another purpose over the same
// two fields being replayed here. It also matches how the codebase already
// signs entity-manager writes (eip712.go), so both sides use standard calls.

const (
	uploadRequestDomainName    = "Audius Upload"
	uploadRequestDomainVersion = "1"
	uploadRequestPrimaryType   = "UploadRequest"
)

// uploadRequestTypedData builds the typed data for an upload request.
//
// Domain is name and version only: verifyingContract because uploads are not a
// contract interaction, chainId so storage nodes need no chain config to
// verify. The domain name already separates these from every other Audius
// signature, and the freshness window bounds cross-network replay to nothing.
func uploadRequestTypedData(userID, timestamp int64) apitypes.TypedData {
	return apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
			},
			uploadRequestPrimaryType: []apitypes.Type{
				{Name: "userId", Type: "uint256"},
				{Name: "timestamp", Type: "uint256"},
			},
		},
		PrimaryType: uploadRequestPrimaryType,
		Domain: apitypes.TypedDataDomain{
			Name:    uploadRequestDomainName,
			Version: uploadRequestDomainVersion,
		},
		Message: apitypes.TypedDataMessage{
			"userId":    math.NewHexOrDecimal256(userID),
			"timestamp": math.NewHexOrDecimal256(timestamp),
		},
	}
}

// UploadRequestHash returns the digest a client signs to start an upload,
// exported so storage nodes and tests derive it from one place.
func UploadRequestHash(userID, timestamp int64) ([]byte, error) {
	typedData := uploadRequestTypedData(userID, timestamp)

	domainSeparator, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return nil, fmt.Errorf("eip712domain hash struct: %w", err)
	}
	structHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return nil, fmt.Errorf("primary type hash struct: %w", err)
	}

	raw := []byte(fmt.Sprintf("\x19\x01%s%s", string(domainSeparator), string(structHash)))
	return crypto.Keccak256(raw), nil
}

// RecoverUploadRequestSigner returns the wallet that signed an upload request.
// Accepts hex with or without 0x, and either 0/1 or 27/28 recovery ids.
func RecoverUploadRequestSigner(userID, timestamp int64, signature string) (string, error) {
	digest, err := UploadRequestHash(userID, timestamp)
	if err != nil {
		return "", err
	}

	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil {
		return "", fmt.Errorf("invalid signature hex: %w", err)
	}
	if len(sigBytes) != 65 {
		return "", fmt.Errorf("invalid signature length: %d", len(sigBytes))
	}
	// Wallets emit v as 27/28; secp256k1 recovery wants 0/1.
	if sigBytes[64] >= 27 {
		sigBytes[64] -= 27
	}

	pubkey, err := crypto.SigToPub(digest, sigBytes)
	if err != nil {
		return "", fmt.Errorf("could not recover pubkey: %w", err)
	}
	return crypto.PubkeyToAddress(*pubkey).Hex(), nil
}
