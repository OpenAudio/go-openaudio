package entity_manager

import (
	"context"
	"testing"
)

func TestPlaylistDelete_TxType(t *testing.T) {
	h := PlaylistDelete()
	if h.EntityType() != EntityTypePlaylist {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypePlaylist)
	}
	if h.Action() != ActionDelete {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionDelete)
	}
}

func TestPlaylistDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 70)
	seedUser(t, pool, uid, "0xplowner", "plowner")

	createMeta := `{"playlist_name":"To Delete"}`
	mustHandle(t, PlaylistCreate(), buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xPlOwner", createMeta))

	mustHandle(t, PlaylistDelete(), buildParams(t, pool, EntityTypePlaylist, ActionDelete, uid, pid, "0xPlOwner", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM playlists WHERE playlist_id = $1 AND is_current = true", pid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestPlaylistDelete_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xplowner", "plowner")
	params := buildParams(t, pool, EntityTypePlaylist, ActionDelete, uid, PlaylistIDOffset+998, "0xPlOwner", `{}`)
	mustReject(t, PlaylistDelete(), params, "does not exist")
}

func TestPlaylistDelete_RejectsWrongOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	pid := int64(PlaylistIDOffset + 71)
	seedUser(t, pool, uid, "0xplowner", "plowner")
	seedUser(t, pool, uid2, "0xother", "other")

	createMeta := `{"playlist_name":"Mine"}`
	mustHandle(t, PlaylistCreate(), buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xPlOwner", createMeta))

	params := buildParams(t, pool, EntityTypePlaylist, ActionDelete, uid2, pid, "0xOther", `{}`)
	mustReject(t, PlaylistDelete(), params, "does not match user")
}
