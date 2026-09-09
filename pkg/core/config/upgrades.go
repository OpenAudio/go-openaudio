package config

import (
	"fmt"

	"github.com/OpenAudio/go-openaudio/pkg/core/config/genesis"
)

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

	// ContentAuthEnforcementHeight activates content authorization: a
	// FileUpload must carry a validator signature over the bound
	// UploadAttestation rather than the legacy unbound per-cid signature, and
	// a ManageEntity Track create/update may only assert cids that the
	// consensus content-auth state records as belonging to the acting user.
	//
	// Must not be scheduled on a persistent network earlier than the height
	// its content-auth state began being tracked, or tracks whose cids predate
	// the projection become unwritable. The genesis rollover is the natural
	// activation point: the migration replay seeds a cid for every track that
	// exists.
	ContentAuthEnforcementHeight int64
}

// Rules is the resolved rule set for a single height: a flat description of
// active behaviors with no heights in sight.
type Rules struct {
	AuthEnforced        bool
	ContentAuthEnforced bool
}

// RulesetAt resolves the rules governing the given block height. A nil
// schedule (tests, unknown chains) resolves to no active upgrades.
func (u *UpgradeSchedule) RulesetAt(height int64) Rules {
	if u == nil {
		return Rules{}
	}
	return Rules{
		AuthEnforced:        activeAt(u.AuthEnforcementHeight, height),
		ContentAuthEnforced: activeAt(u.ContentAuthEnforcementHeight, height),
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
		AuthEnforcementHeight:        1,
		ContentAuthEnforcementHeight: 1,
	},
	// sandbox
	"audius-devnet": {
		AuthEnforcementHeight:        1,
		ContentAuthEnforcementHeight: 1,
	},
	// stage
	"audius-testnet-alpha": {},
	// prod, audius-mainnet-alpha-beta: unscheduled, and must stay so. Its
	// tracks and grants predate the auth projections, so enforcing there
	// would make existing entities unwritable.
	"audius-mainnet-alpha-beta": {},
	// prod, audius-mainnet-beta: the genesis rollover. genesis-writer replays
	// the whole history as migration transactions, and the replay projects
	// auth state and a cid claim for every migrated track, so both
	// enforcements can be active from the first block. Height 1 also means
	// no live block is ever produced under the relaxed rules, so there is no
	// pre-enforcement window in which unverified state can accumulate.
	"audius-mainnet-beta": {
		AuthEnforcementHeight:        1,
		ContentAuthEnforcementHeight: 1,
	},
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

// ContentAuthScheduled reports whether the chain this environment's embedded
// genesis names has content authorization scheduled at all. Mediorum uses it
// to decide whether to require upload attribution and attest cids.
//
// Keyed on the chain rather than the environment because the same prod build
// serves audius-mainnet-alpha-beta until the genesis rollover and
// audius-mainnet-beta after it, and the two differ: attestations submitted
// before the gate are refused at the mempool, so a node that attested on the
// old chain would fail every audio upload at transcode completion. Reading the
// genesis makes the answer flip with the genesis swap and nothing else.
//
// Every scheduled activation is at height 1, so "scheduled" and "active" are
// the same question. If a persistent chain ever schedules a later height,
// mediorum should resolve RulesetAt for the next block instead.
//
// Only a named network resolves to a genesis. genesis.Read falls back to the
// devnet genesis for anything it does not recognize, which would turn content
// auth on for an unset or test-only environment; those get no gate.
func ContentAuthScheduled(environment string) (bool, error) {
	switch environment {
	case "prod", "production", "mainnet",
		"stage", "staging", "testnet",
		"sandbox",
		"dev", "development", "devnet", "local":
	default:
		return false, nil
	}
	genDoc, err := genesis.Read(environment)
	if err != nil {
		return false, fmt.Errorf("reading genesis for %q: %w", environment, err)
	}
	return ScheduleForChainID(genDoc.ChainID).ContentAuthEnforcementHeight != 0, nil
}
