package performance

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58/base58"
	"github.com/stretchr/testify/require"
)

func finalizedFixture(t *testing.T, weights []uint64, signerIndexes ...int) (*FinalizedEpochInput, []*ecdsa.PrivateKey) {
	t.Helper()
	input := &FinalizedEpochInput{
		SchemaVersion:        FinalizedEpochSchemaVersion,
		SourceID:             "core-useful-work-v1",
		ChainID:              "audius-mainnet-beta",
		Epoch:                testEpoch(),
		ScoringVersion:       ScoringVersionV1(),
		FinalizedBlockHash:   testHash(0xf0),
		FinalizedBlockHeight: 250,
		Operators:            make([]OperatorInput, len(weights)),
	}
	keys := make([]*ecdsa.PrivateKey, len(weights))
	for i, weight := range weights {
		key, err := gethcrypto.HexToECDSA(fmt.Sprintf("%064x", i+1))
		require.NoError(t, err)
		keys[i] = key
		signer, err := ParseAddress(gethcrypto.PubkeyToAddress(key.PublicKey).Hex())
		require.NoError(t, err)
		operator := testAddress(t, fmt.Sprintf("%040x", i+101))
		input.Operators[i] = OperatorInput{
			Operator:        operator,
			Signer:          signer,
			Weight:          weight,
			Storage:         metric(uint64(i+1), uint64(i+2), byte(0x10+i)),
			UsefulWork:      metric(uint64(i), uint64(i+1), byte(0x20+i)),
			BlockProduction: metric(uint64(i+2), uint64(i+3), byte(0x30+i)),
		}
	}
	payload, err := PrepareUsefulWorkConsensus(input)
	require.NoError(t, err)
	input.UsefulWork.Root = payload.Root
	for _, index := range signerIndexes {
		attestation, err := SignUsefulWorkConsensus(input, keys[index])
		require.NoError(t, err)
		input.UsefulWork.Attestations = append(input.UsefulWork.Attestations, *attestation)
	}
	return input, keys
}

func testSolanaPubkey(seed byte) string {
	value := make([]byte, 32)
	for i := range value {
		value[i] = seed + byte(i)
	}
	return base58.Encode(value)
}

func TestFinalizedEpochWeightedConsensus(t *testing.T) {
	input, _ := finalizedFixture(t, []uint64{40, 30, 30}, 0, 1)
	validated, err := ValidateFinalizedEpoch(input)
	require.NoError(t, err)
	require.Equal(t, uint64(70), validated.attestedWeight)
	require.Equal(t, input.UsefulWork.Root, validated.usefulWorkRoot)
	require.NotEmpty(t, validated.consensusBytes)

	exactTwoThirds, _ := finalizedFixture(t, []uint64{1, 1, 1}, 0, 1)
	_, err = ValidateFinalizedEpoch(exactTwoThirds)
	require.ErrorIs(t, err, ErrInvalidConsensus)

	oneOver, _ := finalizedFixture(t, []uint64{1, 1, 1}, 0, 1, 2)
	_, err = ValidateFinalizedEpoch(oneOver)
	require.NoError(t, err)
}

