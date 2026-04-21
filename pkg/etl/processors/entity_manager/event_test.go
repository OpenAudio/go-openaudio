package entity_manager

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestEventCreate_TxType(t *testing.T) {
	h := EventCreate()
	if h.EntityType() != EntityTypeEvent {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeEvent)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestEventCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + endDate + `","event_data":{}}`
	params := buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 1, "0xEventowner", meta)
	mustHandle(t, EventCreate(), params)

	var eventType string
	err := pool.QueryRow(context.Background(),
		"SELECT event_type FROM events WHERE event_id = $1",
		1).Scan(&eventType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if eventType != "remix_contest" {
		t.Errorf("event_type = %q, want remix_contest", eventType)
	}
}

func TestEventCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 1, "0xEventowner", meta))
	mustReject(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 1, "0xEventowner", meta), "already exists")
}

func TestEventCreate_RejectsMissingEventType(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"end_date":"` + endDate + `"}`
	mustReject(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 100, "0xEventowner", meta), "missing required field: event_type")
}

func TestEventCreate_RejectsMissingEndDate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")

	meta := `{"event_type":"remix_contest"}`
	mustReject(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 101, "0xEventowner", meta), "missing required field: end_date")
}

func TestEventCreate_RejectsPastEndDate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	pastDate := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + pastDate + `"}`
	mustReject(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 102, "0xEventowner", meta), "end_date cannot be in the past")
}

func TestEventCreate_RejectsRemixOfRemix(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	parentTrackID := int64(TrackIDOffset + 1)
	remixTrackID := int64(TrackIDOffset + 2)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, parentTrackID, uid)

	// Seed a track that is a remix of parentTrackID
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tracks (track_id, owner_id, is_current, is_delete, track_segments, remix_of, created_at, updated_at, txhash)
		VALUES ($1, $2, true, false, '[]', $3, now(), now(), '')
	`, remixTrackID, uid, fmt.Sprintf(`{"tracks":[{"parent_track_id":%d}]}`, parentTrackID))
	if err != nil {
		t.Fatalf("seed remix track: %v", err)
	}

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(remixTrackID) + `,"end_date":"` + endDate + `"}`
	mustReject(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 103, "0xEventowner", meta), "remix and cannot host a remix contest")
}

func TestEventUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 2, "0xEventowner", meta))

	newEndDate := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	updateMeta := `{"end_date":"` + newEndDate + `"}`
	mustHandle(t, EventUpdate(), buildParams(t, pool, EntityTypeEvent, ActionUpdate, uid, 2, "0xEventowner", updateMeta))
}

func TestEventUpdate_RejectsNonOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedUser(t, pool, uid2, "0xother", "other")
	seedTrack(t, pool, tid, uid)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 3, "0xEventowner", meta))

	mustReject(t, EventUpdate(), buildParams(t, pool, EntityTypeEvent, ActionUpdate, uid2, 3, "0xOther", `{}`), "only event owner")
}

func TestEventUpdate_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")

	mustReject(t, EventUpdate(), buildParams(t, pool, EntityTypeEvent, ActionUpdate, uid, 999, "0xEventowner", `{}`), "does not exist")
}

func TestEventDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedTrack(t, pool, tid, uid)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 4, "0xEventowner", meta))
	mustHandle(t, EventDelete(), buildParams(t, pool, EntityTypeEvent, ActionDelete, uid, 4, "0xEventowner", `{}`))

	var isDeleted bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_deleted FROM events WHERE event_id = $1", 4).Scan(&isDeleted)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDeleted {
		t.Error("expected is_deleted = true")
	}
}

func TestEventDelete_RejectsNonOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xeventowner", "eventowner")
	seedUser(t, pool, uid2, "0xother", "other")
	seedTrack(t, pool, tid, uid)

	endDate := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	meta := `{"event_type":"remix_contest","entity_type":"track","entity_id":` + itoa(tid) + `,"end_date":"` + endDate + `","event_data":{}}`
	mustHandle(t, EventCreate(), buildParams(t, pool, EntityTypeEvent, ActionCreate, uid, 5, "0xEventowner", meta))

	mustReject(t, EventDelete(), buildParams(t, pool, EntityTypeEvent, ActionDelete, uid2, 5, "0xOther", `{}`), "only event owner")
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
