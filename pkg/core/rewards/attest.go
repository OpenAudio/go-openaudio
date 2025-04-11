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

func (rs *RewardService) GetRewardById(rewardID string) (*Reward, error) {
	for _, reward := range rs.Rewards {
		if reward.RewardId == rewardID {
			return &reward, nil
		}
	}
	return nil, fmt.Errorf("reward %s not found", rewardID)
}

func (rs *RewardService) SignAttestation(attestationBytes []byte) (owner string, attestation string, err error) {
	privateKey := rs.Config.EthereumKey
	pubKey := privateKey.Public()
	pubKeyECDSA, ok := pubKey.(*ecdsa.PublicKey)
	if !ok {
		return "", "", fmt.Errorf("failed to cast public key to ecdsa.PublicKey")
	}
	owner = crypto.PubkeyToAddress(*pubKeyECDSA).String()

	hash := crypto.Keccak256(attestationBytes)
	signature, err := crypto.Sign(hash, privateKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign attestation: %w", err)
	}

	return owner, "0x" + hex.EncodeToString(signature), nil
}

func GetAttestationBytes(userWallet, rewardID, specifier, oracleAddress string, amount uint64) ([]byte, error) {
	combinedID := fmt.Sprintf("%s:%s", rewardID, specifier)

	encodedAmount := amount * 1e8
	amountBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(amountBytes, uint64(encodedAmount))

	userBytes, err := hex.DecodeString(strings.TrimPrefix(userWallet, "0x"))
	if err != nil {
		return nil, fmt.Errorf("failed to decode user wallet: %w", err)
	}
	oracleBytes, err := hex.DecodeString(strings.TrimPrefix(oracleAddress, "0x"))
	if err != nil {
		return nil, fmt.Errorf("failed to decode oracle address: %w", err)
	}
	combinedIDBytes := []byte(combinedID)
	items := [][]byte{userBytes, amountBytes, combinedIDBytes, oracleBytes}
	attestationBytes := bytes.Join(items, []byte("_"))

	return attestationBytes, nil
}

func GetClaimDataHash(userWallet, rewardID, specifier, oracleAddress string, amount uint64) ([]byte, error) {
	claimData, err := GetAttestationBytes(userWallet, rewardID, specifier, oracleAddress, amount)
	if err != nil {
		return nil, fmt.Errorf("failed to get attestation bytes: %w", err)
	}
	return crypto.Keccak256(claimData), nil
}

func RecoverWalletFromSignature(hash []byte, signature string) (string, error) {
	// Remove any existing 0x prefix
	sigHex := strings.TrimPrefix(signature, "0x")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", fmt.Errorf("failed to decode signature: %w", err)
	}

	recoveredWallet, err := crypto.SigToPub(hash[:], sigBytes)
	if err != nil {
		return "", fmt.Errorf("failed to recover wallet from signature: %w", err)
	}

	return crypto.PubkeyToAddress(*recoveredWallet).String(), nil
}

func SignClaimDataHash(hash []byte, privateKey *ecdsa.PrivateKey) (string, error) {
	signature, err := crypto.Sign(hash, privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign hash: %w", err)
	}
	return "0x" + hex.EncodeToString(signature), nil
}
