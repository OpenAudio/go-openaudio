package common

import (
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestProtoSignRecover_Roundtrip exercises every signed body type through
// ProtoSign + ProtoRecover. Whichever key signed, ProtoRecover returns its
// eth address.
func TestProtoSignRecover_Roundtrip(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := crypto.PubkeyToAddress(priv.PublicKey).Hex()

	t.Run("RewardBody_Create", func(t *testing.T) {
		body := &corev1.RewardBody{
			DeadlineBlockHeight: 999_999,
			Action: &corev1.RewardBody_Create{Create: &corev1.CreateReward{
				RewardId:             "r-1",
				Name:                 "Reward 1",
				Amount:               1000,
				RewardsManagerPubkey: "DJj6F8oHLQM7Ec7FNh3sKWHJDZG7uH1zH7bPq3p1mUe2",
			}},
		}
		sig, err := ProtoSign(priv, body)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		got, err := ProtoRecover(body, sig)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !strings.EqualFold(got, signer) {
			t.Fatalf("recovered %q, want %q", got, signer)
		}
	})

	t.Run("RewardBody_Delete", func(t *testing.T) {
		body := &corev1.RewardBody{
			DeadlineBlockHeight: 100,
			Action:              &corev1.RewardBody_Delete{Delete: &corev1.DeleteReward{Address: "0xreward"}},
		}
		sig, err := ProtoSign(priv, body)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		got, err := ProtoRecover(body, sig)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !strings.EqualFold(got, signer) {
			t.Fatalf("recovered %q, want %q", got, signer)
		}
	})

	t.Run("RewardPoolBody_Create", func(t *testing.T) {
		body := &corev1.RewardPoolBody{
			DeadlineBlockHeight: 100,
			Action: &corev1.RewardPoolBody_Create{Create: &corev1.CreateRewardPool{
				RewardsManagerPubkey: "DJj6F8oHLQM7Ec7FNh3sKWHJDZG7uH1zH7bPq3p1mUe2",
				Authorities: []string{signer},
			}},
		}
		sig, err := ProtoSign(priv, body)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		got, err := ProtoRecover(body, sig)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !strings.EqualFold(got, signer) {
			t.Fatalf("recovered %q, want %q", got, signer)
		}
	})

	t.Run("RewardPoolBody_SetAuthorities", func(t *testing.T) {
		body := &corev1.RewardPoolBody{
			DeadlineBlockHeight: 100,
			Action: &corev1.RewardPoolBody_SetAuthorities{SetAuthorities: &corev1.SetRewardPoolAuthorities{
				RewardsManagerPubkey: "DJj6F8oHLQM7Ec7FNh3sKWHJDZG7uH1zH7bPq3p1mUe2",
				Authorities: []string{"0xnew"},
			}},
		}
		sig, err := ProtoSign(priv, body)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		got, err := ProtoRecover(body, sig)
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !strings.EqualFold(got, signer) {
			t.Fatalf("recovered %q, want %q", got, signer)
		}
	})
}

// TestProtoSign_OneofTagDiscriminatesActions: two RewardPoolBodies — Create
// vs SetAuthorities — produce different signed bytes. The body's oneof field
// tag encodes which action variant is set, so even with otherwise-identical
// inner data the bytes (and thus signatures) diverge.
func TestProtoSign_OneofTagDiscriminatesActions(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	createBody := &corev1.RewardPoolBody{
		DeadlineBlockHeight: 1,
		Action:              &corev1.RewardPoolBody_Create{Create: &corev1.CreateRewardPool{Authorities: []string{"0xa"}}},
	}
	setBody := &corev1.RewardPoolBody{
		DeadlineBlockHeight: 1,
		Action:              &corev1.RewardPoolBody_SetAuthorities{SetAuthorities: &corev1.SetRewardPoolAuthorities{RewardsManagerPubkey: "p", Authorities: []string{"0xa"}}},
	}
	createSig, err := ProtoSign(priv, createBody)
	if err != nil {
		t.Fatalf("sign create: %v", err)
	}
	setSig, err := ProtoSign(priv, setBody)
	if err != nil {
		t.Fatalf("sign set: %v", err)
	}
	if createSig == setSig {
		t.Fatalf("envelope-signed Create and SetAuthorities must differ; both are %q", createSig)
	}
}

// TestProtoSign_TamperingBreaksSignature: changing any field of the body
// (deadline, inner action data, etc.) after signing causes ProtoRecover to
// return a different (or no) signer. Replay with a tampered body fails.
func TestProtoSign_TamperingBreaksSignature(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := crypto.PubkeyToAddress(priv.PublicKey).Hex()

	body := &corev1.RewardPoolBody{
		DeadlineBlockHeight: 100,
		Action: &corev1.RewardPoolBody_SetAuthorities{SetAuthorities: &corev1.SetRewardPoolAuthorities{
			RewardsManagerPubkey: "DJj6F8oHLQM7Ec7FNh3sKWHJDZG7uH1zH7bPq3p1mUe2",
			Authorities:          []string{signer, "0xnew"},
		}},
	}
	sig, err := ProtoSign(priv, body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Tamper: extend the deadline.
	tampered := &corev1.RewardPoolBody{
		DeadlineBlockHeight: 999_999,
		Action:              body.Action,
	}
	got, _ := ProtoRecover(tampered, sig)
	if strings.EqualFold(got, signer) {
		t.Fatalf("tampered deadline should not recover the original signer, got %q", got)
	}
}
