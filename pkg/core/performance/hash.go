package performance

import (
	"encoding/binary"

	"github.com/OpenAudio/go-openaudio/pkg/common"
)

var (
	eligibleLeafDomain        = []byte("OAP_PERFORMANCE_ELIGIBLE_V1")
	rewardLeafDomain          = []byte("OAP_PERFORMANCE_REWARD_V1")
	evidenceDomain            = []byte("OAP_PERFORMANCE_EVIDENCE_V1")
	scoringV1Domain           = []byte("OAP_PERFORMANCE_SCORING_V1")
	snapshotDomain            = []byte("OAP_PERFORMANCE_SNAPSHOT_V1")
	usefulWorkLeafDomain      = []byte("OAP_PERFORMANCE_USEFUL_WORK_LEAF_V1")
	usefulWorkConsensusDomain = []byte("OAP_PERFORMANCE_USEFUL_WORK_CONSENSUS_V1")
	finalizedInputDomain      = []byte("OAP_PERFORMANCE_FINALIZED_INPUT_V1")
)

var scoringVersionV1 = Hash(common.Keccak256Concat(scoringV1Domain))

// ScoringVersionV1 returns the stable identifier for ScoreV1. Returning the
// array by value prevents callers from mutating process-global protocol state.
func ScoringVersionV1() Hash { return scoringVersionV1 }

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
	return Hash(common.Keccak256Concat(s.CommitmentMessage(programID, configAccount)))
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
	return Hash(common.Keccak256Concat(eligibleLeafDomain, signer[:], operator[:], uint64Bytes(weight)))
}

// RewardLeafHash hashes one claim allocation. All numeric fields use
// fixed-width big-endian encoding for byte-for-byte Rust compatibility.
func RewardLeafHash(epochID uint64, operator Address, score, allocation uint64, version, evidence Hash) Hash {
	return Hash(common.Keccak256Concat(
		rewardLeafDomain,
		uint64Bytes(epochID),
		operator[:],
		uint64Bytes(score),
		uint64Bytes(allocation),
		version[:],
		evidence[:],
	))
}

// EvidenceHash commits to the complete raw aggregate input for one operator.
func EvidenceHash(epoch Epoch, input OperatorInput) Hash {
	return Hash(common.Keccak256Concat(
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
	))
}

// UsefulWorkLeafHash commits one operator's consensus useful-work record.
func UsefulWorkLeafHash(epochID uint64, operator Address, metric Metric) Hash {
	return Hash(common.Keccak256Concat(
		usefulWorkLeafDomain,
		uint64Bytes(epochID),
		operator[:],
		uint64Bytes(metric.Completed),
		uint64Bytes(metric.Total),
		metric.EvidenceHash[:],
	))
}

func usefulWorkConsensusMessage(
	sourceID string,
	chainID string,
	finalizedBlockHash Hash,
	finalizedBlockHeight uint64,
	epoch Epoch,
	scoringVersion Hash,
	eligibleRoot Hash,
	totalEligibleWeight uint64,
	usefulWorkRoot Hash,
) []byte {
	sourceHash := common.Keccak256Concat([]byte(sourceID))
	chainHash := common.Keccak256Concat([]byte(chainID))
	return concat(
		usefulWorkConsensusDomain,
		sourceHash[:],
		chainHash[:],
		finalizedBlockHash[:],
		uint64Bytes(finalizedBlockHeight),
		uint64Bytes(epoch.ID),
		uint64Bytes(epoch.StartUnix),
		uint64Bytes(epoch.EndUnix),
		uint64Bytes(epoch.StartBlock),
		uint64Bytes(epoch.EndBlock),
		scoringVersion[:],
		eligibleRoot[:],
		uint64Bytes(totalEligibleWeight),
		usefulWorkRoot[:],
	)
}

func finalizedInputCommitment(
	sourceID string,
	chainID string,
	finalizedBlockHash Hash,
	finalizedBlockHeight uint64,
	snapshot *Snapshot,
	usefulWorkRoot Hash,
) Hash {
	sourceHash := common.Keccak256Concat([]byte(sourceID))
	chainHash := common.Keccak256Concat([]byte(chainID))
	return Hash(common.Keccak256Concat(
		finalizedInputDomain,
		sourceHash[:],
		chainHash[:],
		finalizedBlockHash[:],
		uint64Bytes(finalizedBlockHeight),
		uint64Bytes(snapshot.Epoch.ID),
		uint64Bytes(snapshot.Epoch.StartUnix),
		uint64Bytes(snapshot.Epoch.EndUnix),
		uint64Bytes(snapshot.Epoch.StartBlock),
		uint64Bytes(snapshot.Epoch.EndBlock),
		snapshot.ScoringVersion[:],
		snapshot.EligibleRoot[:],
		uint64Bytes(snapshot.TotalEligibleWeight),
		usefulWorkRoot[:],
		snapshot.MerkleRoot[:],
		uint64Bytes(snapshot.TotalScore),
		uint64Bytes(snapshot.TotalAllocated),
	))
}
