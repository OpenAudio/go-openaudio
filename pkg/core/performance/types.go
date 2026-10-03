// Package performance builds deterministic, versioned validator-node
// performance snapshots for the Solana performance-rewards program.
package performance

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// EpochDurationSeconds is one seven-day reward epoch.
	EpochDurationSeconds uint64 = 7 * 24 * 60 * 60
	// AudioBaseUnitsPerToken is the number of base units in one Solana AUDIO.
	AudioBaseUnitsPerToken uint64 = 100_000_000
	// EpochBudget is the fixed 100,000 AUDIO development reward pool.
	EpochBudget uint64 = 100_000 * AudioBaseUnitsPerToken
	// MaxScore is the maximum score produced by a scoring version.
	MaxScore uint64 = 10_000
)

var (
	ErrInvalidEpoch       = errors.New("invalid performance epoch")
	ErrInvalidAddress     = errors.New("invalid ethereum address")
	ErrInvalidHash        = errors.New("invalid performance hash")
	ErrInvalidMetric      = errors.New("invalid performance metric")
	ErrDuplicateOperator  = errors.New("duplicate operator")
	ErrDuplicateSigner    = errors.New("duplicate signer")
	ErrUnsupportedVersion = errors.New("unsupported scoring version")
	ErrArithmeticOverflow = errors.New("performance arithmetic overflow")
)

// Hash is a fixed-width Keccak-256 digest.
type Hash [32]byte

// ParseHash parses a 32-byte hex digest with an optional 0x prefix.
func ParseHash(value string) (Hash, error) {
	var result Hash
	value = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	if len(value) != hex.EncodedLen(len(result)) {
		return result, fmt.Errorf("%w: expected 32 bytes", ErrInvalidHash)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidHash, err)
	}
	copy(result[:], decoded)
	return result, nil
}

// String returns a lower-case 0x-prefixed digest.
func (h Hash) String() string { return "0x" + hex.EncodeToString(h[:]) }

// MarshalText makes hashes stable, readable JSON strings.
func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText parses a stable JSON/text hash.
func (h *Hash) UnmarshalText(text []byte) error {
	parsed, err := ParseHash(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// Address is a raw Ethereum address without a textual 0x prefix.
type Address [20]byte

// ParseAddress parses a canonical or mixed-case Ethereum hex address.
func ParseAddress(value string) (Address, error) {
	var result Address
	value = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	if len(value) != hex.EncodedLen(len(result)) {
		return result, fmt.Errorf("%w: expected 20 bytes", ErrInvalidAddress)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidAddress, err)
	}
	copy(result[:], decoded)
	if result.IsZero() {
		return Address{}, fmt.Errorf("%w: zero address", ErrInvalidAddress)
	}
	return result, nil
}

// String returns a lower-case 0x-prefixed Ethereum address.
func (a Address) String() string { return "0x" + hex.EncodeToString(a[:]) }

// MarshalText makes addresses stable, lower-case JSON strings.
func (a Address) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText parses an Ethereum address from JSON or text.
func (a *Address) UnmarshalText(text []byte) error {
	parsed, err := ParseAddress(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// IsZero reports whether the address is all zeroes.
func (a Address) IsZero() bool { return a == Address{} }

// Epoch fixes the time and block ranges used by every node generating a snapshot.
// EndUnix and EndBlock are exclusive.
type Epoch struct {
	ID         uint64 `json:"id"`
	StartUnix  uint64 `json:"start_unix"`
	EndUnix    uint64 `json:"end_unix"`
	StartBlock uint64 `json:"start_block"`
	EndBlock   uint64 `json:"end_block"`
}

// Metric is an aggregate performance ratio plus a hash of its underlying
// consensus evidence. Completed must not exceed Total.
type Metric struct {
	Completed    uint64 `json:"completed"`
	Total        uint64 `json:"total"`
	EvidenceHash Hash   `json:"evidence_hash"`
}

// OperatorInput contains the Ethereum-derived frozen identity/weight and the
// three consensus-derived performance inputs for one validator node operator.
type OperatorInput struct {
	Operator        Address `json:"operator"`
	Signer          Address `json:"signer"`
	Weight          uint64  `json:"weight"`
	Storage         Metric  `json:"storage"`
	UsefulWork      Metric  `json:"useful_work"`
	BlockProduction Metric  `json:"block_production"`
}

// Entry is a claimable leaf in the global performance snapshot.
type Entry struct {
	Operator     Address `json:"operator"`
	Score        uint64  `json:"score"`
	Allocation   uint64  `json:"allocation"`
	Version      Hash    `json:"scoring_version"`
	EvidenceHash Hash    `json:"evidence_hash"`
	Leaf         Hash    `json:"leaf"`
	Proof        []Hash  `json:"proof"`
}

// EligibleSigner is a frozen weighted attester and its membership proof.
type EligibleSigner struct {
	Signer   Address `json:"signer"`
	Operator Address `json:"operator"`
	Weight   uint64  `json:"weight"`
	Leaf     Hash    `json:"leaf"`
	Proof    []Hash  `json:"proof"`
}

// Snapshot is the complete deterministic artifact attested and finalized on Solana.
type Snapshot struct {
	Epoch               Epoch            `json:"epoch"`
	Budget              uint64           `json:"budget"`
	ScoringVersion      Hash             `json:"scoring_version"`
	EligibleRoot        Hash             `json:"eligible_root"`
	MerkleRoot          Hash             `json:"merkle_root"`
	TotalEligibleWeight uint64           `json:"total_eligible_weight"`
	TotalScore          uint64           `json:"total_score"`
	TotalAllocated      uint64           `json:"total_allocated"`
	EligibleSigners     []EligibleSigner `json:"eligible_signers"`
	Entries             []Entry          `json:"entries"`
}
