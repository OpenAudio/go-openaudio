package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58/base58"
	"golang.org/x/crypto/sha3"
)

// Reward keys are derived rather than supplied, because they are not keys
// anyone holds: both of a pool's keys are pure functions of a launchpad
// deterministic secret and a mint. The ed25519 half's public key IS the
// rewards_manager_pubkey the chain carries for that mint's pool; the secp256k1
// half's address IS the pool's claim authority. There is no key file to hand
// over — there is a secret, a list of mints, and two algorithms.
//
// This is what makes rm_owner_signature producible. validateCreateRewardPool
// wants an ed25519 signature by the RM keypair over the body, proving the
// creator controls the RM rather than merely having observed its pubkey on
// Solana. Deriving the keypair here is the same thing the launchpad relay does
// when it creates a pool for real (see the API repo's cmd/create_reward_codes),
// so the migrated creates are authorized the same way live ones are, not by
// exemption.
//
// # Two secrets, not one
//
// The launchpad's deterministic secret has been rotated once. Both secrets
// are inputs here because production needs both and neither alone is enough:
// three of the four production pools have reward managers derived from the
// ORIGINAL secret, one from the ROTATED secret, and the current claim
// authorities are a mix. Assuming a single secret fails in both directions, and
// it fails silently — a wrong secret still produces perfectly valid keys, they
// simply authorize nothing. So each pool is matched against derivations from
// both secrets and a pool that matches neither stops the run by name.
//
// The derivations are duplicated from the API repo rather than imported: the
// two live in different modules, and a genesis writer taking a dependency on
// the API to migrate a chain would be the wrong direction.
// TestRMDerivationMatchesAPI and TestClaimAuthorityDerivationMatchesAPI pin the
// duplication to reference vectors generated from the API's own
// implementations, so a divergence fails a test rather than silently producing
// keys that authorize nothing.
// These names match rewards_cli's rotate-launchpad-secret deliberately. The two
// tools consume the same two secrets, and giving them different names invites
// setting one tool's variable and running the other: the derivations would then
// match no pool and the failure would read as a bad mint list rather than a
// misconfigured secret.
const (
	// launchpadSecretEnvVar is the ORIGINAL deterministic secret — the one the
	// Solana reward manager accounts were actually initialized under.
	launchpadSecretEnvVar = "LAUNCHPAD_OLD_SECRET"
	// launchpadRotatedSecretEnvVar is the post-rotation secret, which derives
	// the claim authorities the pools currently carry.
	launchpadRotatedSecretEnvVar = "LAUNCHPAD_NEW_SECRET"
	launchpadMintsEnvVar         = "LAUNCHPAD_MINTS"
)

// claimAuthorityDomain is the domain separator the API's
// utils.DeriveEthAddressForMint is called with for claim authorities.
var claimAuthorityDomain = []byte("claimAuthority")

// secretGeneration says which launchpad secret a derivation came from.
//
// This is not bookkeeping: it is how a phantom pool is identified. The reward
// managers on Solana were all initialized under the ORIGINAL secret, so a pool
// whose RM only derives from the ROTATED secret corresponds to no Solana
// account at all. See planRewardPools.
type secretGeneration int

const (
	secretOriginal secretGeneration = iota
	secretRotated
)

func (g secretGeneration) String() string {
	if g == secretRotated {
		return "rotated"
	}
	return "original"
}

func (g secretGeneration) envVar() string {
	if g == secretRotated {
		return launchpadRotatedSecretEnvVar
	}
	return launchpadSecretEnvVar
}

// launchpadIdentity is everything one (secret, mint) pair derives.
type launchpadIdentity struct {
	mint       string
	generation secretGeneration
	// rm is the base58 rewards_manager_pubkey — the public half of rmKey.
	rm    string
	rmKey ed25519.PrivateKey
	// authority is the EIP-55 claim authority address for this mint under this
	// secret, and authKey is its private key. It signs the pool create envelope
	// and every reward under that pool.
	authority string
	authKey   *ecdsa.PrivateKey
}

