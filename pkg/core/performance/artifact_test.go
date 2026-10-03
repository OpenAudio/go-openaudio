package performance

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func validArtifactFixture(t *testing.T) (*Artifact, []*ecdsa.PrivateKey) {
	t.Helper()
	input, keys := finalizedFixture(t, []uint64{40, 30, 30}, 0, 1)
	validated, err := ValidateFinalizedEpoch(input)
	require.NoError(t, err)
	artifact, err := BuildArtifact(validated, testSolanaPubkey(1), testSolanaPubkey(33))
	require.NoError(t, err)
	return artifact, keys
}

func TestArtifactRoundTripPersistenceAndConflict(t *testing.T) {
	artifact, _ := validArtifactFixture(t)
	data, err := MarshalArtifact(artifact)
	require.NoError(t, err)
	require.True(t, bytes.HasSuffix(data, []byte("\n")))
	decoded, err := DecodeArtifact(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, artifact, decoded)

	path := filepath.Join(t.TempDir(), "published", "epoch-7.json")
	require.NoError(t, PersistArtifact(path, artifact))
	require.NoError(t, PersistArtifact(path, artifact))
	loaded, err := LoadArtifactFile(path)
	require.NoError(t, err)
	require.Equal(t, artifact, loaded)

	conflicting := *artifact
	conflicting.Source.ChainID = "other-chain"
	conflicting.Source.InputCommitment = finalizedInputCommitment(
		conflicting.Source.SourceID,
		conflicting.Source.ChainID,
		conflicting.Source.FinalizedBlockHash,
		conflicting.Source.FinalizedBlockHeight,
		conflicting.Snapshot,
		conflicting.Source.UsefulWorkRoot,
	)
	require.ErrorIs(t, PersistArtifact(path, &conflicting), ErrArtifactConflict)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestArtifactValidationRejectsEveryProjectionTamper(t *testing.T) {
	artifact, _ := validArtifactFixture(t)
	tests := []struct {
		name   string
		mutate func(*Artifact)
	}{
		{"input commitment", func(value *Artifact) { value.Source.InputCommitment[0] ^= 1 }},
		{"useful quorum", func(value *Artifact) { value.Source.UsefulWorkAttestedWeight = 1 }},
		{"eligible leaf", func(value *Artifact) { value.Snapshot.EligibleSigners[0].Leaf[0] ^= 1 }},
		{"eligible proof", func(value *Artifact) { value.Snapshot.EligibleSigners[0].Proof[0][0] ^= 1 }},
		{"reward leaf", func(value *Artifact) { value.Snapshot.Entries[0].Leaf[0] ^= 1 }},
		{"reward proof", func(value *Artifact) { value.Snapshot.Entries[0].Proof[0][0] ^= 1 }},
		{"open args", func(value *Artifact) { value.Solana.Open.Args.EndBlock++ }},
		{"attest message", func(value *Artifact) { value.Solana.Attest.MessageHex = "00" }},
		{"attest hash", func(value *Artifact) { value.Solana.Attest.MessageHash[0] ^= 1 }},
		{"finalize", func(value *Artifact) { value.Solana.Finalize.EpochID++ }},
		{"claim", func(value *Artifact) { value.Solana.Claims[0].Args.Allocation++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := cloneArtifact(t, artifact)
			tt.mutate(copy)
			require.Error(t, ValidateArtifact(copy))
		})
	}
}

func TestSignArtifactProducesExactSolanaPayload(t *testing.T) {
	artifact, keys := validArtifactFixture(t)
	attestation, err := SignArtifact(artifact, keys[0])
	require.NoError(t, err)
	require.Equal(t, artifact.Snapshot.Epoch.ID, attestation.EpochID)
	require.Equal(t, artifact.Solana.Attest.MessageHex, attestation.MessageHex)
	require.Equal(t, artifact.Solana.Attest.Commitment, attestation.Commitment)
	require.True(t, VerifyProof(attestation.Eligibility.Leaf, attestation.Eligibility.Proof, artifact.Snapshot.EligibleRoot))
	message, err := hex.DecodeString(attestation.MessageHex)
	require.NoError(t, err)
	_, recovered, err := common.EthRecoverKeccak(attestation.Signature, message)
	require.NoError(t, err)
	require.True(t, strings.EqualFold(attestation.Signer.String(), recovered))
	require.NoError(t, ValidateSnapshotAttestation(artifact, attestation))
	tampered := *attestation
	tampered.Signature = "abcd"
	require.Error(t, ValidateSnapshotAttestation(artifact, &tampered))
	tampered = *attestation
	tampered.Eligibility.Proof = append([]Hash(nil), attestation.Eligibility.Proof...)
	tampered.Eligibility.Proof[0][0] ^= 1
	require.Error(t, ValidateSnapshotAttestation(artifact, &tampered))

	nonEligible, err := gethcrypto.HexToECDSA("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	_, err = SignArtifact(artifact, nonEligible)
	require.Error(t, err)
	_, err = SignArtifact(artifact, nil)
	require.Error(t, err)
}

func TestArtifactGoldenCommitmentAndRelayerFields(t *testing.T) {
	input := testInput(t, "01", "01", 10, metric(1, 2, 0x11), metric(2, 3, 0x22), metric(3, 4, 0x33))
	snapshot, err := BuildSnapshot(testEpoch(), []OperatorInput{input})
	require.NoError(t, err)
	programID := make([]byte, 32)
	configAccount := make([]byte, 32)
	for i := range programID {
		programID[i] = byte(i)
		configAccount[i] = byte(32 + i)
	}
	// Golden test uses the same raw key bytes mirrored in the Rust tests. The
	// all-zero first key is valid there as a Pubkey, so construct the artifact
	// projection directly around the protocol message instead of BuildArtifact's
	// deployment guard.
	message := snapshot.CommitmentMessage([32]byte(programID), [32]byte(configAccount))
	require.Len(t, message, 251)
	messageHash := common.Keccak256Concat(message)
	require.Equal(t, "8fd92a4a73c4c1d8a7c54ed18fde09408aca9369b5abc58e8d3fafc628240d93", hex.EncodeToString(messageHash[:]))
	require.Equal(t, "810ce6736b0210076f96a10e7f843acfcf0738d5c897d9257aca392826ccc0bd", snapshot.MerkleRoot.String()[2:])
}

func TestArtifactRejectsInvalidSolanaKeysAndTrailingJSON(t *testing.T) {
	input, _ := finalizedFixture(t, []uint64{100}, 0)
	validated, err := ValidateFinalizedEpoch(input)
	require.NoError(t, err)
	for _, value := range []string{"", "not-base58", "1"} {
		_, err := BuildArtifact(validated, value, testSolanaPubkey(33))
		require.Error(t, err)
	}
	artifact, _ := validArtifactFixture(t)
	data, err := MarshalArtifact(artifact)
	require.NoError(t, err)
	_, err = DecodeArtifact(bytes.NewReader(append(data, []byte("{}")...)))
	require.Error(t, err)
}

func cloneArtifact(t *testing.T, artifact *Artifact) *Artifact {
	t.Helper()
	data, err := MarshalArtifact(artifact)
	require.NoError(t, err)
	clone, err := DecodeArtifact(bytes.NewReader(data))
	require.NoError(t, err)
	clone.Snapshot.EligibleSigners = slices.Clone(clone.Snapshot.EligibleSigners)
	clone.Snapshot.Entries = slices.Clone(clone.Snapshot.Entries)
	clone.Solana.Claims = slices.Clone(clone.Solana.Claims)
	return clone
}
