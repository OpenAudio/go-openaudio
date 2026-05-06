package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
)

// TestTryParseLegacyReward_ProtoUnknownFieldRoundtrip exercises the heart of
// the wire-compat layer: when a legacy CreateReward (encoded against the old
// RewardMessage shape) is unmarshaled by the new RewardMessage proto, the
// new proto's Body is nil, but the original tag-1000 bytes survive as
// preserved unknown fields. tryParseLegacyReward re-marshals and re-decodes
// those unknown bytes as LegacyRewardMessage and recovers the action.
func TestTryParseLegacyReward_ProtoUnknownFieldRoundtrip(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	signer := crypto.PubkeyToAddress(priv.PublicKey).Hex()

	t.Run("LegacyCreateReward", func(t *testing.T) {
		legacyCreate := &corev1.LegacyCreateReward{
			RewardId:            "legacy-r-1",
			Name:                "Legacy 1",
			Amount:              42,
			ClaimAuthorities:    []*corev1.ClaimAuthority{{Address: signer, Name: "Owner"}},
			DeadlineBlockHeight: 999_999,
		}
		// Sign with the legacy scheme and stash the signature back on the proto.
		sig := signLegacyCreate(t, priv, legacyCreate)
		legacyCreate.Signature = sig

		legacyMsg := &corev1.LegacyRewardMessage{
			Action: &corev1.LegacyRewardMessage_Create{Create: legacyCreate},
		}
		// Marshal as legacy bytes — what an old node would have written to chain.
		legacyBytes, err := proto.Marshal(legacyMsg)
		if err != nil {
			t.Fatalf("marshal legacy: %v", err)
		}

		// Unmarshal those bytes into the *new* RewardMessage shape. Tag 1000 is
		// unknown to the new shape; proto-go preserves it as unknown fields.
		var newMsg corev1.RewardMessage
		if err := proto.Unmarshal(legacyBytes, &newMsg); err != nil {
			t.Fatalf("unmarshal new shape: %v", err)
		}
		if newMsg.Body != nil {
			t.Fatalf("expected new shape to decode with Body == nil")
		}

		// Wire-compat layer should recover the legacy action.
		recovered, err := tryParseLegacyReward(&newMsg)
		if err != nil {
			t.Fatalf("tryParseLegacyReward: %v", err)
		}
		if recovered == nil {
			t.Fatalf("expected legacy action to be recovered")
		}
		create := recovered.GetCreate()
		if create == nil {
			t.Fatalf("expected create action; got %T", recovered.Action)
		}
		if create.RewardId != "legacy-r-1" || create.Amount != 42 {
			t.Fatalf("recovered fields wrong: %+v", create)
		}

		// Signer recovery against the legacy signing scheme must yield the
		// original key.
		recoveredSigner, err := common.LegacyRecoverCreateReward(create)
		if err != nil {
			t.Fatalf("recover signer: %v", err)
		}
		if !strings.EqualFold(recoveredSigner, signer) {
			t.Fatalf("recovered signer %q want %q", recoveredSigner, signer)
		}
	})

	t.Run("LegacyDeleteReward", func(t *testing.T) {
		legacyDelete := &corev1.LegacyDeleteReward{
			Address:             "0xreward",
			DeadlineBlockHeight: 100,
		}
		sig := signLegacyDelete(t, priv, legacyDelete)
		legacyDelete.Signature = sig

		legacyMsg := &corev1.LegacyRewardMessage{
			Action: &corev1.LegacyRewardMessage_Delete{Delete: legacyDelete},
		}
		legacyBytes, err := proto.Marshal(legacyMsg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var newMsg corev1.RewardMessage
		if err := proto.Unmarshal(legacyBytes, &newMsg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		recovered, err := tryParseLegacyReward(&newMsg)
		if err != nil {
			t.Fatalf("tryParseLegacyReward: %v", err)
		}
		if recovered == nil || recovered.GetDelete() == nil {
			t.Fatalf("expected delete action recovered")
		}
		got, err := common.LegacyRecoverDeleteReward(recovered.GetDelete())
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if !strings.EqualFold(got, signer) {
			t.Fatalf("recovered %q want %q", got, signer)
		}
	})

	t.Run("NewShapeIsLeftAlone", func(t *testing.T) {
		// A genuine new-shape RewardMessage (Body populated) must NOT trip the
		// legacy path; tryParseLegacyReward returns (nil, nil) and the
		// caller's existing dispatch handles it.
		newMsg := &corev1.RewardMessage{
			Body: &corev1.RewardBody{
				DeadlineBlockHeight: 1,
				Action:              &corev1.RewardBody_Delete{Delete: &corev1.DeleteReward{Address: "0xnew"}},
			},
			Signature: "deadbeef",
		}
		recovered, err := tryParseLegacyReward(newMsg)
		if err != nil {
			t.Fatalf("err on new shape: %v", err)
		}
		if recovered != nil {
			t.Fatalf("legacy path triggered on new-shape message")
		}
	})

	t.Run("EmptyMessageIsNotLegacy", func(t *testing.T) {
		var empty corev1.RewardMessage
		recovered, err := tryParseLegacyReward(&empty)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if recovered != nil {
			t.Fatalf("expected nil for empty message; got %+v", recovered)
		}
	})
}

// TestIsValidRewardTransaction_RejectsLegacyAtValidate confirms the
// asymmetric gate: live-validation paths (CheckTx, ProcessProposal) refuse
// legacy-format reward txs even though FinalizeBlock still applies them
// during historical replay. This closes the loophole where an attacker
// could craft legacy bytes (with arbitrary inline claim_authorities) and
// bypass the new pool gate, since legacy CreateReward had no
// signer-membership check.
func TestIsValidRewardTransaction_RejectsLegacyAtValidate(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	cr := &corev1.LegacyCreateReward{
		RewardId:            "live-legacy",
		Name:                "should be rejected",
		Amount:              1,
		ClaimAuthorities:    []*corev1.ClaimAuthority{{Address: crypto.PubkeyToAddress(priv.PublicKey).Hex(), Name: "x"}},
		DeadlineBlockHeight: 999_999,
	}
	cr.Signature = signLegacyCreate(t, priv, cr)
	legacyMsg := &corev1.LegacyRewardMessage{Action: &corev1.LegacyRewardMessage_Create{Create: cr}}
	legacyBytes, err := proto.Marshal(legacyMsg)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	var newMsg corev1.RewardMessage
	if err := proto.Unmarshal(legacyBytes, &newMsg); err != nil {
		t.Fatalf("unmarshal new shape: %v", err)
	}

	signedTx := &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_Reward{Reward: &newMsg},
	}

	// Validate against an empty Server — we only need the dispatch to run far
	// enough to confirm legacy bytes are rejected before any DB access.
	s := &Server{}
	err = s.isValidRewardTransaction(context.Background(), signedTx, 1)
	if err == nil {
		t.Fatalf("expected legacy reward tx to be rejected at validate-time")
	}
	if !strings.Contains(err.Error(), "legacy reward wire format is not accepted") {
		t.Fatalf("expected legacy-rejection error, got: %v", err)
	}
}

func signLegacyCreate(t *testing.T, priv *ecdsa.PrivateKey, cr *corev1.LegacyCreateReward) string {
	t.Helper()
	data := common.LegacyDeterministicCreateRewardData(cr)
	bytes, err := hex.DecodeString(data)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	sig, err := common.EthSign(priv, bytes)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

func signLegacyDelete(t *testing.T, priv *ecdsa.PrivateKey, dr *corev1.LegacyDeleteReward) string {
	t.Helper()
	data := common.LegacyDeterministicDeleteRewardData(dr)
	bytes, err := hex.DecodeString(data)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	sig, err := common.EthSign(priv, bytes)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}