func TestFinalizedEpochRejectsMalformedConsensus(t *testing.T) {
	valid, keys := finalizedFixture(t, []uint64{40, 30, 30}, 0, 1)
	tests := []struct {
		name   string
		mutate func(*FinalizedEpochInput)
		want   error
	}{
		{"wrong root", func(input *FinalizedEpochInput) { input.UsefulWork.Root[0] ^= 1 }, ErrInvalidConsensus},
		{"missing attestations", func(input *FinalizedEpochInput) { input.UsefulWork.Attestations = nil }, ErrInvalidConsensus},
		{"duplicate signer", func(input *FinalizedEpochInput) { input.UsefulWork.Attestations[1] = input.UsefulWork.Attestations[0] }, ErrInvalidConsensus},
		{"wrong recovered signer", func(input *FinalizedEpochInput) { input.UsefulWork.Attestations[0].Signer = input.Operators[2].Signer }, ErrInvalidConsensus},
		{"malformed signature", func(input *FinalizedEpochInput) { input.UsefulWork.Attestations[0].Signature = "abcd" }, ErrInvalidConsensus},
		{"non-eligible signer", func(input *FinalizedEpochInput) {
			key, err := gethcrypto.HexToECDSA(fmt.Sprintf("%064x", 99))
			require.NoError(t, err)
			signer, err := ParseAddress(gethcrypto.PubkeyToAddress(key.PublicKey).Hex())
			require.NoError(t, err)
			input.UsefulWork.Attestations[0].Signer = signer
		}, ErrInvalidConsensus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copy := *valid
			copy.Operators = slices.Clone(valid.Operators)
			copy.UsefulWork.Attestations = slices.Clone(valid.UsefulWork.Attestations)
			tt.mutate(&copy)
			_, err := ValidateFinalizedEpoch(&copy)
			require.ErrorIs(t, err, tt.want)
		})
	}

	wrongKey := *valid
	wrongKey.UsefulWork.Attestations = slices.Clone(valid.UsefulWork.Attestations)
	attestation, err := SignUsefulWorkConsensus(valid, keys[2])
	require.NoError(t, err)
	wrongKey.UsefulWork.Attestations[0].Signature = attestation.Signature
	_, err = ValidateFinalizedEpoch(&wrongKey)
	require.ErrorIs(t, err, ErrInvalidConsensus)
}

func TestFinalizedEpochFailsClosedOnMissingInputs(t *testing.T) {
	valid, _ := finalizedFixture(t, []uint64{100}, 0)
	tests := []struct {
		name   string
		mutate func(*FinalizedEpochInput)
		want   error
	}{
		{"nil input", nil, ErrInvalidMetric},
		{"schema", func(input *FinalizedEpochInput) { input.SchemaVersion++ }, nil},
		{"source id", func(input *FinalizedEpochInput) { input.SourceID = " source" }, nil},
		{"chain id", func(input *FinalizedEpochInput) { input.ChainID = "" }, nil},
		{"finalized hash", func(input *FinalizedEpochInput) { input.FinalizedBlockHash = Hash{} }, nil},
		{"finalized height", func(input *FinalizedEpochInput) { input.FinalizedBlockHeight = input.Epoch.EndBlock - 1 }, nil},
		{"no operators", func(input *FinalizedEpochInput) { input.Operators = nil }, ErrInvalidMetric},
		{"storage total", func(input *FinalizedEpochInput) { input.Operators[0].Storage.Total = 0 }, ErrInvalidMetric},
		{"useful work total", func(input *FinalizedEpochInput) { input.Operators[0].UsefulWork.Total = 0 }, ErrInvalidMetric},
		{"block total", func(input *FinalizedEpochInput) { input.Operators[0].BlockProduction.Total = 0 }, ErrInvalidMetric},
		{"useful evidence", func(input *FinalizedEpochInput) { input.Operators[0].UsefulWork.EvidenceHash = Hash{} }, ErrInvalidMetric},
		{"completed exceeds total", func(input *FinalizedEpochInput) { input.Operators[0].UsefulWork.Completed = math.MaxUint64 }, ErrInvalidMetric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mutate == nil {
				_, err := ValidateFinalizedEpoch(nil)
				require.Error(t, err)
				return
			}
			copy := *valid
			copy.Operators = slices.Clone(valid.Operators)
			copy.UsefulWork.Attestations = slices.Clone(valid.UsefulWork.Attestations)
			tt.mutate(&copy)
			_, err := ValidateFinalizedEpoch(&copy)
			require.Error(t, err)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
			}
		})
	}
}

