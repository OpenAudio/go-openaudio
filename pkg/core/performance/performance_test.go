package performance

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/stretchr/testify/require"
)

func testAddress(t *testing.T, value string) Address {
	t.Helper()
	address, err := ParseAddress(value)
	require.NoError(t, err)
	return address
}

func testHash(value byte) Hash {
	var result Hash
	for i := range result {
		result[i] = value
	}
	return result
}

func testEpoch() Epoch {
	return Epoch{ID: 7, StartUnix: 1_700_000_000, EndUnix: 1_700_604_800, StartBlock: 100, EndBlock: 200}
}

func testInput(t *testing.T, operatorSuffix, signerSuffix string, weight uint64, storage, useful, blocks Metric) OperatorInput {
	t.Helper()
	return OperatorInput{
		Operator:        testAddress(t, "00000000000000000000000000000000000000"+operatorSuffix),
		Signer:          testAddress(t, "10000000000000000000000000000000000000"+signerSuffix),
		Weight:          weight,
		Storage:         storage,
		UsefulWork:      useful,
		BlockProduction: blocks,
	}
}

func metric(completed, total uint64, evidence byte) Metric {
	return Metric{Completed: completed, Total: total, EvidenceHash: testHash(evidence)}
}

func TestParseAddress(t *testing.T) {
	address, err := ParseAddress("  0XABCDEFabcdefABCDEFabcdefABCDEFabcdefABCD  ")
	require.NoError(t, err)
	require.Equal(t, "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd", address.String())

	for _, value := range []string{"", "0x1234", "0xgg00000000000000000000000000000000000000", "0x0000000000000000000000000000000000000000"} {
		_, err := ParseAddress(value)
		require.ErrorIs(t, err, ErrInvalidAddress, value)
	}
}

