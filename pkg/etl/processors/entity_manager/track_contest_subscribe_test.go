package entity_manager

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAutoSubscribeOnRemixContestSubmission(t *testing.T) {
	pool := setupTestDB(t)
	hostID := int64(UserIDOffset + 100)
	parentTrackID := int64(TrackIDOffset + 100)
	uploaderID := int64(UserIDOffset + 101)
	remixID := int64(TrackIDOffset + 200)

	seedUser(t, pool, hostID, "0xhost", "host")
	seedTrack(t, pool, parentTrackID, hostID)
	seedUser(t, pool, uploaderID, "0xuploader", "uploader")

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	eventMeta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(parentTrackID) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, hostID, 500, "0xhost", eventMeta))

	remixMeta := `{"owner_id":` + itoa(uploaderID) + `,"title":"My Remix","remix_of":{"tracks":[{"parent_track_id":` + itoa(parentTrackID) + `}]}}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uploaderID, remixID, "0xuploader", remixMeta))

	var subscriberID, eventUserID int64
	var entityType, txhash string
	err := pool.QueryRow(context.Background(), `
		SELECT subscriber_id, user_id, entity_type, txhash
		FROM subscriptions
		WHERE subscriber_id = $1 AND entity_type = 'Event' AND is_current = true AND is_delete = false
		LIMIT 1
	`, uploaderID).Scan(&subscriberID, &eventUserID, &entityType, &txhash)
	if err != nil {
		t.Fatalf("expected an auto-subscribe row for uploader %d; query: %v", uploaderID, err)
	}
	if entityType != "Event" {
		t.Errorf("entity_type = %q, want Event", entityType)
	}
	if !strings.HasPrefix(txhash, "auto_e") {
		t.Errorf("expected synthetic txhash prefixed `auto_e`, got %q", txhash)
	}
}

func TestAutoSubscribe_SkipsWhenUploaderIsHost(t *testing.T) {
	pool := setupTestDB(t)
	hostID := int64(UserIDOffset + 110)
	parentTrackID := int64(TrackIDOffset + 110)
	remixID := int64(TrackIDOffset + 211)

	seedUser(t, pool, hostID, "0xhost2", "host2")
	seedTrack(t, pool, parentTrackID, hostID)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	eventMeta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(parentTrackID) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, hostID, 501, "0xhost2", eventMeta))

	remixMeta := `{"owner_id":` + itoa(hostID) + `,"title":"Host Remix","remix_of":{"tracks":[{"parent_track_id":` + itoa(parentTrackID) + `}]}}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, hostID, remixID, "0xhost2", remixMeta))

	var cnt int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM subscriptions
		WHERE subscriber_id = $1 AND entity_type = 'Event'
	`, hostID).Scan(&cnt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cnt != 0 {
		t.Errorf("host should not auto-subscribe to their own contest, found %d Event subscriptions", cnt)
	}
}

func TestAutoSubscribe_SkipsWhenNoActiveContest(t *testing.T) {
	pool := setupTestDB(t)
	ownerID := int64(UserIDOffset + 120)
	parentTrackID := int64(TrackIDOffset + 120)
	uploaderID := int64(UserIDOffset + 121)
	remixID := int64(TrackIDOffset + 220)

	seedUser(t, pool, ownerID, "0xowner3", "owner3")
	seedTrack(t, pool, parentTrackID, ownerID)
	seedUser(t, pool, uploaderID, "0xuploader3", "uploader3")

	remixMeta := `{"owner_id":` + itoa(uploaderID) + `,"title":"Casual Remix","remix_of":{"tracks":[{"parent_track_id":` + itoa(parentTrackID) + `}]}}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uploaderID, remixID, "0xuploader3", remixMeta))

	var cnt int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM subscriptions
		WHERE subscriber_id = $1 AND entity_type = 'Event'
	`, uploaderID).Scan(&cnt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cnt != 0 {
		t.Errorf("no contest exists, expected 0 Event subscriptions, found %d", cnt)
	}
}

// Idempotency: a second remix submission from the same uploader must not
// produce a second is_current row for the same (subscriber, event) pair.
func TestAutoSubscribe_SkipsWhenAlreadySubscribed(t *testing.T) {
	pool := setupTestDB(t)
	hostID := int64(UserIDOffset + 130)
	parentTrackID := int64(TrackIDOffset + 130)
	uploaderID := int64(UserIDOffset + 131)
	remix1ID := int64(TrackIDOffset + 230)
	remix2ID := int64(TrackIDOffset + 231)

	seedUser(t, pool, hostID, "0xhost4", "host4")
	seedTrack(t, pool, parentTrackID, hostID)
	seedUser(t, pool, uploaderID, "0xuploader4", "uploader4")

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	eventMeta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(parentTrackID) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(),
		buildParams(t, pool, EntityTypeEvent, ActionCreate, hostID, 502, "0xhost4", eventMeta))

	remix1Meta := `{"owner_id":` + itoa(uploaderID) + `,"title":"Remix 1","remix_of":{"tracks":[{"parent_track_id":` + itoa(parentTrackID) + `}]}}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uploaderID, remix1ID, "0xuploader4", remix1Meta))

	remix2Meta := `{"owner_id":` + itoa(uploaderID) + `,"title":"Remix 2","remix_of":{"tracks":[{"parent_track_id":` + itoa(parentTrackID) + `}]}}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uploaderID, remix2ID, "0xuploader4", remix2Meta))

	var cnt int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM subscriptions
		WHERE subscriber_id = $1 AND entity_type = 'Event' AND is_current = true AND is_delete = false
	`, uploaderID).Scan(&cnt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cnt != 1 {
		t.Errorf("expected exactly 1 is_current Event subscription after two remixes, got %d", cnt)
	}
}
