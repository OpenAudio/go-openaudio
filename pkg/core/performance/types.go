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
	ErrInvalidMetric      = errors.New("invalid performance metric")
	ErrDuplicateOperator  = errors.New("duplicate operator")
	ErrDuplicateSigner    = errors.New("duplicate signer")
	ErrUnsupportedVersion = errors.New("unsupported scoring version")
	ErrArithmeticOverflow = errors.New("performance arithmetic overflow")
)

// Hash is a fixed-width Keccak-256 digest.
type Hash [32]byte

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

// IsZero reports whether the address is all zeroes.
func (a Address) IsZero() bool { return a == Address{} }

// Epoch fixes the time and block ranges used by every node generating a snapshot.
// EndUnix and EndBlock are exclusive.
type Epoch struct {
	ID         uint64
	StartUnix  uint64
	EndUnix    uint64
	StartBlock uint64
	EndBlock   uint64
}

// Metric is an aggregate performance ratio plus a hash of its underlying
// consensus evidence. Completed must not exceed Total.
type Metric struct {
	Completed    uint64
	Total        uint64
	EvidenceHash Hash
}

// OperatorInput contains the Ethereum-derived frozen identity/weight and the
// three consensus-derived performance inputs for one validator node operator.
type OperatorInput struct {
	Operator        Address
	Signer          Address
	Weight          uint64
	Storage         Metric
	UsefulWork      Metric
	BlockProduction Metric
}

// Entry is a claimable leaf in the global performance snapshot.
type Entry struct {
	Operator     Address
	Score        uint64
	Allocation   uint64
	Version      Hash
	EvidenceHash Hash
	Leaf         Hash
	Proof        []Hash
}

// EligibleSigner is a frozen weighted attester and its membership proof.
type EligibleSigner struct {
	Signer   Address
	Operator Address
	Weight   uint64
	Leaf     Hash
	Proof    []Hash
}

// Snapshot is the complete deterministic artifact attested and finalized on Solana.
type Snapshot struct {
	Epoch               Epoch
	Budget              uint64
	ScoringVersion      Hash
	EligibleRoot        Hash
	MerkleRoot          Hash
	TotalEligibleWeight uint64
	TotalScore          uint64
	TotalAllocated      uint64
	EligibleSigners     []EligibleSigner
	Entries             []Entry
}
