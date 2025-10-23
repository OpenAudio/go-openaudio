package rewards

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

type RewardClaim struct {
	RecipientEthAddress string
	Amount              uint64
	RewardID            string
	RewardAddress       string // Optional - for programmatic rewards
	Specifier           string
	ClaimAuthority      string
}

func (claim RewardClaim) Compile() ([]byte, error) {
	// Combine the ID + Specifier to get the disbursement ID
	// For programmatic rewards, include reward address to prevent cross-reward attacks
	var combinedID string
	if claim.RewardAddress != "" {
		combinedID = fmt.Sprintf("%s:%s:%s", claim.RewardAddress, claim.RewardID, claim.Specifier)
	} else {
		combinedID = fmt.Sprintf("%s:%s", claim.RewardID, claim.Specifier)
	}
	combinedIDBytes := []byte(combinedID)

	// Encode the claim amount as wAUDIO Wei
	encodedAmount := claim.Amount * 1e8
	amountBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(amountBytes, uint64(encodedAmount))

	// Decode the user's wallet eth address
	userBytes, err := hex.DecodeString(strings.TrimPrefix(claim.RecipientEthAddress, "0x"))
	if err != nil {
		return nil, fmt.Errorf("failed to decode user wallet: %w", err)
	}

	items := [][]byte{userBytes, amountBytes, combinedIDBytes}

	// antiAbuseOracleEthAddress is not required for oracle attestations
	if claim.ClaimAuthority != "" {
		oracleBytes, err := hex.DecodeString(strings.TrimPrefix(claim.ClaimAuthority, "0x"))
		if err != nil {
			return nil, fmt.Errorf("failed to decode oracle address: %w", err)
		}
		items = append(items, oracleBytes)
	}

	attestationBytes := bytes.Join(items, []byte("_"))

	return attestationBytes, nil
}

// SignClaim is a utility function that compiles and signs a reward claim.
// This is used by claim authorities to create signatures for reward claims.
func SignClaim(claim RewardClaim, privateKey *ecdsa.PrivateKey) (string, error) {
	// Compile the claim data
	claimData, err := claim.Compile()
	if err != nil {
		return "", fmt.Errorf("failed to compile claim: %w", err)
	}

	// Hash the compiled data
	hash := crypto.Keccak256(claimData)

	// Sign the hash
	signatureBytes, err := crypto.Sign(hash, privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign claim: %w", err)
	}

	// Return as hex string with 0x prefix
	return "0x" + hex.EncodeToString(signatureBytes), nil
}

// VerifyClaim is a utility function that verifies a reward claim signature.
// It returns the signer's address if the signature is valid.
func VerifyClaim(claim RewardClaim, signature string) (string, error) {
	// Compile the claim data
	claimData, err := claim.Compile()
	if err != nil {
		return "", fmt.Errorf("failed to compile claim: %w", err)
	}

	// Remove 0x prefix if present
	sigHex := strings.TrimPrefix(signature, "0x")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", fmt.Errorf("failed to decode signature: %w", err)
	}

	// Hash the compiled data
	hash := crypto.Keccak256(claimData)

	// Recover the public key from the signature
	pubKey, err := crypto.SigToPub(hash, sigBytes)
	if err != nil {
		return "", fmt.Errorf("failed to recover public key: %w", err)
	}

	// Get the address from the public key
	address := crypto.PubkeyToAddress(*pubKey).String()

	return address, nil
}
