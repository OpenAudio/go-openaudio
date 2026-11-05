package performance

import (
	"bytes"
	"fmt"
	"math/bits"
	"sort"
)

// BuildSnapshot builds a V1 snapshot with the protocol's fixed epoch budget.
func BuildSnapshot(epoch Epoch, inputs []OperatorInput) (*Snapshot, error) {
	return BuildSnapshotForVersion(epoch, ScoringVersionV1(), inputs)
}

// BuildSnapshotForVersion dispatches to immutable versioned scoring code.
func BuildSnapshotForVersion(epoch Epoch, version Hash, inputs []OperatorInput) (*Snapshot, error) {
	if err := validateEpoch(epoch); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: no eligible operators", ErrInvalidMetric)
	}

	ordered := append([]OperatorInput(nil), inputs...)
	sort.Slice(ordered, func(i, j int) bool {
		return bytes.Compare(ordered[i].Operator[:], ordered[j].Operator[:]) < 0
	})

	operatorSet := make(map[Address]struct{}, len(ordered))
	signerSet := make(map[Address]struct{}, len(ordered))
	entries := make([]Entry, len(ordered))
	eligible := make([]EligibleSigner, len(ordered))
	eligibleLeaves := make([]Hash, len(ordered))
	var totalWeight, totalScore uint64

	for i, input := range ordered {
		if input.Operator.IsZero() || input.Signer.IsZero() {
			return nil, ErrInvalidAddress
		}
		if input.Weight == 0 {
			return nil, fmt.Errorf("%w: zero signer weight", ErrInvalidMetric)
		}
		if _, exists := operatorSet[input.Operator]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateOperator, input.Operator)
		}
		if _, exists := signerSet[input.Signer]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateSigner, input.Signer)
		}
		operatorSet[input.Operator] = struct{}{}
		signerSet[input.Signer] = struct{}{}

		var carry uint64
		totalWeight, carry = bits.Add64(totalWeight, input.Weight, 0)
		if carry != 0 {
			return nil, ErrArithmeticOverflow
		}
		nodeScore, err := score(version, input)
		if err != nil {
			return nil, fmt.Errorf("score operator %s: %w", input.Operator, err)
		}
		totalScore, carry = bits.Add64(totalScore, nodeScore, 0)
		if carry != 0 {
			return nil, ErrArithmeticOverflow
		}

		eligibleLeaves[i] = EligibleLeafHash(input.Signer, input.Operator, input.Weight)
		eligible[i] = EligibleSigner{
			Signer:   input.Signer,
			Operator: input.Operator,
			Weight:   input.Weight,
			Leaf:     eligibleLeaves[i],
		}
		entries[i] = Entry{
			Operator:     input.Operator,
			Score:        nodeScore,
			Version:      version,
			EvidenceHash: EvidenceHash(epoch, input),
		}
	}

	eligibleTree, err := NewTree(eligibleLeaves)
	if err != nil {
		return nil, err
	}
	for i := range eligible {
		eligible[i].Proof, err = eligibleTree.Proof(i)
		if err != nil {
			return nil, err
		}
	}

	rewardLeaves := make([]Hash, len(entries))
	var totalAllocated uint64
	for i := range entries {
		if entries[i].Score > 0 && totalScore > 0 {
			entries[i].Allocation, err = FloorMulDiv(EpochBudget, entries[i].Score, totalScore)
			if err != nil {
				return nil, fmt.Errorf("allocate operator %s: %w", entries[i].Operator, err)
			}
		}
		var carry uint64
		totalAllocated, carry = bits.Add64(totalAllocated, entries[i].Allocation, 0)
		if carry != 0 || totalAllocated > EpochBudget {
			return nil, ErrArithmeticOverflow
		}
		entries[i].Leaf = RewardLeafHash(epoch.ID, entries[i].Operator, entries[i].Score, entries[i].Allocation, version, entries[i].EvidenceHash)
		rewardLeaves[i] = entries[i].Leaf
	}

	rewardTree, err := NewTree(rewardLeaves)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Proof, err = rewardTree.Proof(i)
		if err != nil {
			return nil, err
		}
	}

	return &Snapshot{
		Epoch:               epoch,
		Budget:              EpochBudget,
		ScoringVersion:      version,
		EligibleRoot:        eligibleTree.Root(),
		MerkleRoot:          rewardTree.Root(),
		TotalEligibleWeight: totalWeight,
		TotalScore:          totalScore,
		TotalAllocated:      totalAllocated,
		EligibleSigners:     eligible,
		Entries:             entries,
	}, nil
}

func validateEpoch(epoch Epoch) error {
	if epoch.EndUnix <= epoch.StartUnix || epoch.EndUnix-epoch.StartUnix != EpochDurationSeconds {
		return fmt.Errorf("%w: time range must be exactly %d seconds", ErrInvalidEpoch, EpochDurationSeconds)
	}
	if epoch.EndBlock <= epoch.StartBlock {
		return fmt.Errorf("%w: block range must be non-empty", ErrInvalidEpoch)
	}
	return nil
}

// FloorMulDiv returns floor(x*y/denominator) without overflowing uint64.
func FloorMulDiv(x, y, denominator uint64) (uint64, error) {
	if denominator == 0 {
		return 0, fmt.Errorf("%w: zero denominator", ErrInvalidMetric)
	}
	hi, lo := bits.Mul64(x, y)
	if hi >= denominator {
		return 0, ErrArithmeticOverflow
	}
	quotient, _ := bits.Div64(hi, lo, denominator)
	return quotient, nil
}
