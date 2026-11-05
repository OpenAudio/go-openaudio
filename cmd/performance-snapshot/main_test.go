package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/performance"
	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58/base58"
	"github.com/stretchr/testify/require"
)

const cliFixtureKey = "0000000000000000000000000000000000000000000000000000000000000001"

func cliHash(seed byte) performance.Hash {
	var hash performance.Hash
	for i := range hash {
		hash[i] = seed
	}
	return hash
}

func cliFixture(t *testing.T) (*performance.FinalizedEpochInput, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := gethcrypto.HexToECDSA(cliFixtureKey)
	require.NoError(t, err)
	signer, err := performance.ParseAddress(gethcrypto.PubkeyToAddress(key.PublicKey).Hex())
	require.NoError(t, err)
	operator, err := performance.ParseAddress("0x0000000000000000000000000000000000000011")
	require.NoError(t, err)
	input := &performance.FinalizedEpochInput{
		SchemaVersion:        performance.FinalizedEpochSchemaVersion,
		SourceID:             "core-useful-work-v1",
		ChainID:              "audius-mainnet-beta",
		Epoch:                performance.Epoch{ID: 7, StartUnix: 1_700_000_000, EndUnix: 1_700_604_800, StartBlock: 100, EndBlock: 200},
		ScoringVersion:       performance.ScoringVersionV1(),
		FinalizedBlockHash:   cliHash(0xf0),
		FinalizedBlockHeight: 250,
		Operators: []performance.OperatorInput{{
			Operator: operator,
			Signer:   signer,
			Weight:   100,
			Storage: performance.Metric{
				Completed: 1, Total: 2, EvidenceHash: cliHash(0x11),
			},
			UsefulWork: performance.Metric{
				Completed: 2, Total: 3, EvidenceHash: cliHash(0x22),
			},
			BlockProduction: performance.Metric{
				Completed: 3, Total: 4, EvidenceHash: cliHash(0x33),
			},
		}},
	}
	payload, err := performance.PrepareUsefulWorkConsensus(input)
	require.NoError(t, err)
	input.UsefulWork.Root = payload.Root
	attestation, err := performance.SignUsefulWorkConsensus(input, key)
	require.NoError(t, err)
	input.UsefulWork.Attestations = []performance.ConsensusAttestation{*attestation}
	return input, key
}

func cliPubkey(seed byte) string {
	value := make([]byte, 32)
	for i := range value {
		value[i] = seed + byte(i)
	}
	return base58.Encode(value)
}

func writeCLIFixture(t *testing.T, input *performance.FinalizedEpochInput) string {
	t.Helper()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "epoch.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func runCLI(t *testing.T, getenv func(string) string, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := newApp(&stdout, &stderr, getenv)
	err := app.Run(append([]string{"performance-snapshot"}, args...))
	return stdout.String(), err
}

