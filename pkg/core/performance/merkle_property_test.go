package performance

import (
	"encoding/hex"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/stretchr/testify/require"
)

// These constants are mirrored by performance-rewards/src/merkle.rs. Any
// change is an on-chain protocol change and requires a new domain/version.
func TestCrossLanguageMerkleGoldenVectors(t *testing.T) {
	operator := testAddress(t, "0000000000000000000000000000000000000001")
	signer := testAddress(t, "1000000000000000000000000000000000000001")
	eligible := EligibleLeafHash(signer, operator, 10)
	evidence, err := ParseHash("5a8cf5249cbd175c9d782d840b3fc6770dd98c8be6f01db5cfc65ec4eec77751")
	require.NoError(t, err)
	reward := RewardLeafHash(7, operator, 6388, EpochBudget, ScoringVersionV1(), evidence)
	pair, err := NewTree([]Hash{eligible, reward})
	require.NoError(t, err)
	pairRoot := pair.Root()
	require.Equal(t, "805afe76c9354161245bfcf47f809ba6da198ac05237de516a2e6547d74f2be6", hex.EncodeToString(pairRoot[:]))

	leaves := []Hash{
		Hash(common.Keccak256Concat([]byte("a"))),
		Hash(common.Keccak256Concat([]byte("b"))),
		Hash(common.Keccak256Concat([]byte("c"))),
	}
	require.Equal(t, "3ac225168df54212a25c1c01fd35bebfea408fdac2e31ddd6f80a4bbf9a5f1cb", hex.EncodeToString(leaves[0][:]))
	require.Equal(t, "b5553de315e0edf504d9150af82dafa5c4667fa618ed0a6f19c69b41166c5510", hex.EncodeToString(leaves[1][:]))
	require.Equal(t, "0b42b6393c1f53060fe3ddbfcd7aadcca894465a5a438f69c87d790b2299b9b2", hex.EncodeToString(leaves[2][:]))
	odd, err := NewTree(leaves)
	require.NoError(t, err)
	root := odd.Root()
	require.Equal(t, "5842148bc6ebeb52af882a317c765fccd3ae80589b21a9b8cbf21abb630e46a7", hex.EncodeToString(root[:]))
	wantProofs := [][]string{
		{"b5553de315e0edf504d9150af82dafa5c4667fa618ed0a6f19c69b41166c5510", "0b42b6393c1f53060fe3ddbfcd7aadcca894465a5a438f69c87d790b2299b9b2"},
		{"3ac225168df54212a25c1c01fd35bebfea408fdac2e31ddd6f80a4bbf9a5f1cb", "0b42b6393c1f53060fe3ddbfcd7aadcca894465a5a438f69c87d790b2299b9b2"},
		{"805b21d846b189efaeb0377d6bb0d201b3872a363e607c25088f025b0c6ae1f8"},
	}
	for i, leaf := range leaves {
		proof, err := odd.Proof(i)
		require.NoError(t, err)
		require.Len(t, proof, len(wantProofs[i]))
		for j := range proof {
			require.Equal(t, wantProofs[i][j], hex.EncodeToString(proof[j][:]))
		}
		require.True(t, VerifyProof(leaf, proof, root))
	}
}

func TestMerkleMalformedProofProperties(t *testing.T) {
	leaves := make([]Hash, 7)
	for i := range leaves {
		leaves[i] = Hash(common.Keccak256Concat([]byte("malformed-proof"), []byte{byte(i)}))
	}
	tree, err := NewTree(leaves)
	require.NoError(t, err)
	for i, leaf := range leaves {
		proof, err := tree.Proof(i)
		require.NoError(t, err)
		require.True(t, VerifyProof(leaf, proof, tree.Root()))

		wrongLeaf := leaf
		wrongLeaf[0] ^= 0x80
		require.False(t, VerifyProof(wrongLeaf, proof, tree.Root()))
		wrongRoot := tree.Root()
		wrongRoot[31] ^= 1
		require.False(t, VerifyProof(leaf, proof, wrongRoot))
		if len(proof) > 0 {
			truncated := append([]Hash(nil), proof[:len(proof)-1]...)
			require.False(t, VerifyProof(leaf, truncated, tree.Root()))
			mutated := append([]Hash(nil), proof...)
			mutated[0][0] ^= 1
			require.False(t, VerifyProof(leaf, mutated, tree.Root()))
		}
		appended := append(append([]Hash(nil), proof...), testHash(0xee))
		require.False(t, VerifyProof(leaf, appended, tree.Root()))
	}
}

func FuzzMerkleProofProperties(f *testing.F) {
	for _, seed := range [][]byte{{}, {1}, {1, 2, 3}, []byte("cross-language-merkle")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		count := 1
		if len(data) > 0 {
			count += int(data[0] % 31)
		}
		leaves := make([]Hash, count)
		for i := range leaves {
			leaves[i] = Hash(common.Keccak256Concat([]byte("OAP_FUZZ_LEAF"), []byte{byte(i)}, data))
		}
		tree, err := NewTree(leaves)
		if err != nil {
			t.Fatal(err)
		}
		root := tree.Root()
		for i, leaf := range leaves {
			proof, err := tree.Proof(i)
			if err != nil {
				t.Fatal(err)
			}
			if !VerifyProof(leaf, proof, root) {
				t.Fatalf("valid proof rejected: count=%d index=%d", count, i)
			}
			mutatedLeaf := leaf
			mutatedLeaf[(i+len(data))%len(mutatedLeaf)] ^= 1
			if VerifyProof(mutatedLeaf, proof, root) {
				t.Fatalf("mutated leaf accepted: count=%d index=%d", count, i)
			}
			mutatedRoot := root
			mutatedRoot[(i*7)%len(mutatedRoot)] ^= 1
			if VerifyProof(leaf, proof, mutatedRoot) {
				t.Fatalf("mutated root accepted: count=%d index=%d", count, i)
			}
			if len(proof) > 0 {
				mutatedProof := append([]Hash(nil), proof...)
				mutatedProof[len(mutatedProof)-1][0] ^= 1
				if VerifyProof(leaf, mutatedProof, root) {
					t.Fatalf("mutated proof accepted: count=%d index=%d", count, i)
				}
			}
		}
	})
}
