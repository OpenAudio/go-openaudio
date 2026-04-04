package entity_manager

import (
	"context"
	"testing"
)

func TestTipReaction_TxType(t *testing.T) {
	h := TipReaction()
	if h.EntityType() != EntityTypeTip {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeTip)
	}
	if h.Action() != ActionUpdate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionUpdate)
	}
}

func TestTipReaction_Success(t *testing.T) {
	pool := setupTestDB(t)
	senderUID := int64(UserIDOffset + 1)
	receiverUID := int64(UserIDOffset + 2)
	seedUser(t, pool, senderUID, "0xsender", "sender")
	seedUser(t, pool, receiverUID, "0xreceiver", "receiver")

	// Seed a tip
	_, err := pool.Exec(context.Background(), `
		INSERT INTO user_tips (slot, signature, sender_user_id, receiver_user_id, amount)
		VALUES (1, 'tip-sig-123', $1, $2, 1000000)
	`, senderUID, receiverUID)
	if err != nil {
		t.Fatalf("seed tip: %v", err)
	}

	meta := `{"reacted_to":"tip-sig-123","reaction_value":1}`
	params := buildParams(t, pool, EntityTypeTip, ActionUpdate, receiverUID, 0, "0xReceiver", meta)
	mustHandle(t, TipReaction(), params)

	var reactionValue int
	var senderWallet string
	err = pool.QueryRow(context.Background(),
		"SELECT reaction_value, sender_wallet FROM reactions WHERE reacted_to = 'tip-sig-123'").Scan(&reactionValue, &senderWallet)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if reactionValue != 1 {
		t.Errorf("reaction_value = %d, want 1", reactionValue)
	}
	if senderWallet != "0xsender" {
		t.Errorf("sender_wallet = %q, want 0xsender", senderWallet)
	}
}

func TestTipReaction_RejectsInvalidValue(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xreceiver", "receiver")

	meta := `{"reacted_to":"some-sig","reaction_value":5}`
	params := buildParams(t, pool, EntityTypeTip, ActionUpdate, uid, 0, "0xReceiver", meta)
	mustReject(t, TipReaction(), params, "must be between 1 and 4")
}

func TestTipReaction_RejectsTipNotFound(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xreceiver", "receiver")

	meta := `{"reacted_to":"nonexistent-sig","reaction_value":1}`
	params := buildParams(t, pool, EntityTypeTip, ActionUpdate, uid, 0, "0xReceiver", meta)
	mustReject(t, TipReaction(), params, "tip with signature")
}

func TestTipReaction_RejectsMissingReactedTo(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xreceiver", "receiver")

	meta := `{"reaction_value":1}`
	params := buildParams(t, pool, EntityTypeTip, ActionUpdate, uid, 0, "0xReceiver", meta)
	mustReject(t, TipReaction(), params, "reacted_to is required")
}