func TestCommandEndToEndPrepareGenerateAndSign(t *testing.T) {
	input, _ := cliFixture(t)
	inputPath := writeCLIFixture(t, input)
	getenv := func(name string) string {
		if name == defaultPrivateKeyEnv {
			return cliFixtureKey
		}
		return ""
	}

	preparedJSON, err := runCLI(t, getenv,
		"prepare-input", "--input", inputPath, "--source-id", input.SourceID,
	)
	require.NoError(t, err)
	var prepared performance.UsefulWorkConsensusPayload
	require.NoError(t, json.Unmarshal([]byte(preparedJSON), &prepared))
	require.Equal(t, input.UsefulWork.Root, prepared.Root)
	require.NotEmpty(t, prepared.MessageHex)

	inputSignatureJSON, err := runCLI(t, getenv,
		"sign-input", "--input", inputPath, "--source-id", input.SourceID,
	)
	require.NoError(t, err)
	var inputSignature performance.ConsensusAttestation
	require.NoError(t, json.Unmarshal([]byte(inputSignatureJSON), &inputSignature))
	require.Equal(t, input.Operators[0].Signer, inputSignature.Signer)
	require.NotEmpty(t, inputSignature.Signature)

	artifactPath := filepath.Join(t.TempDir(), "published", "epoch-7.json")
	printedArtifact, err := runCLI(t, getenv,
		"generate",
		"--input", inputPath,
		"--source-id", input.SourceID,
		"--output", artifactPath,
		"--program-id", cliPubkey(1),
		"--config-account", cliPubkey(33),
		"--print",
	)
	require.NoError(t, err)
	artifactBytes, err := os.ReadFile(artifactPath)
	require.NoError(t, err)
	require.Equal(t, string(artifactBytes), printedArtifact)
	artifact, err := performance.LoadArtifactFile(artifactPath)
	require.NoError(t, err)
	require.Equal(t, performance.InstructionOpenFirstEpoch, artifact.Solana.Open.FirstEpochInstruction)
	require.Equal(t, performance.InstructionAttestSnapshot, artifact.Solana.Attest.Instruction)
	require.Equal(t, performance.InstructionFinalizeSnapshot, artifact.Solana.Finalize.NextEpochInstruction)
	require.Len(t, artifact.Solana.Claims, 1)

	snapshotSignatureJSON, err := runCLI(t, getenv,
		"sign-snapshot", "--artifact", artifactPath, "--output", "-",
	)
	require.NoError(t, err)
	var snapshotSignature performance.SnapshotAttestation
	require.NoError(t, json.Unmarshal([]byte(snapshotSignatureJSON), &snapshotSignature))
	require.Equal(t, artifact.Solana.Attest.MessageHex, snapshotSignature.MessageHex)
	require.Equal(t, input.Operators[0].Signer, snapshotSignature.Signer)

	// A repeated production invocation is byte-for-byte idempotent.
	_, err = runCLI(t, getenv,
		"generate",
		"--input", inputPath,
		"--source-id", input.SourceID,
		"--output", artifactPath,
		"--program-id", cliPubkey(1),
		"--config-account", cliPubkey(33),
	)
	require.NoError(t, err)
}

func TestCommandFailsClosed(t *testing.T) {
	input, _ := cliFixture(t)
	inputPath := writeCLIFixture(t, input)
	getenv := func(string) string { return "" }

	_, err := runCLI(t, getenv,
		"sign-input", "--input", inputPath, "--source-id", input.SourceID,
	)
	require.ErrorContains(t, err, defaultPrivateKeyEnv)

	output := filepath.Join(t.TempDir(), "must-not-exist.json")
	missing := *input
	missing.Operators = append([]performance.OperatorInput(nil), input.Operators...)
	missing.Operators[0].UsefulWork.Total = 0
	missingPath := writeCLIFixture(t, &missing)
	_, err = runCLI(t, getenv,
		"generate",
		"--input", missingPath,
		"--source-id", missing.SourceID,
		"--output", output,
		"--program-id", cliPubkey(1),
		"--config-account", cliPubkey(33),
	)
	require.Error(t, err)
	_, statErr := os.Stat(output)
	require.ErrorIs(t, statErr, os.ErrNotExist)

	_, err = runCLI(t, getenv,
		"prepare-input", "--input", inputPath, "--source-id", "unregistered-source",
	)
	require.ErrorIs(t, err, performance.ErrSourceMismatch)
}

func TestPrivateKeyEnvironmentValidation(t *testing.T) {
	_, err := privateKeyFromEnvironment(nil, defaultPrivateKeyEnv)
	require.Error(t, err)
	_, err = privateKeyFromEnvironment(func(string) string { return "not-a-key" }, defaultPrivateKeyEnv)
	require.Error(t, err)
	key, err := privateKeyFromEnvironment(func(string) string { return "0x" + cliFixtureKey }, defaultPrivateKeyEnv)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", key.D), "1")
}
