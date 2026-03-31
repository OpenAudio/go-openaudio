package entity_manager

import (
	"context"
	"testing"
)

func TestTrackDelete_TxType(t *testing.T) {
	h := TrackDelete()
	if h.EntityType() != EntityTypeTrack || h.Action() != ActionDelete {
		t.Fatalf("unexpected handler type")
	}
}

func TestTrackDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 70)
	seedUser(t, pool, uid, "0xo", "o")
	seedTrackFull(t, pool, tid, uid, "Del Me")
	params := buildParams(t, pool, EntityTypeTrack, ActionDelete, uid, tid, "0xo", `{}`)
	mustHandle(t, TrackDelete(), params)

	var isDelete bool
	err := pool.QueryRow(context.Background(), "SELECT is_delete FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete true")
	}
}

func TestTrackDelete_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xo", "o")
	params := buildParams(t, pool, EntityTypeTrack, ActionDelete, uid, TrackIDOffset+88, "0xo", `{}`)
	mustReject(t, TrackDelete(), params, "does not exist")
}
