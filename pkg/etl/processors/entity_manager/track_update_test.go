package entity_manager

import (
	"context"
	"testing"
)

func TestTrackUpdate_TxType(t *testing.T) {
	h := TrackUpdate()
	if h.EntityType() != EntityTypeTrack || h.Action() != ActionUpdate {
		t.Fatalf("unexpected handler type")
	}
}

func TestTrackUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := UserIDOffset + 1
	tid := TrackIDOffset + 60
	seedUser(t, pool, uid, "0xowner", "ou")
	seedTrackFull(t, pool, tid, uid, "Old Title")
	meta := `{"title":"New Title","genre":"Jazz"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", meta)
	mustHandle(t, TrackUpdate(), params)

	var title string
	_ = pool.QueryRow(context.Background(), "SELECT title FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&title)
	if title != "New Title" {
		t.Errorf("title = %q, want New Title", title)
	}
}

func TestTrackUpdate_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	uid := UserIDOffset + 1
	tid := TrackIDOffset + 99
	seedUser(t, pool, uid, "0xo", "o")
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", `{"title":"X"}`)
	mustReject(t, TrackUpdate(), params, "does not exist")
}

func TestTrackUpdate_OwnerMismatch(t *testing.T) {
	pool := setupTestDB(t)
	uid := UserIDOffset + 1
	other := UserIDOffset + 2
	tid := TrackIDOffset + 61
	seedUser(t, pool, uid, "0xa", "a")
	seedUser(t, pool, other, "0xb", "b")
	seedTrackFull(t, pool, tid, other, "T")
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xa", `{"title":"Hack"}`)
	mustReject(t, TrackUpdate(), params, "does not match")
}