func TestFileSourceStrictRegistrationAndJSON(t *testing.T) {
	input, _ := finalizedFixture(t, []uint64{100}, 0)
	data, err := json.Marshal(input)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "epoch.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	loaded, err := (FileSource{Path: path, ExpectedSourceID: input.SourceID}).LoadFinalizedEpoch(context.Background())
	require.NoError(t, err)
	require.Equal(t, input, loaded)
	_, err = (FileSource{Path: path, ExpectedSourceID: "other"}).LoadFinalizedEpoch(context.Background())
	require.ErrorIs(t, err, ErrSourceMismatch)

	unknown := append(bytes.TrimSuffix(data, []byte("}")), []byte(",\"unknown\":true}")...)
	_, err = DecodeFinalizedEpoch(bytes.NewReader(unknown))
	require.Error(t, err)
	_, err = DecodeFinalizedEpoch(bytes.NewReader(append(data, []byte(" {}")...)))
	require.Error(t, err)
	_, err = DecodeFinalizedEpoch(bytes.NewReader([]byte(`{"schema_version":1,"schema_version":1}`)))
	require.ErrorContains(t, err, "duplicate JSON object key")
	_, err = DecodeFinalizedEpoch(bytes.NewReader(bytes.Repeat([]byte(" "), MaxFinalizedEpochBytes+1)))
	require.ErrorIs(t, err, ErrManifestTooLarge)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (FileSource{Path: path, ExpectedSourceID: input.SourceID}).LoadFinalizedEpoch(cancelled)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGenerateArtifactEndToEndDeterministic(t *testing.T) {
	input, _ := finalizedFixture(t, []uint64{40, 30, 30}, 0, 1)
	writeInput := func(path string, value *FinalizedEpochInput) {
		data, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.json")
	writeInput(firstPath, input)
	first, err := GenerateArtifact(context.Background(), FileSource{Path: firstPath, ExpectedSourceID: input.SourceID}, testSolanaPubkey(1), testSolanaPubkey(33))
	require.NoError(t, err)

	reversed := *input
	reversed.Operators = slices.Clone(input.Operators)
	slices.Reverse(reversed.Operators)
	reversed.UsefulWork.Attestations = slices.Clone(input.UsefulWork.Attestations)
	slices.Reverse(reversed.UsefulWork.Attestations)
	secondPath := filepath.Join(directory, "second.json")
	writeInput(secondPath, &reversed)
	second, err := GenerateArtifact(context.Background(), FileSource{Path: secondPath, ExpectedSourceID: input.SourceID}, testSolanaPubkey(1), testSolanaPubkey(33))
	require.NoError(t, err)
	firstJSON, err := MarshalArtifact(first)
	require.NoError(t, err)
	secondJSON, err := MarshalArtifact(second)
	require.NoError(t, err)
	require.Equal(t, firstJSON, secondJSON)
}

func TestStrictSupermajorityBoundaries(t *testing.T) {
	require.False(t, hasStrictSupermajority(0, 0))
	require.False(t, hasStrictSupermajority(2, 3))
	require.True(t, hasStrictSupermajority(3, 3))
	require.False(t, hasStrictSupermajority(67, 101))
	require.True(t, hasStrictSupermajority(68, 101))
	require.True(t, hasStrictSupermajority(math.MaxUint64, math.MaxUint64))
	require.False(t, hasStrictSupermajority(math.MaxUint64, math.MaxUint64-1))
}

func TestConsensusSignaturesUseSharedKeccak(t *testing.T) {
	input, keys := finalizedFixture(t, []uint64{100}, 0)
	prepared, err := prepareFinalizedEpoch(input)
	require.NoError(t, err)
	attestation, err := SignUsefulWorkConsensus(input, keys[0])
	require.NoError(t, err)
	_, recovered, err := common.EthRecoverKeccak(attestation.Signature, prepared.message)
	require.NoError(t, err)
	require.True(t, strings.EqualFold(input.Operators[0].Signer.String(), recovered))
}
