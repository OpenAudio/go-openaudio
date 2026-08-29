package performance

import (
	"bytes"
	"fmt"

	"github.com/OpenAudio/go-openaudio/pkg/common"
)

// Tree is a sorted-pair Keccak Merkle tree. An unpaired node is promoted to
// the next level unchanged, avoiding ambiguous synthetic duplicate leaves.
type Tree struct {
	levels [][]Hash
}

// NewTree creates a Merkle tree while preserving the supplied leaf order.
func NewTree(leaves []Hash) (*Tree, error) {
	if len(leaves) == 0 {
		return nil, fmt.Errorf("merkle tree requires at least one leaf")
	}
	level := append([]Hash(nil), leaves...)
	tree := &Tree{levels: [][]Hash{level}}
	for len(level) > 1 {
		next := make([]Hash, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
				continue
			}
			next = append(next, hashPair(level[i], level[i+1]))
		}
		tree.levels = append(tree.levels, next)
		level = next
	}
	return tree, nil
}

func hashPair(left, right Hash) Hash {
	if bytes.Compare(left[:], right[:]) > 0 {
		left, right = right, left
	}
	return Hash(common.Keccak256Concat(left[:], right[:]))
}

// Root returns the tree root.
func (t *Tree) Root() Hash { return t.levels[len(t.levels)-1][0] }

// Proof returns the sibling hashes needed to prove the leaf at index.
func (t *Tree) Proof(index int) ([]Hash, error) {
	if index < 0 || index >= len(t.levels[0]) {
		return nil, fmt.Errorf("merkle proof index %d out of bounds", index)
	}
	proof := make([]Hash, 0, len(t.levels)-1)
	for _, level := range t.levels[:len(t.levels)-1] {
		sibling := index ^ 1
		if sibling < len(level) {
			proof = append(proof, level[sibling])
		}
		index /= 2
	}
	return proof, nil
}

// VerifyProof verifies a sorted-pair Merkle proof.
func VerifyProof(leaf Hash, proof []Hash, root Hash) bool {
	current := leaf
	for _, sibling := range proof {
		current = hashPair(current, sibling)
	}
	return current == root
}
