package rewards

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

type RewardClaim struct {
	RecipientEthAddress       string
	Amount                    uint64
	RewardID                  string
	RewardAddress             string // Optional - for programmatic rewards
	Specifier                 string
	AntiAbuseOracleEthAddress string
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
	if claim.AntiAbuseOracleEthAddress != "" {
		oracleBytes, err := hex.DecodeString(strings.TrimPrefix(claim.AntiAbuseOracleEthAddress, "0x"))
		if err != nil {
			return nil, fmt.Errorf("failed to decode oracle address: %w", err)
		}
		items = append(items, oracleBytes)
	}

	attestationBytes := bytes.Join(items, []byte("_"))

	return attestationBytes, nil
}
