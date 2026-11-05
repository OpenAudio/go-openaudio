package performance

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58/base58"
)

const (
	ArtifactSchemaVersion uint64 = 1
	MaxArtifactBytes             = 64 << 20

	InstructionOpenFirstEpoch   = "open_first_epoch"
	InstructionOpenEpoch        = "open_epoch"
	InstructionAttestSnapshot   = "attest_snapshot"
	InstructionFinalizeFirst    = "finalize_first_snapshot"
	InstructionFinalizeSnapshot = "finalize_snapshot"
	InstructionClaim            = "claim"
	SnapshotAttestationSchema   = 1
)

var ErrArtifactConflict = errors.New("performance artifact already exists with different content")

type ArtifactSource struct {
	SourceID                 string `json:"source_id"`
	ChainID                  string `json:"chain_id"`
	FinalizedBlockHash       Hash   `json:"finalized_block_hash"`
	FinalizedBlockHeight     uint64 `json:"finalized_block_height"`
	InputCommitment          Hash   `json:"input_commitment"`
	UsefulWorkRoot           Hash   `json:"useful_work_root"`
	UsefulWorkAttestedWeight uint64 `json:"useful_work_attested_weight"`
}

type OpenEpochArgs struct {
	ID                  uint64 `json:"id"`
	StartUnix           uint64 `json:"start_unix"`
	EndUnix             uint64 `json:"end_unix"`
	StartBlock          uint64 `json:"start_block"`
	EndBlock            uint64 `json:"end_block"`
	ScoringVersion      Hash   `json:"scoring_version"`
	EligibleRoot        Hash   `json:"eligible_root"`
	TotalEligibleWeight uint64 `json:"total_eligible_weight"`
}

type OpenEpochPayload struct {
	FirstEpochInstruction string        `json:"first_epoch_instruction"`
	NextEpochInstruction  string        `json:"next_epoch_instruction"`
	Args                  OpenEpochArgs `json:"args"`
}

type SnapshotCommitment struct {
	Root           Hash   `json:"root"`
	TotalScore     uint64 `json:"total_score"`
	TotalAllocated uint64 `json:"total_allocated"`
}

type AttestSnapshotPayload struct {
	Instruction string             `json:"instruction"`
	Commitment  SnapshotCommitment `json:"commitment"`
	MessageHex  string             `json:"message_hex"`
	MessageHash Hash               `json:"message_hash"`
}

type FinalizeSnapshotPayload struct {
	FirstEpochInstruction string             `json:"first_epoch_instruction"`
	NextEpochInstruction  string             `json:"next_epoch_instruction"`
	EpochID               uint64             `json:"epoch_id"`
	Commitment            SnapshotCommitment `json:"commitment"`
}

type ClaimArgs struct {
	Operator       Address `json:"operator"`
	Score          uint64  `json:"score"`
	Allocation     uint64  `json:"allocation"`
	ScoringVersion Hash    `json:"scoring_version"`
	EvidenceHash   Hash    `json:"evidence_hash"`
	Proof          []Hash  `json:"proof"`
}

type ClaimPayload struct {
	Instruction string    `json:"instruction"`
	EpochID     uint64    `json:"epoch_id"`
	Args        ClaimArgs `json:"args"`
}

type SolanaRelayerPayload struct {
	ProgramID     string                  `json:"program_id"`
	ConfigAccount string                  `json:"config_account"`
	Open          OpenEpochPayload        `json:"open"`
	Attest        AttestSnapshotPayload   `json:"attest"`
	Finalize      FinalizeSnapshotPayload `json:"finalize"`
	Claims        []ClaimPayload          `json:"claims"`
}

// Artifact is the canonical deterministic file consumed by signers,
// dashboards, and Solana relayers.
type Artifact struct {
	SchemaVersion uint64               `json:"schema_version"`
	Source        ArtifactSource       `json:"source"`
	Snapshot      *Snapshot            `json:"snapshot"`
	Solana        SolanaRelayerPayload `json:"solana"`
}

