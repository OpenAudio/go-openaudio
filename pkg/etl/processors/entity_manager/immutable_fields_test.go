package entity_manager

import (
	"context"
	"testing"
)

func TestStripImmutableFields_RemovesKeys(t *testing.T) {
	meta := map[string]any{
		"title":      "ok",
		"created_at": "should-be-stripped",
		"is_current": false,
		"track_id":   12345,
	}
	stripImmutableFields(meta, immutableTrackFields)
	if _, ok := meta["created_at"]; ok {
		t.Error("created_at should be stripped")
	}
	if _, ok := meta["is_current"]; ok {
		t.Error("is_current should be stripped")
	}
	if _, ok := meta["track_id"]; ok {
		t.Error("track_id should be stripped (track-specific immutable)")
	}
	if _, ok := meta["title"]; !ok {
		t.Error("title should remain")
	}
}

func TestTrackUpdate_IgnoresImmutableMetadataKeys(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 700)
	other := int64(UserIDOffset + 701)
	tid := int64(TrackIDOffset + 9500)
	seedUser(t, pool, uid, "0xowner", "ownr")
	seedUser(t, pool, other, "0xother", "ot")
	seedTrackFull(t, pool, tid, uid, "Original")

	// Try to mutate owner_id (immutable) along with title (mutable).
	meta := `{"title":"New Title","owner_id":3000701}`
	mustHandle(t, TrackUpdate(),
		buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xowner", meta))

	var ownerID int64
	var title string
	if err := pool.QueryRow(context.Background(),
		"SELECT owner_id, title FROM tracks WHERE track_id = $1 AND is_current = true",
		tid).Scan(&ownerID, &title); err != nil {
		t.Fatalf("query: %v", err)
	}
	if ownerID != uid {
		t.Errorf("owner_id = %d, want %d (immutable)", ownerID, uid)
	}
	if title != "New Title" {
		t.Errorf("title = %q, want New Title (mutable)", title)
	}
}

func TestPlaylistUpdate_IgnoresImmutableIsAlbum(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 710)
	pid := int64(PlaylistIDOffset + 9600)
	seedUser(t, pool, uid, "0xploner", "plr")

	// Create as non-album.
	createMeta := `{"playlist_name":"Not An Album","is_album":false,"is_private":false}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xploner", createMeta))

	// Try to flip is_album in update — should be ignored.
	updateMeta := `{"is_album":true}`
	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xploner", updateMeta))

	var isAlbum bool
	if err := pool.QueryRow(context.Background(),
		"SELECT is_album FROM playlists WHERE playlist_id = $1 AND is_current = true",
		pid).Scan(&isAlbum); err != nil {
		t.Fatalf("query: %v", err)
	}
	if isAlbum {
		t.Error("is_album was changed by update — immutable field not enforced")
	}
}
