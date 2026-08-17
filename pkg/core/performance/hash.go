package performance

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/crypto"
)

var (
	eligibleLeafDomain = []byte("OAP_PERFORMANCE_ELIGIBLE_V1")
	rewardLeafDomain   = []byte("OAP_PERFORMANCE_REWARD_V1")
	evidenceDomain     = []byte("OAP_PERFORMANCE_EVIDENCE_V1")
	scoringV1Domain    = []byte("OAP_PERFORMANCE_SCORING_V1")
	snapshotDomain     = []byte("OAP_PERFORMANCE_SNAPSHOT_V1")
)

var scoringVersionV1 = keccak(scoringV1Domain)

// ScoringVersionV1 returns the stable identifier for ScoreV1. Returning the
// array by value prevents callers from mutating process-global protocol state.
func ScoringVersionV1() Hash { return scoringVersionV1 }

func keccak(parts ...[]byte) Hash {
	h := crypto.NewKeccakState()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	var result Hash
	_, _ = h.Read(result[:])
	return result
}

// CommitmentMessage returns the exact bytes an eligible Ethereum signer
// attests through Solana's secp256k1 builtin. It binds the artifact to one
// deployed program and config account so a signature cannot cross deployments.
func (s *Snapshot) CommitmentMessage(programID, configAccount [32]byte) []byte {
	return concat(
		snapshotDomain,
		programID[:],
		configAccount[:],
		uint64Bytes(s.Epoch.ID),
		uint64Bytes(s.Epoch.StartUnix),
		uint64Bytes(s.Epoch.EndUnix),
		uint64Bytes(s.Epoch.StartBlock),
		uint64Bytes(s.Epoch.EndBlock),
		s.EligibleRoot[:],
		uint64Bytes(s.TotalEligibleWeight),
		s.ScoringVersion[:],
		s.MerkleRoot[:],
		uint64Bytes(s.TotalScore),
		uint64Bytes(s.TotalAllocated),
	)
}

// CommitmentHash is a compact diagnostic identifier for CommitmentMessage.
// Signers sign the message bytes, not this hash; the secp256k1 builtin applies
// its own Keccak hashing as part of signature recovery.
func (s *Snapshot) CommitmentHash(programID, configAccount [32]byte) Hash {
	return keccak(s.CommitmentMessage(programID, configAccount))
}

func concat(parts ...[]byte) []byte {
	size := 0
	for _, part := range parts {
		size += len(part)
	}
	result := make([]byte, 0, size)
	for _, part := range parts {
		result = append(result, part...)
	}
	return result
}

func uint64Bytes(value uint64) []byte {
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, value)
	return result
}

// EligibleLeafHash hashes one frozen signer/operator/weight tuple.
func EligibleLeafHash(signer, operator Address, weight uint64) Hash {
	return keccak(eligibleLeafDomain, signer[:], operator[:], uint64Bytes(weight))
}

// RewardLeafHash hashes one claim allocation. All numeric fields use
// fixed-width big-endian encoding for byte-for-byte Rust compatibility.
func RewardLeafHash(epochID uint64, operator Address, score, allocation uint64, version, evidence Hash) Hash {
	return keccak(
		rewardLeafDomain,
		uint64Bytes(epochID),
		operator[:],
		uint64Bytes(score),
		uint64Bytes(allocation),
		version[:],
		evidence[:],
	)
}

// EvidenceHash commits to the complete raw aggregate input for one operator.
func EvidenceHash(epoch Epoch, input OperatorInput) Hash {
	return keccak(
		evidenceDomain,
		uint64Bytes(epoch.ID),
		uint64Bytes(epoch.StartUnix),
		uint64Bytes(epoch.EndUnix),
		uint64Bytes(epoch.StartBlock),
		uint64Bytes(epoch.EndBlock),
		input.Operator[:],
		input.Signer[:],
		uint64Bytes(input.Weight),
		uint64Bytes(input.Storage.Completed),
		uint64Bytes(input.Storage.Total),
		input.Storage.EvidenceHash[:],
		uint64Bytes(input.UsefulWork.Completed),
		uint64Bytes(input.UsefulWork.Total),
		input.UsefulWork.EvidenceHash[:],
		uint64Bytes(input.BlockProduction.Completed),
		uint64Bytes(input.BlockProduction.Total),
		input.BlockProduction.EvidenceHash[:],
	)
}
