package rewards

import (
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
