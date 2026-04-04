package entity_manager

import (
	"context"
	"testing"
)

func TestSave_Playlist_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 1)
	seedUser(t, pool, uid, "0xplsaver", "plsaver")
	seedUser(t, pool, UserIDOffset+2, "0xplowner", "plowner")
	seedPlaylist(t, pool, pid, UserIDOffset+2)

	meta := `{"type":"playlist"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionSave, uid, pid, "0xPlSaver", meta)
	mustHandle(t, Save(), params)

	var saveType string
	err := pool.QueryRow(context.Background(),
		"SELECT save_type::text FROM saves WHERE user_id = $1 AND save_item_id = $2 AND is_current = true",
		uid, pid).Scan(&saveType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if saveType != "playlist" {
		t.Errorf("save_type = %q, want playlist", saveType)
	}
}

func TestRepost_Playlist_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 2)
	seedUser(t, pool, uid, "0xplreposter", "plreposter")
	seedUser(t, pool, UserIDOffset+2, "0xplowner2", "plowner2")
	seedPlaylist(t, pool, pid, UserIDOffset+2)

	meta := `{"type":"playlist"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionRepost, uid, pid, "0xPlReposter", meta)
	mustHandle(t, Repost(), params)

	var repostType string
	err := pool.QueryRow(context.Background(),
		"SELECT repost_type::text FROM reposts WHERE user_id = $1 AND repost_item_id = $2 AND is_current = true",
		uid, pid).Scan(&repostType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if repostType != "playlist" {
		t.Errorf("repost_type = %q, want playlist", repostType)
	}
}

func TestSave_Album_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 3)
	seedUser(t, pool, uid, "0xalbsaver", "albsaver")
	seedUser(t, pool, UserIDOffset+2, "0xalbowner", "albowner")
	// Seed an album
	_, err := pool.Exec(context.Background(), `
		INSERT INTO playlists (playlist_id, playlist_owner_id, is_album, is_private, playlist_contents, is_current, is_delete, created_at, updated_at, txhash)
		VALUES ($1, $2, true, false, '{}', true, false, now(), now(), '')
	`, pid, UserIDOffset+2)
	if err != nil {
		t.Fatalf("seed album: %v", err)
	}

	meta := `{"type":"album"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionSave, uid, pid, "0xAlbSaver", meta)
	mustHandle(t, Save(), params)

	var saveType string
	err = pool.QueryRow(context.Background(),
		"SELECT save_type::text FROM saves WHERE user_id = $1 AND save_item_id = $2 AND is_current = true",
		uid, pid).Scan(&saveType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if saveType != "album" {
		t.Errorf("save_type = %q, want album", saveType)
	}
}
