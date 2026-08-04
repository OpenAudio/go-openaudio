package entity_manager

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// TestSave_Album_RecordsAsPlaylist asserts that saving an album records
// save_type="playlist", both when the chain gives us only entity_type
// "Playlist" and when the client explicitly sends "type":"album" in metadata.
//
// save_type is part of the saves primary key and is written once, whereas
// is_album is mutable — deriving "album" from it made the same chain history
// index differently depending on when it was replayed. Consumers that need
// the distinction read playlists.is_album at query time.
func TestSave_Album_RecordsAsPlaylist(t *testing.T) {
	seedAlbum := func(t *testing.T, pool *pgxpool.Pool, pid, ownerID int64) {
		t.Helper()
		_, err := pool.Exec(context.Background(), `
			INSERT INTO playlists (playlist_id, playlist_owner_id, is_album, is_private, playlist_contents, is_current, is_delete, created_at, updated_at, txhash)
			VALUES ($1, $2, true, false, '{}', true, false, now(), now(), '')
		`, pid, ownerID)
		if err != nil {
			t.Fatalf("seed album: %v", err)
		}
	}

	// Metadata "type" is the highest-priority signal, chain entity_type the
	// next: an album must resolve to "playlist" through either path.
	for _, tc := range []struct {
		name string
		meta string
	}{
		{"metadata says album", `{"type":"album"}`},
		{"chain entity_type only", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := setupTestDB(t)
			uid := int64(UserIDOffset + 1)
			pid := int64(PlaylistIDOffset + 3)
			seedUser(t, pool, uid, "0xalbsaver", "albsaver")
			seedUser(t, pool, UserIDOffset+2, "0xalbowner", "albowner")
			seedAlbum(t, pool, pid, UserIDOffset+2)

			params := buildParams(t, pool, EntityTypePlaylist, ActionSave, uid, pid, "0xAlbSaver", tc.meta)
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
		})
	}
}

// TestRepost_Album_RecordsAsPlaylist is the repost-side mirror of
// TestSave_Album_RecordsAsPlaylist.
func TestRepost_Album_RecordsAsPlaylist(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 4)
	seedUser(t, pool, uid, "0xalbreposter", "albreposter")
	seedUser(t, pool, UserIDOffset+2, "0xalbowner2", "albowner2")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO playlists (playlist_id, playlist_owner_id, is_album, is_private, playlist_contents, is_current, is_delete, created_at, updated_at, txhash)
		VALUES ($1, $2, true, false, '{}', true, false, now(), now(), '')
	`, pid, UserIDOffset+2)
	if err != nil {
		t.Fatalf("seed album: %v", err)
	}

	params := buildParams(t, pool, EntityTypePlaylist, ActionRepost, uid, pid, "0xAlbReposter", `{"type":"album"}`)
	mustHandle(t, Repost(), params)

	var repostType string
	err = pool.QueryRow(context.Background(),
		"SELECT repost_type::text FROM reposts WHERE user_id = $1 AND repost_item_id = $2 AND is_current = true",
		uid, pid).Scan(&repostType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if repostType != "playlist" {
		t.Errorf("repost_type = %q, want playlist", repostType)
	}
}

// TestSave_Playlist_WhenTrackIdCollides asserts that a Playlist save resolves
// to save_type="playlist" even when a track happens to exist with the same
// numeric id (track_id and playlist_id are independent namespaces — collisions
// are real in prod data). Without the guard, `inferSaveType` would check
// tracks first and write `save_type='track'` for the playlist save.
func TestSave_Playlist_WhenTrackIdCollides(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 100)
	collisionID := int64(TrackIDOffset + 4281) // same id used as both track + playlist
	seedUser(t, pool, uid, "0xcollider", "collider")
	seedUser(t, pool, UserIDOffset+101, "0xowner1", "owner1")
	seedUser(t, pool, UserIDOffset+102, "0xowner2", "owner2")
	// Seed a TRACK with the same numeric id as the playlist we'll save
	seedTrack(t, pool, collisionID, UserIDOffset+101)
	// Seed the playlist (non-album) the chain tx is about
	seedPlaylist(t, pool, collisionID, UserIDOffset+102)

	// Chain tx mimics observed prod tx: entity_type=Playlist, no "type" in metadata
	meta := `{"is_save_of_repost":false}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionSave, uid, collisionID, "0xCollider", meta)
	mustHandle(t, Save(), params)

	var saveType string
	err := pool.QueryRow(context.Background(),
		"SELECT save_type::text FROM saves WHERE user_id = $1 AND save_item_id = $2 AND is_current = true",
		uid, collisionID).Scan(&saveType)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if saveType != "playlist" {
		t.Errorf("save_type = %q, want playlist (track with same id must not win)", saveType)
	}
}
