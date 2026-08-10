package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// rewardSigningKeysEnvVar carries the reward pool authority keys as a JSON
// object of {"0xauthority": "privkeyhex"}.
//
// Deliberately NOT wired to a urfave/cli flag the way --private-key is, even
// though that is the local precedent for key material. A StringFlag with
// EnvVars accepts the value on argv too, and argv is world-readable through
// ps. The migration key can afford that — the writer generates one if it is
// not given, and it is thrown away at the end of the run. These are standing
// launchpad authority keys that keep signing for real pools after the
// migration, so they get env-or-file only.
const rewardSigningKeysEnvVar = "GENESIS_REWARD_SIGNING_KEYS"

// rewardSigningKeys maps a lowercased authority address to its private key.
type rewardSigningKeys map[string]*ecdsa.PrivateKey

// loadRewardSigningKeys reads the reward pool authority keys from a file, or
// from GENESIS_REWARD_SIGNING_KEYS if no file is given. Returns nil when
// neither is set; whether that is fatal is the caller's decision, because it
// depends on whether any signing is actually required.
//
// Keys are taken per authority address rather than as the launchpad's master
// deterministic secret on purpose. The secret derives every per-mint key for
// every mint through DeriveEthAddressForMint, which lives in the API repo and
// keys off the MINT — a value this writer has no mapping to, since
// launchpad_authority_rm maps authority to reward manager and not mint to
// reward manager. Taking the master secret would therefore mean either a
// cross-repo dependency or a copy of the derivation and a mint list, in
// exchange for handing the writer authority over every mint that has ever
// existed. Per-authority keys need none of that and expose exactly the pools
// being migrated.
func loadRewardSigningKeys(filePath string) (rewardSigningKeys, error) {
	var raw []byte

	switch {
	case filePath != "":
		b, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read reward signing keys file: %w", err)
		}
		raw = b
	default:
		v := os.Getenv(rewardSigningKeysEnvVar)
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		raw = []byte(v)
	}

	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("parse reward signing keys as a JSON object of {\"0xauthority\": \"privkeyhex\"}: %w", err)
	}

	keys := make(rewardSigningKeys, len(decoded))
	for declaredAddr, privHex := range decoded {
		if !ethcommon.IsHexAddress(strings.TrimSpace(declaredAddr)) {
			return nil, fmt.Errorf("reward signing keys: %q is not a valid eth address", declaredAddr)
		}
		b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(privHex), "0x"))
		if err != nil {
			return nil, fmt.Errorf("reward signing keys: private key for %s is not hex: %w", declaredAddr, err)
		}
		key, err := crypto.ToECDSA(b)
		if err != nil {
			return nil, fmt.Errorf("reward signing keys: private key for %s is not a valid secp256k1 key: %w", declaredAddr, err)
		}
		// The address is derived, not trusted. A transposed pair here would
		// otherwise sign every reward under one pool with another pool's key
		// and only surface as an authorization failure much later.
		derived := crypto.PubkeyToAddress(key.PublicKey)
		if !strings.EqualFold(derived.Hex(), strings.TrimSpace(declaredAddr)) {
			return nil, fmt.Errorf("reward signing keys: key listed under %s actually belongs to %s", declaredAddr, derived.Hex())
		}
		keys[strings.ToLower(derived.Hex())] = key
	}
	return keys, nil
}

// authorityAddresses returns the addresses the key set covers, for logging.
// Addresses only — never the keys.
func (k rewardSigningKeys) authorityAddresses() []string {
	out := make([]string, 0, len(k))
	for addr := range k {
		out = append(out, addr)
	}
	return out
}
