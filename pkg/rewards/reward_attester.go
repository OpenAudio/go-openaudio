package rewards

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

type RewardAttester struct {
	EthereumAddress string
	EthereumKey     *ecdsa.PrivateKey
	Rewards         []Reward
}

func NewRewardAttester(ethereumKey *ecdsa.PrivateKey, rewards []Reward) *RewardAttester {
	// Get owner address from private key
	address := ""
	if ethereumKey != nil {
		pubKey := ethereumKey.Public()
		pubKeyECDSA, _ := pubKey.(*ecdsa.PublicKey)
		address = crypto.PubkeyToAddress(*pubKeyECDSA).Hex()
	}

	return &RewardAttester{
		EthereumAddress: address,
		EthereumKey:     ethereumKey,
		Rewards:         rewards,
	}
}

func (rs *RewardAttester) Validate(claim RewardClaim) error {
	reward, err := rs.getRewardById(claim.RewardID)
	if err != nil {
		return err
	}

	if claim.RecipientEthAddress == "" {
		return fmt.Errorf("missing recipient eth address")
	}

	if claim.Specifier == "" {
		return fmt.Errorf("missing specifier")
	}

	if claim.Amount == 0 {
		return fmt.Errorf("missing amount")
	}

	// Allow any positive amount up to the static reward config amount. Some
	// rewards (notably trending tracks/underground top 10) pay rank-dependent
	// amounts that are <= the headline reward.Amount; the actual per-claim
	// amount is determined by the discovery node from user_challenges.amount
	// and the claim authority is trusted to request the correct amount. This
	// bound just prevents catastrophic over-attestation if the authority is
	// buggy or compromised.
	if claim.Amount > reward.Amount {
		return fmt.Errorf("amount %d exceeds reward amount %d", claim.Amount, reward.Amount)
	}

	// TODO: Check oracle is registered, maybe validate lengths of inputs?

	return nil
}

func (rs *RewardAttester) Authenticate(claim RewardClaim, signature string) error {
	reward, err := rs.getRewardById(claim.RewardID)
	if err != nil {
		return err
	}

	recoveredSigner, err := recoverSigner(claim, signature)
	if err != nil {
		return err
	}

	if !validClaimAuthority(reward.ClaimAuthorities, recoveredSigner) {
		return fmt.Errorf("address %s is not a claim authority for reward %s", recoveredSigner, reward.RewardId)
	}

	return nil
}

func (rs *RewardAttester) Attest(claim RewardClaim) (message []byte, signature string, err error) {
	claimData, err := claim.Compile()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get attestation bytes: %w", err)
	}

	hash := crypto.Keccak256(claimData)

	signatureBytes, err := crypto.Sign(hash, rs.EthereumKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to sign hash: %w", err)
	}

	return claimData, "0x" + hex.EncodeToString(signatureBytes), nil
}

func (rs *RewardAttester) getRewardById(rewardID string) (*Reward, error) {
	for _, reward := range rs.Rewards {
		if reward.RewardId == rewardID {
			return &reward, nil
		}
	}
	return nil, fmt.Errorf("reward %s not found", rewardID)
}

func recoverSigner(claim RewardClaim, signature string) (string, error) {
	// Use the VerifyClaim utility function
	return VerifyClaim(claim, signature)
}

func validClaimAuthority(claimAuthorities []ClaimAuthority, address string) bool {
	for _, authority := range claimAuthorities {
		// Case-insensitive comparison for Ethereum addresses
		if strings.EqualFold(authority.Address, address) {
			return true
		}
	}
	return false
}
