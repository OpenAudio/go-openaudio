package common

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// Legacy reward signing scheme used by the pre-pool-rollout network. Kept
// here so the new binary can recover signers from historical reward
// transactions during block-sync-from-genesis. New code paths must not use
// this scheme — sign / verify against the body+signature envelope instead.
//
// The scheme is a pipe-delimited canonical string of the action's fields
// (with claim authorities sorted and JSON-encoded), sha256-hashed, and
// signed with EthSign / recovered with EthRecover. It is NOT
// proto-deterministic and depends on the field set of the legacy proto
// types staying frozen.

func LegacyDeterministicCreateRewardData(cr *corev1.LegacyCreateReward) string {
	authorities := make([]string, len(cr.ClaimAuthorities))
	for i, auth := range cr.ClaimAuthorities {
		authorities[i] = fmt.Sprintf("%s:%s", auth.Address, auth.Name)
	}
	sort.Strings(authorities)
	authoritiesJson, _ := json.Marshal(authorities)
	data := fmt.Sprintf("%s|%s|%d|%s|%d",
		cr.RewardId,
		cr.Name,
		cr.Amount,
		string(authoritiesJson),
		cr.DeadlineBlockHeight)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func LegacyDeterministicDeleteRewardData(dr *corev1.LegacyDeleteReward) string {
	data := fmt.Sprintf("%s|%d", dr.Address, dr.DeadlineBlockHeight)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// LegacyRecoverCreateReward returns the eth address that signed a legacy
// CreateReward transaction.
func LegacyRecoverCreateReward(cr *corev1.LegacyCreateReward) (string, error) {
	signatureData := LegacyDeterministicCreateRewardData(cr)
	dataBytes, err := hex.DecodeString(signatureData)
	if err != nil {
		return "", fmt.Errorf("invalid hex data: %w", err)
	}
	_, address, err := EthRecover(cr.Signature, dataBytes)
	return address, err
}

// LegacyRecoverDeleteReward returns the eth address that signed a legacy
// DeleteReward transaction.
func LegacyRecoverDeleteReward(dr *corev1.LegacyDeleteReward) (string, error) {
	signatureData := LegacyDeterministicDeleteRewardData(dr)
	dataBytes, err := hex.DecodeString(signatureData)
	if err != nil {
		return "", fmt.Errorf("invalid hex data: %w", err)
	}
	_, address, err := EthRecover(dr.Signature, dataBytes)
	return address, err
}
