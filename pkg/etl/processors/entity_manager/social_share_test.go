package entity_manager

import (
	"context"
	"testing"
)

func TestShare_TxType(t *testing.T) {
	h := Share()
	if h.EntityType() != EntityTypeAny {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeAny)
	}
	if h.Action() != ActionShare {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionShare)
	}
}

func TestShare_Track_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xsharer", "sharer")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	params := buildParams(t, pool, EntityTypeTrack, ActionShare, uid, tid, "0xSharer", `{}`)
	mustHandle(t, Share(), params)

	var count int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM shares WHERE user_id = $1 AND share_item_id = $2",
		uid, tid).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 share row, got %d", count)
	}
}

func TestShare_AllowsDuplicates(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 2)
	seedUser(t, pool, uid, "0xsharer", "sharer")
	seedUser(t, pool, UserIDOffset+2, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, UserIDOffset+2)

	// Shares allow duplicates (unlike saves/reposts)
	mustHandle(t, Share(), buildParams(t, pool, EntityTypeTrack, ActionShare, uid, tid, "0xSharer", `{}`))
	// Second share with different txhash should succeed
	params2 := buildParams(t, pool, EntityTypeTrack, ActionShare, uid, tid, "0xSharer", `{}`)
	params2.TxHash = "txhash-share-dup"
	mustHandle(t, Share(), params2)
}

func TestShare_RejectsNonexistentTarget(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xsharer", "sharer")
	params := buildParams(t, pool, EntityTypeTrack, ActionShare, uid, TrackIDOffset+999, "0xSharer", `{}`)
	mustReject(t, Share(), params, "does not exist")
}
