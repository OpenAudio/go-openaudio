package rewards

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/crypto"
)

// ClaimV2Domain separates reward claims from every other signing protocol.
const ClaimV2Domain = "AudiusRewardClaimV2"

type ClaimRole byte

const (
	ClaimRoleOracle    ClaimRole = 1
	ClaimRoleValidator ClaimRole = 2
)

// RewardClaimV2 binds a claim to the complete payout context. Amount is in
// atomic token units, never a caller-selected decimal scale. ChainDomain and
// Epoch must come from the reward manager's on-chain policy, not the request.
// This format is intentionally separate from legacy RewardClaim. It must not
// be signed in production until an on-chain V2 verifier and policy are deployed.
type RewardClaimV2 struct {
	ChainDomain    [32]byte
	ProgramID      [32]byte
	RewardManager  [32]byte
	Mint           [32]byte
	RewardAddress  [20]byte
	Recipient      [20]byte
	Oracle         [20]byte
	Amount         uint64
	Epoch          uint64
	ValidUntilSlot uint64
	DisbursementID string
}

// Compile returns a 52-byte message that fits legacy VerifiedMessages storage.
// The digest covers a canonical, length-delimited context. The role is outside
// the digest but inside the signed message, preventing validator/oracle reuse.
func (c RewardClaimV2) Compile(role ClaimRole) ([]byte, error) {
	if role != ClaimRoleOracle && role != ClaimRoleValidator {
		return nil, fmt.Errorf("invalid reward claim role")
	}
	if c.ChainDomain == ([32]byte{}) || c.ProgramID == ([32]byte{}) || c.RewardManager == ([32]byte{}) || c.Mint == ([32]byte{}) || c.RewardAddress == ([20]byte{}) || c.Oracle == ([20]byte{}) {
		return nil, fmt.Errorf("missing reward claim domain or authority")
	}
	// The legacy PDA uses the two-byte V_ / T_ prefix plus this ID as a seed.
	if len(c.DisbursementID) == 0 || len(c.DisbursementID) > 30 || !utf8.ValidString(c.DisbursementID) {
		return nil, fmt.Errorf("disbursement ID must be valid UTF-8 and 1 to 30 bytes")
	}
	if c.Amount == 0 || c.Epoch == 0 || c.ValidUntilSlot == 0 {
		return nil, fmt.Errorf("amount, epoch, and expiry must be nonzero")
	}
	var b bytes.Buffer
	b.WriteString(ClaimV2Domain)
	for _, v := range [][]byte{c.ChainDomain[:], c.ProgramID[:], c.RewardManager[:], c.Mint[:], c.RewardAddress[:], c.Recipient[:], c.Oracle[:]} {
		b.Write(v)
	}
	for _, v := range []uint64{c.Amount, c.Epoch, c.ValidUntilSlot} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	_ = binary.Write(&b, binary.LittleEndian, uint16(len(c.DisbursementID)))
	b.WriteString(c.DisbursementID)
	message := append([]byte(ClaimV2Domain), byte(role))
	return append(message, crypto.Keccak256(b.Bytes())...), nil
}

func SignClaimV2(claim RewardClaimV2, role ClaimRole, key *ecdsa.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("missing signing key")
	}
	if role == ClaimRoleOracle && crypto.PubkeyToAddress(key.PublicKey) != claim.Oracle {
		return "", fmt.Errorf("signing key does not match claim oracle")
	}
	message, err := claim.Compile(role)
	if err != nil {
		return "", err
	}
	sig, err := crypto.Sign(crypto.Keccak256(message), key)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// VerifyOracleV2 authenticates the exact oracle embedded in the claim. Pool
// authorization, policy epoch/expiry, and amount eligibility are separate
// mandatory checks at the caller and on-chain verifier.
func VerifyOracleV2(claim RewardClaimV2, signature []byte) error {
	message, err := claim.Compile(ClaimRoleOracle)
	if err != nil {
		return err
	}
	pub, err := crypto.SigToPub(crypto.Keccak256(message), signature)
	if err != nil {
		return fmt.Errorf("invalid oracle signature: %w", err)
	}
	if crypto.PubkeyToAddress(*pub) != claim.Oracle {
		return fmt.Errorf("signature does not match claim oracle")
	}
	return nil
}
