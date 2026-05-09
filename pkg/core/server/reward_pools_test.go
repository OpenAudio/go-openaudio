package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/mr-tron/base58/base58"
)

// TestValidateRewardsManagerPubkey_BaseChecks covers shape rejection:
// empty, surrounding whitespace, non-base58, and wrong byte length.
// The AUDIO denylist is exercised separately by
// TestValidateRewardsManagerPubkey_AudioDenylist.
func TestValidateRewardsManagerPubkey_BaseChecks(t *testing.T) {
	good := freshSolanaPubkeyForTest(t)

	cases := []struct {
		name    string
		pubkey  string
		wantErr string
	}{
		{"empty", "", "is required"},
		{"leading whitespace", " " + good, "whitespace"},
		{"trailing whitespace", good + " ", "whitespace"},
		{"non-base58", "this!is@not%base58", "not valid base58"},
		{"too few bytes", base58.Encode([]byte("short")), "must decode to 32 bytes"},
		{"good", good, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRewardsManagerPubkey(tc.pubkey)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected pass; got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q; got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateRewardsManagerPubkey_AudioDenylist verifies that a CreateRewardPool
// targeting the configured AUDIO RM is refused. We swap in a test-only
// AUDIO RM via the per-env var (this test runs in the default 'prod'
// runtime environment per GetRuntimeEnvironment's fallback), so we set
// ProdAudioRewardsManagerPubkey for the duration of the test.
func TestValidateRewardsManagerPubkey_AudioDenylist(t *testing.T) {
	// Two distinct, well-formed pubkeys: one will be the configured AUDIO RM
	// (denylisted), the other an arbitrary launchpad RM (allowed).
	audioRM := freshSolanaPubkeyForTest(t)
	otherRM := freshSolanaPubkeyForTest(t)

	// Save and restore so the test is hermetic.
	prevDev := config.DevAudioRewardsManagerPubkey
	prevStage := config.StageAudioRewardsManagerPubkey
	prevProd := config.ProdAudioRewardsManagerPubkey
	defer func() {
		config.DevAudioRewardsManagerPubkey = prevDev
		config.StageAudioRewardsManagerPubkey = prevStage
		config.ProdAudioRewardsManagerPubkey = prevProd
	}()
	// Set all three so the test passes regardless of which env happens to be
	// resolved (default is "prod" per GetRuntimeEnvironment).
	config.DevAudioRewardsManagerPubkey = audioRM
	config.StageAudioRewardsManagerPubkey = audioRM
	config.ProdAudioRewardsManagerPubkey = audioRM

	if err := validateRewardsManagerPubkey(audioRM); err == nil {
		t.Fatalf("expected AUDIO RM to be rejected by denylist")
	} else if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected 'reserved' error; got %v", err)
	}

	if err := validateRewardsManagerPubkey(otherRM); err != nil {
		t.Fatalf("expected non-AUDIO RM to pass; got %v", err)
	}
}

func freshSolanaPubkeyForTest(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base58.Encode(b[:])
}

// TestVerifyRewardPoolOwnerSignature exercises the ed25519 proof-of-RM-
// keypair-possession that gates CreateRewardPool. Frontrunning defense
// rests on this check: the verification key IS the rm_pubkey, so
// possession of the matching secret is the only way to produce a valid
// signature.
func TestVerifyRewardPoolOwnerSignature(t *testing.T) {
	const chainID = "audius-test-1"

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	rmPubkey := base58.Encode(pub)

	// Foreign keypair representing an attacker who controls a different
	// RM and tries to reuse their signature against ours.
	foreignPub, foreignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey foreign: %v", err)
	}
	_ = foreignPub
	authorities := []string{"0xAbCdEf0000000000000000000000000000000001"}

	t.Run("valid signature passes", func(t *testing.T) {
		sig := rewards.SignCreateRewardPool(priv, chainID, rmPubkey, authorities)
		if err := verifyRewardPoolOwnerSignature(chainID, rmPubkey, authorities, sig); err != nil {
			t.Fatalf("expected valid signature to verify; got %v", err)
		}
	})

	t.Run("authority canonicalization is invariant", func(t *testing.T) {
		// Sign with one ordering/case, verify against another ordering/case.
		// Both sides canonicalize before producing/verifying bytes, so
		// equivalent inputs must produce verifiable signatures.
		sigUpper := rewards.SignCreateRewardPool(priv, chainID, rmPubkey, []string{strings.ToUpper(authorities[0])})
		if err := verifyRewardPoolOwnerSignature(chainID, rmPubkey, []string{"  " + strings.ToLower(authorities[0]) + "  "}, sigUpper); err != nil {
			t.Fatalf("canonicalization should make signatures invariant under case/whitespace; got %v", err)
		}
	})

	t.Run("signature signed by foreign keypair fails", func(t *testing.T) {
		// Attacker signs a payload claiming our rmPubkey, using their own
		// (foreign) key. ed25519.Verify against our rmPubkey must reject.
		sig := ed25519.Sign(foreignPriv, rewards.CanonicalCreateRewardPoolPayload(chainID, rmPubkey, authorities))
		err := verifyRewardPoolOwnerSignature(chainID, rmPubkey, authorities, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected ErrRewardPoolOwnerSignatureInvalid; got %v", err)
		}
	})

	t.Run("signature over different chain_id fails", func(t *testing.T) {
		sig := rewards.SignCreateRewardPool(priv, "wrong-chain", rmPubkey, authorities)
		err := verifyRewardPoolOwnerSignature(chainID, rmPubkey, authorities, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected cross-chain replay rejected; got %v", err)
		}
	})

	t.Run("signature over different authorities fails", func(t *testing.T) {
		sig := rewards.SignCreateRewardPool(priv, chainID, rmPubkey, []string{"0xAbCdEf0000000000000000000000000000000099"})
		err := verifyRewardPoolOwnerSignature(chainID, rmPubkey, authorities, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected mismatched-authorities signature rejected; got %v", err)
		}
	})

	t.Run("malformed signature length fails", func(t *testing.T) {
		err := verifyRewardPoolOwnerSignature(chainID, rmPubkey, authorities, []byte("too-short"))
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected short-signature rejected; got %v", err)
		}
	})

	t.Run("non-base58 rm_pubkey fails", func(t *testing.T) {
		sig := rewards.SignCreateRewardPool(priv, chainID, rmPubkey, authorities)
		err := verifyRewardPoolOwnerSignature(chainID, "not!base58", authorities, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected invalid-pubkey rejected; got %v", err)
		}
	})

	t.Run("rm_pubkey wrong byte length fails", func(t *testing.T) {
		shortPubkey := base58.Encode([]byte("short"))
		sig := rewards.SignCreateRewardPool(priv, chainID, shortPubkey, authorities)
		err := verifyRewardPoolOwnerSignature(chainID, shortPubkey, authorities, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected wrong-length rm_pubkey rejected; got %v", err)
		}
	})
}
