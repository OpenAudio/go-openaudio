package common

import (
	"bytes"
	"crypto/sha256"
	"io"
	"sort"
)

type NodeTuple struct {
	addr  string
	score []byte
}

type NodeTuples []NodeTuple

func (s NodeTuples) Len() int      { return len(s) }
func (s NodeTuples) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s NodeTuples) Less(i, j int) bool {
	c := bytes.Compare(s[i].score, s[j].score)
	if c == 0 {
		return s[i].addr < s[j].addr
	}
	return c == -1
}

// Returns the first `size` number of addresses from a list of all validators sorted
// by a hashing function. The hashing is seeded according to the given key.
func GetAttestorRendezvous(validatorAddresses []string, key []byte, size int) map[string]bool {
	tuples := make(NodeTuples, len(validatorAddresses))

	hasher := sha256.New()
	for i, addr := range validatorAddresses {
		hasher.Reset()
		io.WriteString(hasher, addr)
		hasher.Write(key)
		tuples[i] = NodeTuple{addr, hasher.Sum(nil)}
	}
	sort.Sort(tuples)
	result := make(map[string]bool, len(validatorAddresses))
	bound := min(len(tuples), size)
	for i, tup := range tuples {
		if i >= bound {
			break
		}
		result[tup.addr] = true
	}
	return result
}
