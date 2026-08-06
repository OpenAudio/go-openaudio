package config

import "testing"

// The boundary is the whole contract: a rule activates at exactly its
// activation height, never one block earlier.
func TestRulesetAtBoundary(t *testing.T) {
	u := &UpgradeSchedule{AuthEnforcementHeight: 100}

	if u.RulesetAt(99).AuthEnforced {
		t.Fatal("rule active one block before its activation height")
	}
	if !u.RulesetAt(100).AuthEnforced {
		t.Fatal("rule inactive at its activation height")
	}
	if !u.RulesetAt(101).AuthEnforced {
		t.Fatal("rule inactive after its activation height")
	}
}

// Height 0 means "never active", including at very large heights.
func TestRulesetAtZeroMeansNever(t *testing.T) {
	u := &UpgradeSchedule{AuthEnforcementHeight: 0}
	if u.RulesetAt(1).AuthEnforced || u.RulesetAt(1<<40).AuthEnforced {
		t.Fatal("zero activation height must mean never active")
	}
}

// A nil schedule (tests, manually built configs) resolves to no active
// upgrades instead of panicking.
func TestRulesetAtNilSchedule(t *testing.T) {
	var u *UpgradeSchedule
	if u.RulesetAt(1000) != (Rules{}) {
		t.Fatal("nil schedule must resolve to zero rules")
	}
}

// Ephemeral networks activate everything at height 1; persistent networks
// activate nothing until an explicit height is scheduled; unknown chain IDs
// must never inherit another network's activations.
func TestScheduleForChainID(t *testing.T) {
	for _, chainID := range []string{"openaudio-devnet", "audius-devnet"} {
		if !ScheduleForChainID(chainID).RulesetAt(1).AuthEnforced {
			t.Fatalf("%s: auth enforcement should be active from height 1", chainID)
		}
	}
	for _, chainID := range []string{"audius-testnet-alpha", "audius-mainnet-alpha-beta", "some-future-chain"} {
		if ScheduleForChainID(chainID).RulesetAt(1<<40) != (Rules{}) {
			t.Fatalf("%s: no upgrades should be active", chainID)
		}
	}
}
