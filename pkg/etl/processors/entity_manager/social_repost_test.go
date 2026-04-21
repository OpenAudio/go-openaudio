package entity_manager

import (
	"context"
	"testing"
)

func TestRepost_TxType(t *testing.T) {
	h := Repost()
	if h.EntityType() != EntityTypeAny {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeAny)
	}
	if h.Action() != ActionRepost {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionRepost)
	}
}

func TestRepost_Track_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xreposter", "reposter")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionRepost, uid, tid, "0xReposter", meta)
	mustHandle(t, Repost(), params)

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM reposts WHERE user_id = $1 AND repost_item_id = $2 AND is_current = true",
		uid, tid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestRepost_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 2)
	seedUser(t, pool, uid, "0xreposter", "reposter")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	mustHandle(t, Repost(), buildParams(t, pool, EntityTypeTrack, ActionRepost, uid, tid, "0xReposter", meta))
	mustReject(t, Repost(), buildParams(t, pool, EntityTypeTrack, ActionRepost, uid, tid, "0xReposter", meta), "already exists")
}

func TestUnrepost_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 3)
	seedUser(t, pool, uid, "0xreposter", "reposter")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	mustHandle(t, Repost(), buildParams(t, pool, EntityTypeTrack, ActionRepost, uid, tid, "0xReposter", meta))
	mustHandle(t, Unrepost(), buildParams(t, pool, EntityTypeTrack, ActionUnrepost, uid, tid, "0xReposter", meta))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM reposts WHERE user_id = $1 AND repost_item_id = $2 AND is_current = true",
		uid, tid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestUnrepost_RejectsNoActiveRepost(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 4)
	seedUser(t, pool, uid, "0xreposter", "reposter")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	meta := `{"type":"track"}`
	mustReject(t, Unrepost(), buildParams(t, pool, EntityTypeTrack, ActionUnrepost, uid, tid, "0xReposter", meta), "no active repost")
}
