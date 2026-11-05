package performance

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"sort"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	FinalizedEpochSchemaVersion uint64 = 1
	MaxFinalizedEpochBytes             = 16 << 20
	MaxOperators                       = 10_000
	maxSourceIDLength                  = 128
)

var (
	ErrInvalidConsensus = errors.New("invalid useful-work consensus")
	ErrSourceMismatch   = errors.New("performance source mismatch")
	ErrManifestTooLarge = errors.New("performance manifest is too large")
)

// ConsensusAttestation is one eligible Ethereum signer's raw-Keccak signature
// over UsefulWorkConsensusPayload.Message.
type ConsensusAttestation struct {
	Signer    Address `json:"signer"`
	Signature string  `json:"signature"`
}

// UsefulWorkConsensus records the independently collected weighted quorum for
// the useful-work Merkle root. It is intentionally separate from the later
// Solana snapshot-root attestations.
type UsefulWorkConsensus struct {
	Root         Hash                   `json:"root"`
	Attestations []ConsensusAttestation `json:"attestations"`
}

// FinalizedEpochInput is the versioned production input contract for snapshot
// generation. SourceID identifies an operator-configured consensus producer;
// FileSource rejects manifests from any other producer.
type FinalizedEpochInput struct {
	SchemaVersion        uint64              `json:"schema_version"`
	SourceID             string              `json:"source_id"`
	ChainID              string              `json:"chain_id"`
	Epoch                Epoch               `json:"epoch"`
	ScoringVersion       Hash                `json:"scoring_version"`
	FinalizedBlockHash   Hash                `json:"finalized_block_hash"`
	FinalizedBlockHeight uint64              `json:"finalized_block_height"`
	Operators            []OperatorInput     `json:"operators"`
	UsefulWork           UsefulWorkConsensus `json:"useful_work_consensus"`
}

// UsefulWorkConsensusPayload is the exact material consensus signers inspect
// and sign before an input manifest is considered finalized.
type UsefulWorkConsensusPayload struct {
	Root                Hash   `json:"root"`
	EligibleRoot        Hash   `json:"eligible_root"`
	TotalEligibleWeight uint64 `json:"total_eligible_weight"`
	MessageHex          string `json:"message_hex"`
	MessageHash         Hash   `json:"message_hash"`
}

// ValidatedEpoch holds the deterministic snapshot and the quorum information
// proven by a finalized input manifest.
type ValidatedEpoch struct {
	sourceID        string
	chainID         string
	finalizedHash   Hash
	finalizedHeight uint64
	snapshot        *Snapshot
	usefulWorkRoot  Hash
	attestedWeight  uint64
	consensusBytes  []byte
}

// FinalizedSource loads an immutable finalized epoch from a registered source.
type FinalizedSource interface {
	LoadFinalizedEpoch(context.Context) (*FinalizedEpochInput, error)
}

// FileSource is the initial production source adapter. The configured source
// identity is mandatory and is included in every signed consensus payload.
type FileSource struct {
	Path             string
	ExpectedSourceID string
}

func (s FileSource) LoadFinalizedEpoch(ctx context.Context) (*FinalizedEpochInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Path) == "" {
		return nil, fmt.Errorf("finalized epoch path is empty")
	}
	if strings.TrimSpace(s.ExpectedSourceID) == "" {
		return nil, fmt.Errorf("expected source id is empty")
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		return nil, fmt.Errorf("stat finalized epoch: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("finalized epoch is not a regular file")
	}
	if info.Size() > MaxFinalizedEpochBytes {
		return nil, ErrManifestTooLarge
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("open finalized epoch: %w", err)
	}
	input, decodeErr := DecodeFinalizedEpoch(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return nil, fmt.Errorf("decode finalized epoch: %w", decodeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close finalized epoch: %w", closeErr)
	}
	if input.SourceID != s.ExpectedSourceID {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrSourceMismatch, s.ExpectedSourceID, input.SourceID)
	}
	return input, nil
}

// DecodeFinalizedEpoch strictly decodes one manifest and rejects unknown or
// trailing fields so a producer cannot accidentally sign a different schema.
func DecodeFinalizedEpoch(reader io.Reader) (*FinalizedEpochInput, error) {
	var input FinalizedEpochInput
	if err := decodeStrictJSON(reader, MaxFinalizedEpochBytes, &input); err != nil {
		return nil, err
	}
	return &input, nil
}

