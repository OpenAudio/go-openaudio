package rewards

import (
	"crypto/md5"
	"encoding/hex"
	"sort"
	"strings"
)

// CanonicalAuthorities normalizes a list of eth addresses to the canonical
// form stored in core_reward_pools.authorities: lowercased, trimmed,
// deduplicated, non-empty entries, sorted ascending. Producers of pool
// authority sets canonicalize before write so containment checks against
// the gin index on authorities stay deterministic.
func CanonicalAuthorities(authorities []string) []string {
	seen := make(map[string]struct{}, len(authorities))
	out := make([]string, 0, len(authorities))
	for _, a := range authorities {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// MigratedPoolAddress returns the deterministic synthetic pool identifier
// for a set of claim authorities: "mig_" + md5(comma-joined canonical
// addresses). Used as the fallback when a CreateReward's authorities
// don't include any known launchpad-derived per-mint key — the pool
// exists so the reward can be authenticated against its own
// claim_authorities, but the synthetic identifier doesn't decode as a
// real Solana RM pubkey, so the validator's per-RM sender-attestation
// gate ignores it (synthetic pools never grant Solana sender
// registration).
func MigratedPoolAddress(authorities []string) string {
	canonical := CanonicalAuthorities(authorities)
	joined := strings.Join(canonical, ",")
	sum := md5.Sum([]byte(joined))
	return "mig_" + hex.EncodeToString(sum[:])
}