// launchpadKeys indexes every derived identity two ways: by the reward manager
// it produces (to find the RM signing key and the mint behind a pool) and by
// claim authority address (to find the key for an authority a pool row lists,
// whichever secret happens to have produced it).
type launchpadKeys struct {
	byRM map[string]launchpadIdentity
	// byAuthority is keyed by lowercased address. A pool's CURRENT authority
	// need not come from the same secret as its RM: the rotation moved pools
	// onto post-rotation authorities while their reward managers stayed as the
	// original secret derived them.
	byAuthority map[string]*ecdsa.PrivateKey
	// mints records the mint list the derivations covered, for logging.
	mints []string
}

// deriveRewardManagerKeypair reproduces the solana-relay's
// deriveKeypair('reward-manager', mint), whose public half is the
// rewards_manager_pubkey for that mint's pool.
//
// Seed material is
//
//	sha256(secret_utf8 || "audius-launchpad" || "reward-manager" || mint_bytes)
//
// where secret_utf8 is the UTF-8 bytes of the hex-encoded secret STRING, not
// the 32 bytes it decodes to. That asymmetry is load-bearing and easy to get
// backwards: deriveClaimAuthority, over the same secret, uses the DECODED
// bytes. Both spellings produce a perfectly well-formed keypair, so getting it
// wrong yields an RM pubkey that matches no pool — or worse, a signature that
// verifies against nothing and is only caught when a validator rejects the
// transaction. The reference vectors exist for this reason.
func deriveRewardManagerKeypair(secretHex string, mint []byte) ed25519.PrivateKey {
	var buf []byte
	buf = append(buf, []byte(secretHex)...)
	buf = append(buf, []byte("audius-launchpad")...)
	buf = append(buf, []byte("reward-manager")...)
	buf = append(buf, mint...)
	seed := sha256.Sum256(buf)
	return ed25519.NewKeyFromSeed(seed[:])
}

// deriveClaimAuthority reproduces the API's utils.DeriveEthAddressForMint with
// the "claimAuthority" domain: the secp256k1 key whose address is the pool's
// claim authority.
//
//	keccak256(domain || secret_bytes || mint_bytes || ctr)
//
// with ctr incremented until the digest is a valid secp256k1 scalar. Note the
// asymmetry against deriveRewardManagerKeypair: here the secret is consumed as
// the 32 bytes it DECODES to, not as its hex string.
//
// The counter loop is not decorative. A keccak digest is a valid private key
// only if it is a non-zero scalar below the curve order; the odds of a miss are
// negligible but the API retries rather than failing, and a port that dropped
// the retry would diverge on exactly the inputs nobody tests.
func deriveClaimAuthority(secretHex string, mint []byte) (string, *ecdsa.PrivateKey, error) {
	h := strings.TrimPrefix(secretHex, "0x")
	if len(h) != 64 {
		return "", nil, fmt.Errorf("launchpad secret must be 32-byte hex (64 characters); got %d characters", len(h))
	}
	secret, err := hex.DecodeString(h)
	if err != nil {
		return "", nil, fmt.Errorf("launchpad secret is not valid hex: %w", err)
	}

	for ctr := 0; ctr < 16; ctr++ {
		data := make([]byte, 0, len(claimAuthorityDomain)+len(secret)+len(mint)+1)
		data = append(data, claimAuthorityDomain...)
		data = append(data, secret...)
		data = append(data, mint...)
		data = append(data, byte(ctr))

		digest := sha3.NewLegacyKeccak256()
		digest.Write(data)
		priv := digest.Sum(nil)

		key, err := crypto.ToECDSA(priv)
		if err != nil {
			continue
		}
		return crypto.PubkeyToAddress(key.PublicKey).Hex(), key, nil
	}
	return "", nil, fmt.Errorf("could not derive a valid secp256k1 claim authority key")
}