// PrepareUsefulWorkConsensus returns the exact useful-work root and bytes that
// eligible signers must approve. It validates every raw record but does not
// accept the manifest as finalized; ValidateFinalizedEpoch enforces quorum.
func PrepareUsefulWorkConsensus(input *FinalizedEpochInput) (*UsefulWorkConsensusPayload, error) {
	prepared, err := prepareFinalizedEpoch(input)
	if err != nil {
		return nil, err
	}
	messageHash := Hash(common.Keccak256Concat(prepared.message))
	return &UsefulWorkConsensusPayload{
		Root:                prepared.usefulWorkRoot,
		EligibleRoot:        prepared.snapshot.EligibleRoot,
		TotalEligibleWeight: prepared.snapshot.TotalEligibleWeight,
		MessageHex:          fmt.Sprintf("%x", prepared.message),
		MessageHash:         messageHash,
	}, nil
}

// SignUsefulWorkConsensus creates one collection record for a registered
// eligible signer. Aggregators store these records in the finalized manifest.
func SignUsefulWorkConsensus(input *FinalizedEpochInput, privateKey *ecdsa.PrivateKey) (*ConsensusAttestation, error) {
	if privateKey == nil || privateKey.Curve != gethcrypto.S256() {
		return nil, fmt.Errorf("invalid secp256k1 private key")
	}
	prepared, err := prepareFinalizedEpoch(input)
	if err != nil {
		return nil, err
	}
	signer, err := ParseAddress(gethcrypto.PubkeyToAddress(privateKey.PublicKey).Hex())
	if err != nil {
		return nil, err
	}
	eligible := false
	for _, entry := range prepared.snapshot.EligibleSigners {
		if entry.Signer == signer {
			eligible = true
			break
		}
	}
	if !eligible {
		return nil, fmt.Errorf("signer %s is not eligible for epoch %d", signer, input.Epoch.ID)
	}
	signature, err := common.EthSignKeccak(privateKey, prepared.message)
	if err != nil {
		return nil, err
	}
	return &ConsensusAttestation{Signer: signer, Signature: signature}, nil
}

// ValidateFinalizedEpoch verifies the registered source's complete metric set
// and strictly more than two thirds of frozen signing weight.
func ValidateFinalizedEpoch(input *FinalizedEpochInput) (*ValidatedEpoch, error) {
	prepared, err := prepareFinalizedEpoch(input)
	if err != nil {
		return nil, err
	}
	if input.UsefulWork.Root != prepared.usefulWorkRoot {
		return nil, fmt.Errorf("%w: useful-work root mismatch", ErrInvalidConsensus)
	}
	weights := make(map[Address]uint64, len(prepared.snapshot.EligibleSigners))
	for _, eligible := range prepared.snapshot.EligibleSigners {
		weights[eligible.Signer] = eligible.Weight
	}
	seen := make(map[Address]struct{}, len(input.UsefulWork.Attestations))
	var attestedWeight uint64
	for _, attestation := range input.UsefulWork.Attestations {
		if _, duplicate := seen[attestation.Signer]; duplicate {
			return nil, fmt.Errorf("%w: duplicate signer %s", ErrInvalidConsensus, attestation.Signer)
		}
		weight, eligible := weights[attestation.Signer]
		if !eligible {
			return nil, fmt.Errorf("%w: signer %s is not eligible", ErrInvalidConsensus, attestation.Signer)
		}
		signature := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(attestation.Signature), "0x"), "0X")
		_, recoveredText, err := common.EthRecoverKeccak(signature, prepared.message)
		if err != nil {
			return nil, fmt.Errorf("%w: recover signer %s: %v", ErrInvalidConsensus, attestation.Signer, err)
		}
		recovered, err := ParseAddress(recoveredText)
		if err != nil || recovered != attestation.Signer {
			return nil, fmt.Errorf("%w: signature does not recover signer %s", ErrInvalidConsensus, attestation.Signer)
		}
		seen[attestation.Signer] = struct{}{}
		var carry uint64
		attestedWeight, carry = bits.Add64(attestedWeight, weight, 0)
		if carry != 0 {
			return nil, ErrArithmeticOverflow
		}
	}
	if !hasStrictSupermajority(attestedWeight, prepared.snapshot.TotalEligibleWeight) {
		return nil, fmt.Errorf(
			"%w: attested weight %d is not greater than two thirds of %d",
			ErrInvalidConsensus,
			attestedWeight,
			prepared.snapshot.TotalEligibleWeight,
		)
	}
	return &ValidatedEpoch{
		sourceID:        input.SourceID,
		chainID:         input.ChainID,
		finalizedHash:   input.FinalizedBlockHash,
		finalizedHeight: input.FinalizedBlockHeight,
		snapshot:        prepared.snapshot,
		usefulWorkRoot:  prepared.usefulWorkRoot,
		attestedWeight:  attestedWeight,
		consensusBytes:  append([]byte(nil), prepared.message...),
	}, nil
}

