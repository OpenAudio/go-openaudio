package rewards

import (
	"crypto/ed25519"
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

// CreateRewardPoolOwnerSignatureDomain is the domain-separation prefix
// used in the canonical CreateRewardPool authorization payload. Any
// future ed25519 signing scheme that re-uses an RM keypair MUST use a
// different domain prefix to avoid signature replay across schemes.
const CreateRewardPoolOwnerSignatureDomain = "audius:create-reward-pool:"

// CanonicalCreateRewardPoolPayload returns the bytes the RM keypair signs
// to authorize a CreateRewardPool transaction. Format:
//
//	"audius:create-reward-pool:" + chain_id + ":" + rm_pubkey_b58 +
//	":" + sorted_lowercased_authorities.join(",")
//
// Authorities are canonicalized so that an attacker cannot grind a
// different message-byte order to produce a different signature for the
// same logical authority set. chain_id is included to prevent cross-
// chain replay; rm_pubkey is included to bind the signature to the
// specific RM being registered.
//
// Both the validator (signature verification) and any client that
// produces signatures (launchpad relay, integration tests) must use this
// helper so encodings stay synchronized.
func CanonicalCreateRewardPoolPayload(chainID, rmPubkey string, authorities []string) []byte {
	canon := CanonicalAuthorities(authorities)
	var b strings.Builder
	b.WriteString(CreateRewardPoolOwnerSignatureDomain)
	b.WriteString(chainID)
	b.WriteByte(':')
	b.WriteString(rmPubkey)
	b.WriteByte(':')
	b.WriteString(strings.Join(canon, ","))
	return []byte(b.String())
}

// SignCreateRewardPool produces an ed25519 signature over the canonical
// authorization payload for a CreateRewardPool tx, suitable for placing
// in CreateRewardPool.RmOwnerSignature. Used by clients that hold the RM
// keypair (the launchpad relay deterministically derives it from
// (launchpadDeterministicSecret, mint); integration tests generate fresh
// keypairs via ed25519.GenerateKey).
func SignCreateRewardPool(privKey ed25519.PrivateKey, chainID, rmPubkey string, authorities []string) []byte {
	return ed25519.Sign(privKey, CanonicalCreateRewardPoolPayload(chainID, rmPubkey, authorities))
}