type SnapshotAttestation struct {
	SchemaVersion uint64             `json:"schema_version"`
	EpochID       uint64             `json:"epoch_id"`
	Signer        Address            `json:"signer"`
	Signature     string             `json:"signature"`
	MessageHex    string             `json:"message_hex"`
	MessageHash   Hash               `json:"message_hash"`
	Commitment    SnapshotCommitment `json:"commitment"`
	Eligibility   EligibleSigner     `json:"eligibility"`
}

// GenerateArtifact runs the fail-closed source, scoring, persistence model,
// and relayer projection in one production invocation path.
func GenerateArtifact(
	ctx context.Context,
	source FinalizedSource,
	programID string,
	configAccount string,
) (*Artifact, error) {
	if source == nil {
		return nil, fmt.Errorf("finalized performance source is nil")
	}
	input, err := source.LoadFinalizedEpoch(ctx)
	if err != nil {
		return nil, fmt.Errorf("load finalized performance epoch: %w", err)
	}
	validated, err := ValidateFinalizedEpoch(input)
	if err != nil {
		return nil, fmt.Errorf("validate finalized performance epoch: %w", err)
	}
	return BuildArtifact(validated, programID, configAccount)
}

func BuildArtifact(validated *ValidatedEpoch, programIDText, configAccountText string) (*Artifact, error) {
	if validated == nil || validated.snapshot == nil || validated.sourceID == "" || validated.chainID == "" || validated.finalizedHash == (Hash{}) || validated.finalizedHeight == 0 {
		return nil, fmt.Errorf("validated epoch is incomplete")
	}
	programID, canonicalProgramID, err := parseSolanaPubkey(programIDText)
	if err != nil {
		return nil, fmt.Errorf("program id: %w", err)
	}
	configAccount, canonicalConfigAccount, err := parseSolanaPubkey(configAccountText)
	if err != nil {
		return nil, fmt.Errorf("config account: %w", err)
	}
	snapshot := validated.snapshot
	commitment := SnapshotCommitment{
		Root:           snapshot.MerkleRoot,
		TotalScore:     snapshot.TotalScore,
		TotalAllocated: snapshot.TotalAllocated,
	}
	message := snapshot.CommitmentMessage(programID, configAccount)
	claims := make([]ClaimPayload, len(snapshot.Entries))
	for i, entry := range snapshot.Entries {
		claims[i] = ClaimPayload{
			Instruction: InstructionClaim,
			EpochID:     snapshot.Epoch.ID,
			Args: ClaimArgs{
				Operator:       entry.Operator,
				Score:          entry.Score,
				Allocation:     entry.Allocation,
				ScoringVersion: entry.Version,
				EvidenceHash:   entry.EvidenceHash,
				Proof:          append([]Hash(nil), entry.Proof...),
			},
		}
	}
	artifact := &Artifact{
		SchemaVersion: ArtifactSchemaVersion,
		Source: ArtifactSource{
			SourceID:             validated.sourceID,
			ChainID:              validated.chainID,
			FinalizedBlockHash:   validated.finalizedHash,
			FinalizedBlockHeight: validated.finalizedHeight,
			InputCommitment: finalizedInputCommitment(
				validated.sourceID,
				validated.chainID,
				validated.finalizedHash,
				validated.finalizedHeight,
				snapshot,
				validated.usefulWorkRoot,
			),
			UsefulWorkRoot:           validated.usefulWorkRoot,
			UsefulWorkAttestedWeight: validated.attestedWeight,
		},
		Snapshot: snapshot,
		Solana: SolanaRelayerPayload{
			ProgramID:     canonicalProgramID,
			ConfigAccount: canonicalConfigAccount,
			Open: OpenEpochPayload{
				FirstEpochInstruction: InstructionOpenFirstEpoch,
				NextEpochInstruction:  InstructionOpenEpoch,
				Args: OpenEpochArgs{
					ID:                  snapshot.Epoch.ID,
					StartUnix:           snapshot.Epoch.StartUnix,
					EndUnix:             snapshot.Epoch.EndUnix,
					StartBlock:          snapshot.Epoch.StartBlock,
					EndBlock:            snapshot.Epoch.EndBlock,
					ScoringVersion:      snapshot.ScoringVersion,
					EligibleRoot:        snapshot.EligibleRoot,
					TotalEligibleWeight: snapshot.TotalEligibleWeight,
				},
			},
			Attest: AttestSnapshotPayload{
				Instruction: InstructionAttestSnapshot,
				Commitment:  commitment,
				MessageHex:  hex.EncodeToString(message),
				MessageHash: Hash(common.Keccak256Concat(message)),
			},
			Finalize: FinalizeSnapshotPayload{
				FirstEpochInstruction: InstructionFinalizeFirst,
				NextEpochInstruction:  InstructionFinalizeSnapshot,
				EpochID:               snapshot.Epoch.ID,
				Commitment:            commitment,
			},
			Claims: claims,
		},
	}
	if err := ValidateArtifact(artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

// ValidateArtifact recomputes all protocol leaves, roots, proofs, totals, and
// commitment bytes before an artifact is published or signed.
func ValidateArtifact(artifact *Artifact) error {
	if artifact == nil || artifact.Snapshot == nil {
		return fmt.Errorf("performance artifact is incomplete")
	}
	if artifact.SchemaVersion != ArtifactSchemaVersion {
		return fmt.Errorf("unsupported performance artifact schema %d", artifact.SchemaVersion)
	}
	programID, _, err := parseSolanaPubkey(artifact.Solana.ProgramID)
	if err != nil {
		return fmt.Errorf("artifact program id: %w", err)
	}
	configAccount, _, err := parseSolanaPubkey(artifact.Solana.ConfigAccount)
	if err != nil {
		return fmt.Errorf("artifact config account: %w", err)
	}
	snapshot := artifact.Snapshot
	if err := validateEpoch(snapshot.Epoch); err != nil {
		return err
	}
	if snapshot.Budget != EpochBudget || snapshot.ScoringVersion != ScoringVersionV1() {
		return fmt.Errorf("artifact has unsupported budget or scoring version")
	}
	if artifact.Source.SourceID == "" || artifact.Source.ChainID == "" || artifact.Source.FinalizedBlockHash == (Hash{}) || artifact.Source.FinalizedBlockHeight < snapshot.Epoch.EndBlock || artifact.Source.UsefulWorkRoot == (Hash{}) {
		return fmt.Errorf("artifact source is incomplete")
	}
	if !hasStrictSupermajority(artifact.Source.UsefulWorkAttestedWeight, snapshot.TotalEligibleWeight) {
		return fmt.Errorf("artifact useful-work quorum is insufficient")
	}
	if len(snapshot.EligibleSigners) == 0 || len(snapshot.Entries) != len(snapshot.EligibleSigners) {
		return fmt.Errorf("artifact operator sets are incomplete")
	}

	eligibleLeaves := make([]Hash, len(snapshot.EligibleSigners))
	var totalWeight uint64
	for i, eligible := range snapshot.EligibleSigners {
		if i > 0 && bytes.Compare(snapshot.EligibleSigners[i-1].Operator[:], eligible.Operator[:]) >= 0 {
			return fmt.Errorf("eligible operators are not strictly sorted")
		}
		leaf := EligibleLeafHash(eligible.Signer, eligible.Operator, eligible.Weight)
		if eligible.Signer.IsZero() || eligible.Operator.IsZero() || eligible.Weight == 0 || leaf != eligible.Leaf || !VerifyProof(leaf, eligible.Proof, snapshot.EligibleRoot) {
			return fmt.Errorf("invalid eligibility proof for %s", eligible.Signer)
		}
		eligibleLeaves[i] = leaf
		var carry uint64
		totalWeight, carry = bits.Add64(totalWeight, eligible.Weight, 0)
		if carry != 0 {
			return ErrArithmeticOverflow
		}
	}
	eligibleTree, err := NewTree(eligibleLeaves)
	if err != nil || eligibleTree.Root() != snapshot.EligibleRoot || totalWeight != snapshot.TotalEligibleWeight {
		return fmt.Errorf("artifact eligible root or weight mismatch")
	}

	rewardLeaves := make([]Hash, len(snapshot.Entries))
	var totalScore, totalAllocated uint64
	for i, entry := range snapshot.Entries {
		if i > 0 && bytes.Compare(snapshot.Entries[i-1].Operator[:], entry.Operator[:]) >= 0 {
			return fmt.Errorf("reward operators are not strictly sorted")
		}
		if entry.Operator != snapshot.EligibleSigners[i].Operator || entry.Operator.IsZero() || entry.EvidenceHash == (Hash{}) || entry.Version != snapshot.ScoringVersion || entry.Score > MaxScore || (entry.Score == 0 && entry.Allocation != 0) {
			return fmt.Errorf("invalid reward entry for %s", entry.Operator)
		}
		leaf := RewardLeafHash(snapshot.Epoch.ID, entry.Operator, entry.Score, entry.Allocation, entry.Version, entry.EvidenceHash)
		if leaf != entry.Leaf || !VerifyProof(leaf, entry.Proof, snapshot.MerkleRoot) {
			return fmt.Errorf("invalid reward proof for %s", entry.Operator)
		}
		rewardLeaves[i] = leaf
		var carry uint64
		totalScore, carry = bits.Add64(totalScore, entry.Score, 0)
		if carry != 0 {
			return ErrArithmeticOverflow
		}
		totalAllocated, carry = bits.Add64(totalAllocated, entry.Allocation, 0)
		if carry != 0 || totalAllocated > EpochBudget {
			return ErrArithmeticOverflow
		}
	}
	rewardTree, err := NewTree(rewardLeaves)
	if err != nil || rewardTree.Root() != snapshot.MerkleRoot || totalScore != snapshot.TotalScore || totalAllocated != snapshot.TotalAllocated {
		return fmt.Errorf("artifact reward root or totals mismatch")
	}
	if snapshot.TotalScore == 0 && snapshot.TotalAllocated != 0 {
		return fmt.Errorf("zero-score artifact has nonzero allocation")
	}

	open := artifact.Solana.Open
	wantOpen := OpenEpochArgs{
		ID: snapshot.Epoch.ID, StartUnix: snapshot.Epoch.StartUnix, EndUnix: snapshot.Epoch.EndUnix,
		StartBlock: snapshot.Epoch.StartBlock, EndBlock: snapshot.Epoch.EndBlock,
		ScoringVersion: snapshot.ScoringVersion, EligibleRoot: snapshot.EligibleRoot,
		TotalEligibleWeight: snapshot.TotalEligibleWeight,
	}
	if open.FirstEpochInstruction != InstructionOpenFirstEpoch || open.NextEpochInstruction != InstructionOpenEpoch || open.Args != wantOpen {
		return fmt.Errorf("artifact open-epoch payload mismatch")
	}
	wantCommitment := SnapshotCommitment{Root: snapshot.MerkleRoot, TotalScore: snapshot.TotalScore, TotalAllocated: snapshot.TotalAllocated}
	message := snapshot.CommitmentMessage(programID, configAccount)
	attest := artifact.Solana.Attest
	if attest.Instruction != InstructionAttestSnapshot || attest.Commitment != wantCommitment || attest.MessageHex != hex.EncodeToString(message) || attest.MessageHash != Hash(common.Keccak256Concat(message)) {
		return fmt.Errorf("artifact attestation payload mismatch")
	}
	finalize := artifact.Solana.Finalize
	if finalize.FirstEpochInstruction != InstructionFinalizeFirst || finalize.NextEpochInstruction != InstructionFinalizeSnapshot || finalize.EpochID != snapshot.Epoch.ID || finalize.Commitment != wantCommitment {
		return fmt.Errorf("artifact finalization payload mismatch")
	}
	if len(artifact.Solana.Claims) != len(snapshot.Entries) {
		return fmt.Errorf("artifact claim count mismatch")
	}
	for i, claim := range artifact.Solana.Claims {
		entry := snapshot.Entries[i]
		if claim.Instruction != InstructionClaim || claim.EpochID != snapshot.Epoch.ID || claim.Args.Operator != entry.Operator || claim.Args.Score != entry.Score || claim.Args.Allocation != entry.Allocation || claim.Args.ScoringVersion != entry.Version || claim.Args.EvidenceHash != entry.EvidenceHash || !equalHashes(claim.Args.Proof, entry.Proof) {
			return fmt.Errorf("artifact claim payload mismatch for %s", entry.Operator)
		}
	}
	wantInputCommitment := finalizedInputCommitment(
		artifact.Source.SourceID,
		artifact.Source.ChainID,
		artifact.Source.FinalizedBlockHash,
		artifact.Source.FinalizedBlockHeight,
		snapshot,
		artifact.Source.UsefulWorkRoot,
	)
	if artifact.Source.InputCommitment != wantInputCommitment {
		return fmt.Errorf("artifact input commitment mismatch")
	}
	return nil
}

func SignArtifact(artifact *Artifact, privateKey *ecdsa.PrivateKey) (*SnapshotAttestation, error) {
	if privateKey == nil || privateKey.Curve != gethcrypto.S256() {
		return nil, fmt.Errorf("invalid secp256k1 private key")
	}
	if err := ValidateArtifact(artifact); err != nil {
		return nil, err
	}
	addressText := gethcrypto.PubkeyToAddress(privateKey.PublicKey).Hex()
	signer, err := ParseAddress(addressText)
	if err != nil {
		return nil, err
	}
	var eligibility *EligibleSigner
	for i := range artifact.Snapshot.EligibleSigners {
		if artifact.Snapshot.EligibleSigners[i].Signer == signer {
			copy := artifact.Snapshot.EligibleSigners[i]
			eligibility = &copy
			break
		}
	}
	if eligibility == nil {
		return nil, fmt.Errorf("signer %s is not eligible for epoch %d", signer, artifact.Snapshot.Epoch.ID)
	}
	message, err := hex.DecodeString(artifact.Solana.Attest.MessageHex)
	if err != nil {
		return nil, fmt.Errorf("decode artifact commitment message: %w", err)
	}
	signature, err := common.EthSignKeccak(privateKey, message)
	if err != nil {
		return nil, err
	}
	_, recoveredText, err := common.EthRecoverKeccak(signature, message)
	if err != nil {
		return nil, err
	}
	recovered, err := ParseAddress(recoveredText)
	if err != nil || recovered != signer {
		return nil, fmt.Errorf("generated signature recovery mismatch")
	}
	attestation := &SnapshotAttestation{
		SchemaVersion: SnapshotAttestationSchema,
		EpochID:       artifact.Snapshot.Epoch.ID,
		Signer:        signer,
		Signature:     signature,
		MessageHex:    artifact.Solana.Attest.MessageHex,
		MessageHash:   artifact.Solana.Attest.MessageHash,
		Commitment:    artifact.Solana.Attest.Commitment,
		Eligibility:   *eligibility,
	}
	if err := ValidateSnapshotAttestation(artifact, attestation); err != nil {
		return nil, err
	}
	return attestation, nil
}

// ValidateSnapshotAttestation binds a signer record back to the canonical
// artifact before a relayer uses it to build the secp256k1 instruction.
func ValidateSnapshotAttestation(artifact *Artifact, attestation *SnapshotAttestation) error {
	if err := ValidateArtifact(artifact); err != nil {
		return err
	}
	if attestation == nil || attestation.SchemaVersion != SnapshotAttestationSchema {
		return fmt.Errorf("snapshot attestation is incomplete")
	}
	if attestation.EpochID != artifact.Snapshot.Epoch.ID || attestation.MessageHex != artifact.Solana.Attest.MessageHex || attestation.MessageHash != artifact.Solana.Attest.MessageHash || attestation.Commitment != artifact.Solana.Attest.Commitment {
		return fmt.Errorf("snapshot attestation does not match artifact")
	}
	eligibility := attestation.Eligibility
	if attestation.Signer != eligibility.Signer || eligibility.Leaf != EligibleLeafHash(eligibility.Signer, eligibility.Operator, eligibility.Weight) || !VerifyProof(eligibility.Leaf, eligibility.Proof, artifact.Snapshot.EligibleRoot) {
		return fmt.Errorf("snapshot attestation eligibility proof is invalid")
	}
	message, err := hex.DecodeString(attestation.MessageHex)
	if err != nil {
		return fmt.Errorf("snapshot attestation message is invalid: %w", err)
	}
	_, recoveredText, err := common.EthRecoverKeccak(strings.TrimPrefix(strings.TrimPrefix(attestation.Signature, "0x"), "0X"), message)
	if err != nil {
		return fmt.Errorf("snapshot attestation signature is invalid: %w", err)
	}
	recovered, err := ParseAddress(recoveredText)
	if err != nil || recovered != attestation.Signer {
		return fmt.Errorf("snapshot attestation signer recovery mismatch")
	}
	return nil
}

func MarshalArtifact(artifact *Artifact) ([]byte, error) {
	if err := ValidateArtifact(artifact); err != nil {
		return nil, err
	}
	return marshalCanonicalJSON(artifact)
}

func MarshalSnapshotAttestation(artifact *Artifact, attestation *SnapshotAttestation) ([]byte, error) {
	if err := ValidateSnapshotAttestation(artifact, attestation); err != nil {
		return nil, err
	}
	return marshalCanonicalJSON(attestation)
}

func DecodeArtifact(reader io.Reader) (*Artifact, error) {
	var artifact Artifact
	if err := decodeStrictJSON(reader, MaxArtifactBytes, &artifact); err != nil {
		return nil, err
	}
	if err := ValidateArtifact(&artifact); err != nil {
		return nil, err
	}
	return &artifact, nil
}

func LoadArtifactFile(path string) (*Artifact, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxArtifactBytes {
		return nil, fmt.Errorf("artifact file is invalid or too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	artifact, decodeErr := DecodeArtifact(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return nil, decodeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return artifact, nil
}

// PersistArtifact atomically creates an immutable artifact. Re-running with
// identical bytes is idempotent; competing bytes at the same path fail closed.
func PersistArtifact(path string, artifact *Artifact) error {
	data, err := MarshalArtifact(artifact)
	if err != nil {
		return err
	}
	return persistImmutable(path, data)
}

func PersistSnapshotAttestation(path string, artifact *Artifact, attestation *SnapshotAttestation) error {
	data, err := MarshalSnapshotAttestation(artifact, attestation)
	if err != nil {
		return err
	}
	return persistImmutable(path, data)
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func persistImmutable(path string, data []byte) error {
	if strings.TrimSpace(path) == "" || path == "-" {
		return fmt.Errorf("output path is empty or reserved for stdout")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return ErrArtifactConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".performance-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(existing, data) {
				return nil
			}
			return ErrArtifactConflict
		}
		return err
	}
	return nil
}

func parseSolanaPubkey(value string) ([32]byte, string, error) {
	var result [32]byte
	if value == "" || value != strings.TrimSpace(value) {
		return result, "", fmt.Errorf("base58 public key is empty or has surrounding whitespace")
	}
	decoded, err := base58.Decode(value)
	if err != nil {
		return result, "", fmt.Errorf("invalid base58: %w", err)
	}
	if len(decoded) != len(result) {
		return result, "", fmt.Errorf("must decode to 32 bytes, got %d", len(decoded))
	}
	copy(result[:], decoded)
	if result == ([32]byte{}) {
		return result, "", fmt.Errorf("zero public key is not allowed")
	}
	return result, base58.Encode(result[:]), nil
}

func equalHashes(left, right []Hash) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