// loadLaunchpadKeys derives both keys per (secret, mint) pair and indexes the
// results. Returns nil when no secret is configured; whether that is fatal is
// the caller's decision.
//
// Secrets come from the environment only, never a flag — they derive every
// per-mint key for every mint that has ever existed, so they must not reach
// argv where ps can read them.
func loadLaunchpadKeys(mintsFile string) (*launchpadKeys, error) {
	original := strings.TrimSpace(os.Getenv(launchpadSecretEnvVar))
	rotated := strings.TrimSpace(os.Getenv(launchpadRotatedSecretEnvVar))

	// Both are required. Either one alone derives half the key material: the
	// original owns the reward managers, the rotated one owns the authorities
	// the pools currently carry, and a run missing either fails later with a
	// per-pool error that reads like a bad mint list. Failing here names the
	// actual problem, before anything is read or written.
	var missing []string
	if original == "" {
		missing = append(missing, launchpadSecretEnvVar)
	}
	if rotated == "" {
		missing = append(missing, launchpadRotatedSecretEnvVar)
	}
	if len(missing) == 2 {
		// Neither set at all — the caller decides whether that is fatal, since
		// it depends on whether rewards are being migrated.
		return nil, nil
	}
	if len(missing) == 1 {
		return nil, fmt.Errorf("%s is not set; both launchpad secrets are required "+
			"(%s derives the reward managers, %s derives the authorities they carry)",
			missing[0], launchpadSecretEnvVar, launchpadRotatedSecretEnvVar)
	}

	mints, err := loadLaunchpadMints(mintsFile)
	if err != nil {
		return nil, err
	}
	if len(mints) == 0 {
		return nil, fmt.Errorf("%s is set but no mints were supplied: pass --launchpad-mints or set %s. "+
			"A secret alone derives nothing; the mint is the other half of every key",
			launchpadSecretEnvVar, launchpadMintsEnvVar)
	}

	keys := &launchpadKeys{
		byRM:        map[string]launchpadIdentity{},
		byAuthority: map[string]*ecdsa.PrivateKey{},
		mints:       mints,
	}

	type gen struct {
		generation secretGeneration
		secret     string
	}
	gens := []gen{{secretOriginal, original}, {secretRotated, rotated}}

	for _, g := range gens {
		// Both derivations require 32-byte hex; a secret of another length
		// would silently derive a whole parallel universe of keys that match
		// nothing, so it is rejected rather than normalized.
		if len(strings.TrimPrefix(g.secret, "0x")) != 64 {
			return nil, fmt.Errorf("%s must be 32-byte hex (64 characters); got %d characters",
				g.generation.envVar(), len(strings.TrimPrefix(g.secret, "0x")))
		}
		for _, m := range mints {
			raw, err := base58.Decode(m)
			if err != nil {
				return nil, fmt.Errorf("launchpad mint %q is not valid base58: %w", m, err)
			}
			if len(raw) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("launchpad mint %q decodes to %d bytes, want %d", m, len(raw), ed25519.PublicKeySize)
			}

			rmKey := deriveRewardManagerKeypair(g.secret, raw)
			rm := base58.Encode(rmKey.Public().(ed25519.PublicKey))
			authority, authKey, err := deriveClaimAuthority(g.secret, raw)
			if err != nil {
				return nil, fmt.Errorf("derive claim authority for mint %s under the %s secret: %w", m, g.generation, err)
			}

			// A collision would mean two (secret, mint) pairs deriving the same
			// reward manager, which cannot happen for distinct inputs and would
			// make "which secret produced this pool" ambiguous — the question
			// phantom detection turns on.
			if prior, ok := keys.byRM[rm]; ok {
				return nil, fmt.Errorf("reward manager %s derives from both (%s secret, mint %s) and "+
					"(%s secret, mint %s); the two secrets or the mint list must be wrong",
					rm, prior.generation, prior.mint, g.generation, m)
			}
			keys.byRM[rm] = launchpadIdentity{
				mint:       m,
				generation: g.generation,
				rm:         rm,
				rmKey:      rmKey,
				authority:  authority,
				authKey:    authKey,
			}
			keys.byAuthority[strings.ToLower(authority)] = authKey
		}
	}
	return keys, nil
}