type preparedEpoch struct {
	snapshot       *Snapshot
	usefulWorkRoot Hash
	message        []byte
}

func prepareFinalizedEpoch(input *FinalizedEpochInput) (*preparedEpoch, error) {
	if input == nil {
		return nil, fmt.Errorf("finalized epoch is nil")
	}
	if input.SchemaVersion != FinalizedEpochSchemaVersion {
		return nil, fmt.Errorf("unsupported finalized epoch schema %d", input.SchemaVersion)
	}
	if input.SourceID == "" || input.SourceID != strings.TrimSpace(input.SourceID) || len(input.SourceID) > maxSourceIDLength {
		return nil, fmt.Errorf("invalid performance source id")
	}
	if input.ChainID == "" || input.ChainID != strings.TrimSpace(input.ChainID) || len(input.ChainID) > maxSourceIDLength {
		return nil, fmt.Errorf("invalid Core chain id")
	}
	if input.FinalizedBlockHash == (Hash{}) {
		return nil, fmt.Errorf("finalized Core block hash is missing")
	}
	if input.FinalizedBlockHeight < input.Epoch.EndBlock {
		return nil, fmt.Errorf("finalized Core block height %d precedes epoch end block %d", input.FinalizedBlockHeight, input.Epoch.EndBlock)
	}
	if len(input.Operators) == 0 || len(input.Operators) > MaxOperators {
		return nil, fmt.Errorf("%w: operator count %d", ErrInvalidMetric, len(input.Operators))
	}
	for _, operator := range input.Operators {
		if err := validateFinalizedMetric("storage", operator.Storage); err != nil {
			return nil, fmt.Errorf("operator %s: %w", operator.Operator, err)
		}
		if err := validateFinalizedMetric("useful work", operator.UsefulWork); err != nil {
			return nil, fmt.Errorf("operator %s: %w", operator.Operator, err)
		}
		if err := validateFinalizedMetric("block production", operator.BlockProduction); err != nil {
			return nil, fmt.Errorf("operator %s: %w", operator.Operator, err)
		}
	}

	snapshot, err := BuildSnapshotForVersion(input.Epoch, input.ScoringVersion, input.Operators)
	if err != nil {
		return nil, err
	}
	ordered := append([]OperatorInput(nil), input.Operators...)
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(ordered[i].Operator[:], ordered[j].Operator[:]) < 0
	})
	leaves := make([]Hash, len(ordered))
	for i, operator := range ordered {
		leaves[i] = UsefulWorkLeafHash(input.Epoch.ID, operator.Operator, operator.UsefulWork)
	}
	tree, err := NewTree(leaves)
	if err != nil {
		return nil, err
	}
	root := tree.Root()
	message := usefulWorkConsensusMessage(
		input.SourceID,
		input.ChainID,
		input.FinalizedBlockHash,
		input.FinalizedBlockHeight,
		input.Epoch,
		input.ScoringVersion,
		snapshot.EligibleRoot,
		snapshot.TotalEligibleWeight,
		root,
	)
	return &preparedEpoch{snapshot: snapshot, usefulWorkRoot: root, message: message}, nil
}

func validateFinalizedMetric(name string, metric Metric) error {
	if metric.Total == 0 {
		return fmt.Errorf("%w: %s total is zero or absent", ErrInvalidMetric, name)
	}
	if metric.Completed > metric.Total {
		return fmt.Errorf("%w: %s completed %d exceeds total %d", ErrInvalidMetric, name, metric.Completed, metric.Total)
	}
	if metric.EvidenceHash == (Hash{}) {
		return fmt.Errorf("%w: %s evidence hash is missing", ErrInvalidMetric, name)
	}
	return nil
}

func hasStrictSupermajority(attested, total uint64) bool {
	if total == 0 || attested > total {
		return false
	}
	quotient, remainder := total/3, total%3
	threshold := quotient*2 + (remainder*2)/3
	return attested > threshold
}
