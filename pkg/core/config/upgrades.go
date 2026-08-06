package config

// This file is the height-gated consensus ruleset engine.
//
// A consensus rule can never simply be replaced: a node syncing from genesis
// re-executes all of history and must apply to each block exactly the rules
// that were live when the block was produced. Rules are therefore a function
// of block height, resolved through this file.
//
// Contract:
//
//   - Height comparisons live only in RulesetAt. Everything else consumes a
//     resolved Rules value and branches on behavior, never on height.
//   - An activation height of 0 means "never active" on that network.
//   - A rule that has governed even one block on a persistent network is
//     permanent: superseding it means adding a new entry at a new height,
//     not editing or removing the old behavior.
//   - The mempool admits transactions for the *next* block, so CheckTx-time
//     callers must resolve RulesetAt(currentHeight + 1).
//
// Schedules are keyed by chain ID and baked into the binary, so every
// validator that runs the same release resolves the same rules.

// UpgradeSchedule is the activation-height table for one network. Each field
// names an upgrade and holds the first height at which it is active (0 =
// never).
type UpgradeSchedule struct {
	// AuthEnforcementHeight activates authorization enforcement for
	// ManageEntity transactions: the signer must match the EIP-712 recovery
	// and be authorized (own wallet or active approved grant) against the
	// consensus auth state, or the transaction is rejected at the mempool and
	// proposal stages. It also closes proposal-level submission of genesis
	// migration transactions. Enforcement must not be scheduled on a
	// persistent network earlier than the height its auth state began being
	// tracked, or it will trust state built from unverified signers.
	AuthEnforcementHeight int64
}

// Rules is the resolved rule set for a single height: a flat description of
// active behaviors with no heights in sight.
type Rules struct {
	AuthEnforced bool
}

// RulesetAt resolves the rules governing the given block height. A nil
// schedule (tests, unknown chains) resolves to no active upgrades.
func (u *UpgradeSchedule) RulesetAt(height int64) Rules {
	if u == nil {
		return Rules{}
	}
	return Rules{
		AuthEnforced: activeAt(u.AuthEnforcementHeight, height),
	}
}

func activeAt(activation, height int64) bool {
	return activation != 0 && height >= activation
}

// upgradeSchedules maps genesis chain IDs (pkg/core/config/genesis/*.json) to
// their activation tables. Ephemeral networks (devnet, sandbox) activate new
// upgrades at height 1 so every local chain and integration test exercises
// them; persistent networks activate at explicitly chosen heights once all
// validators run a release that knows the entry.
var upgradeSchedules = map[string]*UpgradeSchedule{
	// dev
	"openaudio-devnet": {
		AuthEnforcementHeight: 1,
	},
	// sandbox
	"audius-devnet": {
		AuthEnforcementHeight: 1,
	},
	// stage
	"audius-testnet-alpha": {},
	// prod
	"audius-mainnet-alpha-beta": {},
}

// ScheduleForChainID returns the upgrade schedule for a chain ID. Unknown
// chain IDs get an empty schedule — no upgrades active — so a fresh or
// test-only chain never picks up activations meant for another network.
func ScheduleForChainID(chainID string) *UpgradeSchedule {
	if s, ok := upgradeSchedules[chainID]; ok {
		return s
	}
	return &UpgradeSchedule{}
}
