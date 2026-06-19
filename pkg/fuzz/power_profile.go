package fuzz

import (
	"fmt"
	"math/rand"
)

// SeededValidatorPowers returns deterministic, uneven voting power for
// simulated networks. The skew keeps generated chaos from quietly assuming
// node-count quorum is equivalent to validator-power quorum.
func SeededValidatorPowers(nodeCount int, seed int64) map[NodeID]int64 {
	nodeCount = clamp(nodeCount, 1, DefaultModelNodeLimit)
	rng := rand.New(rand.NewSource(seed))
	powers := make(map[NodeID]int64, nodeCount)
	for i := 0; i < nodeCount; i++ {
		id := NodeID(fmt.Sprintf("%s%d", modelDefaultNodePrefix, i+1))
		power := int64(1 + rng.Intn(25))
		switch {
		case i == 0 && nodeCount > 1:
			power += int64(nodeCount * 3)
		case (i+1)%17 == 0:
			power += int64(25 + rng.Intn(75))
		case (i+1)%43 == 0:
			power = 1
		}
		powers[id] = power
	}
	return powers
}
