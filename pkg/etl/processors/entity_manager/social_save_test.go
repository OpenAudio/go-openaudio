package entity_manager

import (
	"context"
	"testing"
)

func TestSave_TxType(t *testing.T) {
	h := Save()
	if h.EntityType() != EntityTypeAny {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeAny)
	}
	if h.Action() != ActionSave {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionSave)
	}
}

func TestSave_Track_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xsaver", "saver")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionSave, uid, tid, "0xSaver", meta)
	mustHandle(t, Save(), params)

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM saves WHERE user_id = $1 AND save_item_id = $2 AND is_current = true",
		uid, tid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestSave_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 2)
	seedUser(t, pool, uid, "0xsaver", "saver")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	mustHandle(t, Save(), buildParams(t, pool, EntityTypeTrack, ActionSave, uid, tid, "0xSaver", meta))
	mustReject(t, Save(), buildParams(t, pool, EntityTypeTrack, ActionSave, uid, tid, "0xSaver", meta), "already exists")
}

func TestUnsave_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 3)
	seedUser(t, pool, uid, "0xsaver", "saver")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	mustHandle(t, Save(), buildParams(t, pool, EntityTypeTrack, ActionSave, uid, tid, "0xSaver", meta))
	mustHandle(t, Unsave(), buildParams(t, pool, EntityTypeTrack, ActionUnsave, uid, tid, "0xSaver", meta))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM saves WHERE user_id = $1 AND save_item_id = $2 AND is_current = true",
		uid, tid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestUnsave_RejectsNoActiveSave(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 4)
	seedUser(t, pool, uid, "0xsaver", "saver")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	mustReject(t, Unsave(), buildParams(t, pool, EntityTypeTrack, ActionUnsave, uid, tid, "0xSaver", meta), "no active save")
}
