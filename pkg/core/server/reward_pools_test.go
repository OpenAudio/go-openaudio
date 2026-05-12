package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
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
	prevProd := config.ProdAudioRewardsManagerPubkey
	defer func() {
		config.DevAudioRewardsManagerPubkey = prevDev
		config.ProdAudioRewardsManagerPubkey = prevProd
	}()
	// Set both so the test passes regardless of which env happens to be
	// resolved (default is "prod" per GetRuntimeEnvironment).
	config.DevAudioRewardsManagerPubkey = audioRM
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
// signature over the body bytes.
func TestVerifyRewardPoolOwnerSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	rmPubkey := base58.Encode(pub)

	// Foreign keypair representing an attacker who controls a different
	// RM and tries to reuse their signature against ours.
	_, foreignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey foreign: %v", err)
	}

	// The validator hashes body bytes for verification — we don't care
	// what those bytes are in this unit test, just that the (sig, key,
	// bytes) triple matches.
	bodyBytes := []byte("body-marshal-bytes-stand-in")

	t.Run("valid signature passes", func(t *testing.T) {
		sig := ed25519.Sign(priv, bodyBytes)
		if err := verifyRewardPoolOwnerSignature(rmPubkey, bodyBytes, sig); err != nil {
			t.Fatalf("expected valid signature to verify; got %v", err)
		}
	})

	t.Run("signature signed by foreign keypair fails", func(t *testing.T) {
		// Foreign secret signs the same bytes; verify against our rmPubkey
		// must reject — verification key doesn't match the signing key.
		sig := ed25519.Sign(foreignPriv, bodyBytes)
		err := verifyRewardPoolOwnerSignature(rmPubkey, bodyBytes, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected ErrRewardPoolOwnerSignatureInvalid; got %v", err)
		}
	})

	t.Run("signature over different body bytes fails", func(t *testing.T) {
		sig := ed25519.Sign(priv, []byte("different-body"))
		err := verifyRewardPoolOwnerSignature(rmPubkey, bodyBytes, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected mismatched-body signature rejected; got %v", err)
		}
	})

	t.Run("malformed signature length fails", func(t *testing.T) {
		err := verifyRewardPoolOwnerSignature(rmPubkey, bodyBytes, []byte("too-short"))
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected short-signature rejected; got %v", err)
		}
	})

	t.Run("non-base58 rm_pubkey fails", func(t *testing.T) {
		sig := ed25519.Sign(priv, bodyBytes)
		err := verifyRewardPoolOwnerSignature("not!base58", bodyBytes, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected invalid-pubkey rejected; got %v", err)
		}
	})

	t.Run("rm_pubkey wrong byte length fails", func(t *testing.T) {
		shortPubkey := base58.Encode([]byte("short"))
		sig := ed25519.Sign(priv, bodyBytes)
		err := verifyRewardPoolOwnerSignature(shortPubkey, bodyBytes, sig)
		if !errors.Is(err, ErrRewardPoolOwnerSignatureInvalid) {
			t.Fatalf("expected wrong-length rm_pubkey rejected; got %v", err)
		}
	})
}
