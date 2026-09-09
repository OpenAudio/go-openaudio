package config

import (
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/config/genesis"
)

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
	for _, chainID := range []string{"openaudio-devnet", "audius-devnet", "audius-mainnet-beta"} {
		rules := ScheduleForChainID(chainID).RulesetAt(1)
		if !rules.AuthEnforced {
			t.Fatalf("%s: auth enforcement should be active from height 1", chainID)
		}
		// Content auth is checked inside the manage-entity auth check, so it
		// only means something with both active.
		if !rules.ContentAuthEnforced {
			t.Fatalf("%s: content auth enforcement should be active from height 1", chainID)
		}
	}
	for _, chainID := range []string{"audius-testnet-alpha", "audius-mainnet-alpha-beta", "some-future-chain"} {
		if ScheduleForChainID(chainID).RulesetAt(1<<40) != (Rules{}) {
			t.Fatalf("%s: no upgrades should be active", chainID)
		}
	}
}

// Mediorum attests cids exactly on the chains where core will accept the
// attestation. The prod answer is whatever prod.json names: false while it is
// audius-mainnet-alpha-beta, true once the rollover build swaps in
// audius-mainnet-beta.
func TestContentAuthScheduledFollowsEmbeddedGenesis(t *testing.T) {
	for _, env := range []string{"dev", "sandbox", "stage", "prod"} {
		got, err := ContentAuthScheduled(env)
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		genDoc, err := genesis.Read(env)
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		want := ScheduleForChainID(genDoc.ChainID).RulesetAt(1).ContentAuthEnforced
		if got != want {
			t.Fatalf("%s (%s): ContentAuthScheduled = %v, want %v", env, genDoc.ChainID, got, want)
		}
	}
	// genesis.Read defaults unknown names to devnet; an unset or test-only
	// environment must not inherit devnet's gate through that fallback.
	for _, env := range []string{"", "test"} {
		got, err := ContentAuthScheduled(env)
		if err != nil {
			t.Fatalf("%q: %v", env, err)
		}
		if got {
			t.Fatalf("%q: content auth must not be scheduled for an unnamed environment", env)
		}
	}
}
