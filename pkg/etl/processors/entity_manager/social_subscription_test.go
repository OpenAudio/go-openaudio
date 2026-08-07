package entity_manager

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
	var entityType string
	var entityID int64
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete, entity_type, COALESCE(entity_id, -1) FROM subscriptions WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true",
		uid1, uid2).Scan(&isDelete, &entityType, &entityID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
	if entityType != EntityTypeUser {
		t.Errorf("entity_type = %q, want %q", entityType, EntityTypeUser)
	}
	if entityID != -1 {
		t.Errorf("entity_id = %d, want NULL", entityID)
	}
}

func TestSubscribe_EventSuccess(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 10)
	hostID := int64(UserIDOffset + 11)
	eventID := int64(7001)

	seedUser(t, pool, subscriberID, "0xsubscriber", "subscriber")
	seedUser(t, pool, hostID, "0xhost", "host")
	seedEvent(t, pool, eventID, hostID)

	params := buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xsubscriber", `{}`)
	mustHandle(t, Subscribe(), params)

	var isDelete bool
	var entityType string
	var entityID int64
	err := pool.QueryRow(context.Background(), `
		SELECT is_delete, entity_type, entity_id
		FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true
	`, subscriberID, eventID).Scan(&isDelete, &entityType, &entityID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
	if entityType != EntityTypeEvent {
		t.Errorf("entity_type = %q, want %q", entityType, EntityTypeEvent)
	}
	if entityID != eventID {
		t.Errorf("entity_id = %d, want %d", entityID, eventID)
	}
}

func TestSubscribe_EventRejectsNonexistentTarget(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 12)
	eventID := int64(7002)

	seedUser(t, pool, subscriberID, "0xsubscriber2", "subscriber2")

	params := buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xsubscriber2", `{}`)
	mustReject(t, Subscribe(), params, "target Event")
}

func TestSubscribe_EventRejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 13)
	hostID := int64(UserIDOffset + 14)
	eventID := int64(7003)

	seedUser(t, pool, subscriberID, "0xsubscriber3", "subscriber3")
	seedUser(t, pool, hostID, "0xhost3", "host3")
	seedEvent(t, pool, eventID, hostID)

	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xsubscriber3", `{}`))
	mustReject(t, Subscribe(), buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xsubscriber3", `{}`), "already exists")
}

func TestSubscribe_UserAndEventWithSameIdCoexist(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 30)
	// The followed user's id and the event's id collide on purpose: the two id
	// spaces are independent, and subscriptions.user_id holds either one
	// depending on entity_type. Both subscriptions must be able to coexist.
	collidingID := int64(UserIDOffset + 31)
	hostID := int64(UserIDOffset + 32)

	seedUser(t, pool, subscriberID, "0xcoexsubscriber", "coexsubscriber")
	seedUser(t, pool, collidingID, "0xcoexpublisher", "coexpublisher")
	seedUser(t, pool, hostID, "0xcoexhost", "coexhost")
	seedEvent(t, pool, collidingID, hostID)

	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeUser, ActionSubscribe, subscriberID, collidingID, "0xcoexsubscriber", `{}`))
	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, collidingID, "0xcoexsubscriber", `{}`))

	assertSubscriptionDeleteState(t, pool, subscriberID, collidingID, EntityTypeUser, false)
	assertSubscriptionDeleteState(t, pool, subscriberID, collidingID, EntityTypeEvent, false)

	// Unsubscribing from the user must tombstone only the User row.
	mustHandle(t, Unsubscribe(), buildParams(t, pool, EntityTypeUser, ActionUnsubscribe, subscriberID, collidingID, "0xcoexsubscriber", `{}`))

	assertSubscriptionDeleteState(t, pool, subscriberID, collidingID, EntityTypeUser, true)
	assertSubscriptionDeleteState(t, pool, subscriberID, collidingID, EntityTypeEvent, false)
}

// assertSubscriptionDeleteState asserts that exactly one current subscriptions
// row exists for the identity and that its is_delete matches.
func assertSubscriptionDeleteState(t *testing.T, pool *pgxpool.Pool, subscriberID, userID int64, entityType string, wantDelete bool) {
	t.Helper()
	var isDelete bool
	err := pool.QueryRow(context.Background(), `
		SELECT is_delete FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND entity_type = $3 AND is_current = true
	`, subscriberID, userID, entityType).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query %s subscription (%d -> %d): %v", entityType, subscriberID, userID, err)
	}
	if isDelete != wantDelete {
		t.Errorf("%s subscription (%d -> %d): is_delete = %v, want %v", entityType, subscriberID, userID, isDelete, wantDelete)
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

func TestUnsubscribe_EventSuccess(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 20)
	hostID := int64(UserIDOffset + 21)
	eventID := int64(7010)

	seedUser(t, pool, subscriberID, "0xeventsubscriber", "eventsubscriber")
	seedUser(t, pool, hostID, "0xeventhost", "eventhost")
	seedEvent(t, pool, eventID, hostID)

	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xeventsubscriber", `{}`))
	mustHandle(t, Unsubscribe(), buildParams(t, pool, EntityTypeEvent, ActionUnsubscribe, subscriberID, eventID, "0xeventsubscriber", `{}`))

	var isDelete bool
	var entityType string
	var entityID int64
	err := pool.QueryRow(context.Background(), `
		SELECT is_delete, entity_type, entity_id
		FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true
	`, subscriberID, eventID).Scan(&isDelete, &entityType, &entityID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
	if entityType != EntityTypeEvent {
		t.Errorf("entity_type = %q, want %q", entityType, EntityTypeEvent)
	}
	if entityID != eventID {
		t.Errorf("entity_id = %d, want %d", entityID, eventID)
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

func seedEvent(t *testing.T, pool *pgxpool.Pool, eventID, hostID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO events (
			event_id, event_type, user_id, entity_type, entity_id,
			end_date, event_data, is_deleted,
			created_at, updated_at, txhash, blockhash, blocknumber
		) VALUES (
			$1, 'remix_contest', $2, 'track', 1,
			now() + interval '1 day', '{}', false,
			now(), now(), 'seed-event', 'test-block-100', 100
		)
	`, eventID, hostID)
	if err != nil {
		t.Fatalf("seedEvent(%d): %v", eventID, err)
	}
}
