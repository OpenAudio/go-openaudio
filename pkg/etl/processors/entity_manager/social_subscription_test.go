package entity_manager

import (
	"context"
	"testing"
)

func TestSubscribe_TxType(t *testing.T) {
	h := Subscribe()
	if h.EntityType() != EntityTypeAny {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeAny)
	}
	if h.Action() != ActionSubscribe {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionSubscribe)
	}
}

func TestSubscribe_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xsubscriber", "subscriber")
	seedUser(t, pool, uid2, "0xpublisher", "publisher")

	params := buildParams(t, pool, EntityTypeUser, ActionSubscribe, uid1, uid2, "0xSubscriber", `{}`)
	mustHandle(t, Subscribe(), params)

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM subscriptions WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true",
		uid1, uid2).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestSubscribe_RejectsSelfSubscription(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xself", "self")
	params := buildParams(t, pool, EntityTypeUser, ActionSubscribe, uid, uid, "0xSelf", `{}`)
	mustReject(t, Subscribe(), params, "cannot subscribe to themselves")
}

func TestSubscribe_RejectsNonexistentTarget(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xsubscriber", "subscriber")
	params := buildParams(t, pool, EntityTypeUser, ActionSubscribe, uid, UserIDOffset+999, "0xSubscriber", `{}`)
	mustReject(t, Subscribe(), params, "does not exist")
}

func TestSubscribe_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xsubscriber", "subscriber")
	seedUser(t, pool, uid2, "0xpublisher", "publisher")

	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeUser, ActionSubscribe, uid1, uid2, "0xSubscriber", `{}`))
	mustReject(t, Subscribe(), buildParams(t, pool, EntityTypeUser, ActionSubscribe, uid1, uid2, "0xSubscriber", `{}`), "already exists")
}

func TestUnsubscribe_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xsubscriber", "subscriber")
	seedUser(t, pool, uid2, "0xpublisher", "publisher")

	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeUser, ActionSubscribe, uid1, uid2, "0xSubscriber", `{}`))
	mustHandle(t, Unsubscribe(), buildParams(t, pool, EntityTypeUser, ActionUnsubscribe, uid1, uid2, "0xSubscriber", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM subscriptions WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true",
		uid1, uid2).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestUnsubscribe_RejectsNoActiveSubscription(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xsubscriber", "subscriber")
	seedUser(t, pool, uid2, "0xpublisher", "publisher")
	params := buildParams(t, pool, EntityTypeUser, ActionUnsubscribe, uid1, uid2, "0xSubscriber", `{}`)
	mustReject(t, Unsubscribe(), params, "no active subscription")
}