// loadLaunchpadMints reads mints from a file if given, otherwise from the
// environment. Accepts newline, comma, or whitespace separation so a list
// pasted out of a psql query works without reformatting.
func loadLaunchpadMints(file string) ([]string, error) {
	var raw string
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read launchpad mints file: %w", err)
		}
		raw = string(b)
	} else {
		raw = os.Getenv(launchpadMintsEnvVar)
	}

	seen := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '"' || r == '\''
		}) {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read launchpad mints: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// identityForRM returns what the derivations know about a pool's reward
// manager: which mint and which secret produced it, and the keys.
func (k *launchpadKeys) identityForRM(rm string) (launchpadIdentity, bool) {
	if k == nil {
		return launchpadIdentity{}, false
	}
	id, ok := k.byRM[rm]
	return id, ok
}

// rmKeyForPool returns the derived reward manager key for a pool.
//
// A miss is fatal rather than a fallback to an unsigned create. The pool's RM
// is a real Solana account; being unable to re-derive it means the mint list is
// incomplete or neither secret is the one those accounts were created under,
// and both of those are answers the operator needs before a migration, not
// after.
func (k *launchpadKeys) rmKeyForPool(rm string) (ed25519.PrivateKey, error) {
	id, ok := k.identityForRM(rm)
	if !ok {
		return nil, fmt.Errorf("no launchpad mint derives reward manager %s under either the %s or the %s "+
			"secret: either the mint list is missing that pool's mint, or neither secret is the one its "+
			"reward manager account was created under", rm, launchpadSecretEnvVar, launchpadRotatedSecretEnvVar)
	}
	return id.rmKey, nil
}

// rmForMint returns the reward manager a mint derives under one secret.
func (k *launchpadKeys) rmForMint(mint string, generation secretGeneration) (string, bool) {
	if k == nil {
		return "", false
	}
	for rm, id := range k.byRM {
		if id.mint == mint && id.generation == generation {
			return rm, true
		}
	}
	return "", false
}

// authorityKeyFor returns the key for the first of a pool's authorities that
// the derivations cover, along with that authority's address as the pool lists
// it.
//
// A pool's authorities come off the old chain's core_reward_pools row, and the
// derivations have to meet them there rather than assume which secret produced
// them: the rotation left pools whose RM is original-secret and whose authority
// is rotated-secret.
func (k *launchpadKeys) authorityKeyFor(authorities []string) (*ecdsa.PrivateKey, string, error) {
	if k == nil {
		return nil, "", fmt.Errorf("no launchpad secret configured; set %s (and %s)",
			launchpadSecretEnvVar, launchpadRotatedSecretEnvVar)
	}
	for _, a := range authorities {
		trimmed := strings.TrimSpace(a)
		if key, ok := k.byAuthority[strings.ToLower(trimmed)]; ok {
			if !ethcommon.IsHexAddress(trimmed) {
				return nil, "", fmt.Errorf("authority %q is not a valid eth address", a)
			}
			return key, trimmed, nil
		}
	}
	return nil, "", fmt.Errorf("no launchpad mint derives any of the authorities %v under either the %s "+
		"or the %s secret", authorities, launchpadSecretEnvVar, launchpadRotatedSecretEnvVar)
}

// derivedManagers returns the reward managers the mint list covers, with the
// secret each came from, for logging. Public material only — never the keys.
func (k *launchpadKeys) derivedManagers() []string {
	if k == nil {
		return nil
	}
	out := make([]string, 0, len(k.byRM))
	for rm, id := range k.byRM {
		out = append(out, fmt.Sprintf("%s (%s secret, mint %s)", rm, id.generation, id.mint))
	}
	sort.Strings(out)
	return out
}

// derivedAuthorities returns the claim authority addresses the mint list
// covers, for logging. Addresses only — never the keys.
func (k *launchpadKeys) derivedAuthorities() []string {
	if k == nil {
		return nil
	}
	out := make([]string, 0, len(k.byAuthority))
	for addr := range k.byAuthority {
		out = append(out, addr)
	}
	sort.Strings(out)
	return out
}