func TestScoreV1Boundaries(t *testing.T) {
	tests := []struct {
		name  string
		input OperatorInput
		want  uint64
	}{
		{"perfect", OperatorInput{Storage: metric(1, 1, 1), UsefulWork: metric(2, 2, 2), BlockProduction: metric(3, 3, 3)}, 10_000},
		{"zero", OperatorInput{Storage: metric(0, 1, 1), UsefulWork: metric(0, 1, 2), BlockProduction: metric(0, 1, 3)}, 0},
		{"missing component is zero", OperatorInput{Storage: metric(1, 1, 1), UsefulWork: metric(0, 0, 2), BlockProduction: metric(1, 1, 3)}, 6_666},
		{"floors components and mean", OperatorInput{Storage: metric(1, 3, 1), UsefulWork: metric(2, 3, 2), BlockProduction: metric(1, 6, 3)}, 3_888},
		{"large safe ratio", OperatorInput{Storage: metric(math.MaxUint64, math.MaxUint64, 1), UsefulWork: metric(1, 1, 2), BlockProduction: metric(1, 1, 3)}, 10_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScoreV1(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	_, err := ScoreV1(OperatorInput{Storage: metric(2, 1, 1)})
	require.ErrorIs(t, err, ErrInvalidMetric)
}

func TestFloorMulDivBoundaries(t *testing.T) {
	got, err := FloorMulDiv(EpochBudget, 1, 3)
	require.NoError(t, err)
	require.Equal(t, uint64(3_333_333_333_333), got)

	got, err = FloorMulDiv(math.MaxUint64, math.MaxUint64, math.MaxUint64)
	require.NoError(t, err)
	require.Equal(t, uint64(math.MaxUint64), got)

	_, err = FloorMulDiv(1, 1, 0)
	require.ErrorIs(t, err, ErrInvalidMetric)
	_, err = FloorMulDiv(math.MaxUint64, math.MaxUint64, 1)
	require.ErrorIs(t, err, ErrArithmeticOverflow)
}

func TestMerkleProofsOddEvenAndTampering(t *testing.T) {
	for count := 1; count <= 8; count++ {
		leaves := make([]Hash, count)
		for i := range leaves {
			leaves[i] = Hash(common.Keccak256Concat([]byte{byte(i + 1)}))
		}
		tree, err := NewTree(leaves)
		require.NoError(t, err)
		for i, leaf := range leaves {
			proof, err := tree.Proof(i)
			require.NoError(t, err)
			require.True(t, VerifyProof(leaf, proof, tree.Root()), "count=%d index=%d", count, i)
			require.False(t, VerifyProof(Hash(common.Keccak256Concat([]byte("tampered"))), proof, tree.Root()))
		}
	}
	_, err := NewTree(nil)
	require.Error(t, err)
	tree, err := NewTree([]Hash{testHash(1)})
	require.NoError(t, err)
	_, err = tree.Proof(-1)
	require.Error(t, err)
	_, err = tree.Proof(1)
	require.Error(t, err)
}

func TestBuildSnapshotDeterministicAllocationAndProofs(t *testing.T) {
	inputs := []OperatorInput{
		testInput(t, "03", "03", 30, metric(0, 1, 1), metric(0, 1, 2), metric(0, 1, 3)),
		testInput(t, "01", "01", 10, metric(1, 1, 4), metric(1, 1, 5), metric(1, 1, 6)),
		testInput(t, "02", "02", 20, metric(1, 2, 7), metric(1, 2, 8), metric(1, 2, 9)),
	}
	snapshot, err := BuildSnapshot(testEpoch(), inputs)
	require.NoError(t, err)
	require.Equal(t, EpochBudget, snapshot.Budget)
	require.Equal(t, uint64(60), snapshot.TotalEligibleWeight)
	require.Equal(t, uint64(15_000), snapshot.TotalScore)
	require.Equal(t, uint64(9_999_999_999_999), snapshot.TotalAllocated)
	require.Equal(t, []uint64{10_000, 5_000, 0}, []uint64{snapshot.Entries[0].Score, snapshot.Entries[1].Score, snapshot.Entries[2].Score})
	require.Equal(t, []uint64{6_666_666_666_666, 3_333_333_333_333, 0}, []uint64{snapshot.Entries[0].Allocation, snapshot.Entries[1].Allocation, snapshot.Entries[2].Allocation})
	for _, entry := range snapshot.Entries {
		require.True(t, VerifyProof(entry.Leaf, entry.Proof, snapshot.MerkleRoot))
	}
	for _, signer := range snapshot.EligibleSigners {
		require.True(t, VerifyProof(signer.Leaf, signer.Proof, snapshot.EligibleRoot))
	}

	reversed := slices.Clone(inputs)
	slices.Reverse(reversed)
	again, err := BuildSnapshot(testEpoch(), reversed)
	require.NoError(t, err)
	require.Equal(t, snapshot, again)
}

func TestBuildSnapshotZeroScores(t *testing.T) {
	snapshot, err := BuildSnapshot(testEpoch(), []OperatorInput{
		testInput(t, "01", "01", 1, metric(0, 0, 1), metric(0, 0, 2), metric(0, 0, 3)),
	})
	require.NoError(t, err)
	require.Zero(t, snapshot.TotalScore)
	require.Zero(t, snapshot.TotalAllocated)
	require.Zero(t, snapshot.Entries[0].Allocation)
}

func TestBuildSnapshotValidation(t *testing.T) {
	valid := testInput(t, "01", "01", 1, metric(1, 1, 1), metric(1, 1, 2), metric(1, 1, 3))
	tests := []struct {
		name    string
		epoch   Epoch
		inputs  []OperatorInput
		version Hash
		want    error
	}{
		{"short epoch", Epoch{StartUnix: 1, EndUnix: 2, StartBlock: 1, EndBlock: 2}, []OperatorInput{valid}, ScoringVersionV1(), ErrInvalidEpoch},
		{"empty block range", Epoch{StartUnix: 1, EndUnix: 1 + EpochDurationSeconds, StartBlock: 2, EndBlock: 2}, []OperatorInput{valid}, ScoringVersionV1(), ErrInvalidEpoch},
		{"empty inputs", testEpoch(), nil, ScoringVersionV1(), ErrInvalidMetric},
		{"unsupported version", testEpoch(), []OperatorInput{valid}, testHash(99), ErrUnsupportedVersion},
		{"zero weight", testEpoch(), []OperatorInput{func() OperatorInput { x := valid; x.Weight = 0; return x }()}, ScoringVersionV1(), ErrInvalidMetric},
		{"zero operator", testEpoch(), []OperatorInput{func() OperatorInput { x := valid; x.Operator = Address{}; return x }()}, ScoringVersionV1(), ErrInvalidAddress},
		{"duplicate operator", testEpoch(), []OperatorInput{valid, func() OperatorInput {
			x := valid
			x.Signer = testAddress(t, "1000000000000000000000000000000000000002")
			return x
		}()}, ScoringVersionV1(), ErrDuplicateOperator},
		{"duplicate signer", testEpoch(), []OperatorInput{valid, func() OperatorInput {
			x := valid
			x.Operator = testAddress(t, "0000000000000000000000000000000000000002")
			return x
		}()}, ScoringVersionV1(), ErrDuplicateSigner},
		{"weight overflow", testEpoch(), []OperatorInput{func() OperatorInput { x := valid; x.Weight = math.MaxUint64; return x }(), testInput(t, "02", "02", 1, metric(1, 1, 1), metric(1, 1, 2), metric(1, 1, 3))}, ScoringVersionV1(), ErrArithmeticOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildSnapshotForVersion(tt.epoch, tt.version, tt.inputs)
			require.Error(t, err)
			require.True(t, errors.Is(err, tt.want), "got %v", err)
		})
	}
}

func TestCrossLanguageGoldenVector(t *testing.T) {
	input := testInput(t, "01", "01", 10, metric(1, 2, 0x11), metric(2, 3, 0x22), metric(3, 4, 0x33))
	snapshot, err := BuildSnapshot(testEpoch(), []OperatorInput{input})
	require.NoError(t, err)
	// These constants are consumed by the Rust tests too. Changing any hash
	// encoding is a protocol change and must introduce a new domain/version.
	version := ScoringVersionV1()
	require.Equal(t, "28823611c1c6d274a4d71ab65ade7629644dfc5be8459c8edceda54ae7d01d2b", hex.EncodeToString(version[:]))
	require.Equal(t, "619efdf3ad4bbfad5ca8e6172aa1247fc27e8e5f91465f570b870ef6b3d8fa54", hex.EncodeToString(snapshot.EligibleRoot[:]))
	require.Equal(t, "5a8cf5249cbd175c9d782d840b3fc6770dd98c8be6f01db5cfc65ec4eec77751", hex.EncodeToString(snapshot.Entries[0].EvidenceHash[:]))
	require.Equal(t, "810ce6736b0210076f96a10e7f843acfcf0738d5c897d9257aca392826ccc0bd", hex.EncodeToString(snapshot.MerkleRoot[:]))
	var programID, configAccount [32]byte
	for i := range programID {
		programID[i] = byte(i)
		configAccount[i] = byte(32 + i)
	}
	require.Len(t, snapshot.CommitmentMessage(programID, configAccount), 251)
	require.Equal(t,
		"4f41505f504552464f524d414e43455f534e415053484f545f5631000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f0000000000000007000000006553f10000000000655d2b80000000000000006400000000000000c8619efdf3ad4bbfad5ca8e6172aa1247fc27e8e5f91465f570b870ef6b3d8fa54000000000000000a28823611c1c6d274a4d71ab65ade7629644dfc5be8459c8edceda54ae7d01d2b810ce6736b0210076f96a10e7f843acfcf0738d5c897d9257aca392826ccc0bd00000000000018f4000009184e72a000",
		hex.EncodeToString(snapshot.CommitmentMessage(programID, configAccount)),
	)
	commitmentHash := snapshot.CommitmentHash(programID, configAccount)
	require.Equal(t, "8fd92a4a73c4c1d8a7c54ed18fde09408aca9369b5abc58e8d3fafc628240d93", hex.EncodeToString(commitmentHash[:]))
}

type fixtureSource struct {
	inputs []OperatorInput
	err    error
}

func (s fixtureSource) PerformanceInputs(context.Context, Epoch) ([]OperatorInput, error) {
	return s.inputs, s.err
}

func TestGenerateSourceIntegration(t *testing.T) {
	input := testInput(t, "01", "01", 1, metric(1, 1, 1), metric(1, 1, 2), metric(1, 1, 3))
	want, err := BuildSnapshot(testEpoch(), []OperatorInput{input})
	require.NoError(t, err)
	got, err := Generate(context.Background(), fixtureSource{inputs: []OperatorInput{input}}, testEpoch(), ScoringVersionV1())
	require.NoError(t, err)
	require.Equal(t, want, got)
	_, err = Generate(context.Background(), nil, testEpoch(), ScoringVersionV1())
	require.Error(t, err)
	sentinel := errors.New("source failed")
	_, err = Generate(context.Background(), fixtureSource{err: sentinel}, testEpoch(), ScoringVersionV1())
	require.ErrorIs(t, err, sentinel)
}
