package common

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	corev1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
)

// CreateDeterministicCreateRewardData creates deterministic hex data for signing CreateReward
func CreateDeterministicCreateRewardData(createReward *corev1.CreateReward) string {
	// Sort claim authorities for deterministic ordering
	authorities := make([]string, len(createReward.ClaimAuthorities))
	for i, auth := range createReward.ClaimAuthorities {
		authorities[i] = fmt.Sprintf("%s:%s", auth.Address, auth.Name)
	}
	sort.Strings(authorities)

	authoritiesJson, _ := json.Marshal(authorities)
	data := fmt.Sprintf("%s|%s|%d|%s|%d",
		createReward.RewardId,
		createReward.Name,
		createReward.Amount,
		string(authoritiesJson),
		createReward.DeadlineBlockHeight)

	// Hash the data for consistent length
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// CreateDeterministicDeleteRewardData creates deterministic hex data for signing DeleteReward
func CreateDeterministicDeleteRewardData(deleteReward *corev1.DeleteReward) string {
	data := fmt.Sprintf("%s|%d", deleteReward.Address, deleteReward.DeadlineBlockHeight)

	// Hash the data for consistent length
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// SignCreateReward signs a CreateReward message using deterministic data
func SignCreateReward(privateKey *ecdsa.PrivateKey, createReward *corev1.CreateReward) (string, error) {
	signatureData := CreateDeterministicCreateRewardData(createReward)

	// Convert hex data to bytes for signing
	dataBytes, err := hex.DecodeString(signatureData)
	if err != nil {
		return "", fmt.Errorf("invalid hex data: %w", err)
	}

	return EthSign(privateKey, dataBytes)
}

// SignDeleteReward signs a DeleteReward message using deterministic data
func SignDeleteReward(privateKey *ecdsa.PrivateKey, deleteReward *corev1.DeleteReward) (string, error) {
	signatureData := CreateDeterministicDeleteRewardData(deleteReward)

	// Convert hex data to bytes for signing
	dataBytes, err := hex.DecodeString(signatureData)
	if err != nil {
		return "", fmt.Errorf("invalid hex data: %w", err)
	}

	return EthSign(privateKey, dataBytes)
}
