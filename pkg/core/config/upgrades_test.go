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

// Every known network ships with an empty schedule until a feature schedules
// itself, and unknown chain IDs must never inherit another network's
// activations.
func TestScheduleForChainID(t *testing.T) {
	for _, chainID := range []string{
		"openaudio-devnet", "audius-devnet",
		"audius-testnet-alpha", "audius-mainnet-alpha-beta",
		"some-future-chain",
	} {
		if ScheduleForChainID(chainID).RulesetAt(1<<40) != (Rules{}) {
			t.Fatalf("%s: no upgrades should be active yet", chainID)
		}
	}
}
