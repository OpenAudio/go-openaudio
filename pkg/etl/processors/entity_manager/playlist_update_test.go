package entity_manager

import (
	"context"
	"testing"
)

func TestPlaylistUpdate_TxType(t *testing.T) {
	h := PlaylistUpdate()
	if h.EntityType() != EntityTypePlaylist {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypePlaylist)
	}
	if h.Action() != ActionUpdate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionUpdate)
	}
}

func TestPlaylistUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 60)
	seedUser(t, pool, uid, "0xplowner", "plowner")

	// First create the playlist
	createMeta := `{"playlist_name":"Original Name"}`
	mustHandle(t, PlaylistCreate(), buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xPlOwner", createMeta))

	// Then update it
	updateMeta := `{"playlist_name":"Updated Name","description":"new desc"}`
	mustHandle(t, PlaylistUpdate(), buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xPlOwner", updateMeta))

	var name, desc string
	err := pool.QueryRow(context.Background(),
		"SELECT playlist_name, COALESCE(description, '') FROM playlists WHERE playlist_id = $1 AND is_current = true", pid).Scan(&name, &desc)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "Updated Name" {
		t.Errorf("playlist_name = %q, want %q", name, "Updated Name")
	}
	if desc != "new desc" {
		t.Errorf("description = %q, want %q", desc, "new desc")
	}
}

func TestPlaylistUpdate_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xplowner", "plowner")
	meta := `{"playlist_name":"X"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, PlaylistIDOffset+999, "0xPlOwner", meta)
	mustReject(t, PlaylistUpdate(), params, "does not exist")
}

func TestPlaylistUpdate_RejectsWrongOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	pid := int64(PlaylistIDOffset + 61)
	seedUser(t, pool, uid, "0xplowner", "plowner")
	seedUser(t, pool, uid2, "0xother", "other")

	createMeta := `{"playlist_name":"Mine"}`
	mustHandle(t, PlaylistCreate(), buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xPlOwner", createMeta))

	updateMeta := `{"playlist_name":"Stolen"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid2, pid, "0xOther", updateMeta)
	mustReject(t, PlaylistUpdate(), params, "does not match user")
}
