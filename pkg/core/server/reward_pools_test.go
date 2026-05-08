package server

import (
	"crypto/rand"
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
