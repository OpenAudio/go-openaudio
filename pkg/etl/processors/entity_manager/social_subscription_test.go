package entity_manager

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedRemixContestEvent creates a live remix_contest event (via EventCreate) and
// returns its event_id, so the Event-follow subscribe tests exercise a real
// target rather than hand-writing the subscriptions row.
func seedRemixContestEvent(t *testing.T, pool *pgxpool.Pool, eventID int64) int64 {
	t.Helper()
	hostID := eventID + 1
	parentTrackID := eventID + 2
	seedUser(t, pool, hostID, "0xhost", "host")
	seedTrack(t, pool, parentTrackID, hostID)
	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	eventMeta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(parentTrackID) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, hostID, eventID, "0xhost", eventMeta))
	return eventID
}

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

// TestSubscribe_EventSuccess is the regression test for "following a contest is
// not working": a Subscribe/Event tx must write an Event subscription row with
// the event_id mirrored into both the overloaded user_id column and entity_id,
// so the follow_state / followers endpoints can read it back.
func TestSubscribe_EventSuccess(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 1)
	seedUser(t, pool, subscriberID, "0xsubscriber", "subscriber")
	eventID := seedRemixContestEvent(t, pool, 9_000_001)

	mustHandle(t, Subscribe(),
		buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xSubscriber", `{}`))

	var userID, entityID int64
	var entityType string
	var isDelete bool
	err := pool.QueryRow(context.Background(), `
		SELECT user_id, entity_type, entity_id, is_delete
		FROM subscriptions
		WHERE subscriber_id = $1 AND entity_type = 'Event' AND is_current = true
	`, subscriberID).Scan(&userID, &entityType, &entityID, &isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if userID != eventID {
		t.Errorf("user_id = %d, want event_id %d (overloaded column)", userID, eventID)
	}
	if entityType != EntityTypeEvent {
		t.Errorf("entity_type = %q, want %q", entityType, EntityTypeEvent)
	}
	if entityID != eventID {
		t.Errorf("entity_id = %d, want %d", entityID, eventID)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestSubscribe_RejectsNonexistentEvent(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 1)
	seedUser(t, pool, subscriberID, "0xsubscriber", "subscriber")
	params := buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, 9_000_999, "0xSubscriber", `{}`)
	mustReject(t, Subscribe(), params, "subscription target event 9000999 does not exist")
}

func TestUnsubscribe_EventSuccess(t *testing.T) {
	pool := setupTestDB(t)
	subscriberID := int64(UserIDOffset + 1)
	seedUser(t, pool, subscriberID, "0xsubscriber", "subscriber")
	eventID := seedRemixContestEvent(t, pool, 9_000_001)

	mustHandle(t, Subscribe(),
		buildParams(t, pool, EntityTypeEvent, ActionSubscribe, subscriberID, eventID, "0xSubscriber", `{}`))
	mustHandle(t, Unsubscribe(),
		buildParams(t, pool, EntityTypeEvent, ActionUnsubscribe, subscriberID, eventID, "0xSubscriber", `{}`))

	// The tombstone row keeps entity_type='Event' so follow_state's
	// is_delete=false filter correctly excludes it.
	var entityType string
	var isDelete bool
	err := pool.QueryRow(context.Background(), `
		SELECT entity_type, is_delete
		FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true
	`, subscriberID, eventID).Scan(&entityType, &isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if entityType != EntityTypeEvent {
		t.Errorf("entity_type = %q, want %q", entityType, EntityTypeEvent)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}
