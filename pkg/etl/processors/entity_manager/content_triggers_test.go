package entity_manager

import (
	"context"
	"strconv"
	"testing"
)

// Tests for the content triggers ported in migration 0019:
// handle_track and handle_playlist. These triggers fire on INSERT (and
// for tracks, also UPDATE) into the tracks/playlists tables. Go-side
// entity_manager handlers do the inserts; the triggers should keep
// aggregate_user track_count/playlist_count/album_count in sync and
// initialize aggregate_track / aggregate_playlist rows.

func TestTrigger_HandleTrack_InitializesAggregates(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 200)
	tid := int64(TrackIDOffset + 2000)
	seedUser(t, pool, uid, "0xtrigtrack", "trigtrack")

	meta := `{"owner_id":3000200,"title":"Trigger Test","genre":"Electronic"}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xtrigtrack", meta))

	// aggregate_track row created
	var saveCount, repostCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT save_count, repost_count FROM aggregate_track WHERE track_id = $1", tid).Scan(&saveCount, &repostCount); err != nil {
		t.Fatalf("aggregate_track: %v", err)
	}
	// aggregate_user.track_count = 1 (public track)
	var trackCount, totalTrackCount int64
	if err := pool.QueryRow(context.Background(),
		"SELECT track_count, total_track_count FROM aggregate_user WHERE user_id = $1", uid).Scan(&trackCount, &totalTrackCount); err != nil {
		t.Fatalf("aggregate_user: %v", err)
	}
	if trackCount != 1 {
		t.Errorf("aggregate_user.track_count = %d, want 1", trackCount)
	}
	if totalTrackCount != 1 {
		t.Errorf("aggregate_user.total_track_count = %d, want 1", totalTrackCount)
	}
}

func TestTrigger_HandleTrack_UnlistedDoesNotIncrementTrackCount(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 210)
	tid := int64(TrackIDOffset + 2100)
	seedUser(t, pool, uid, "0xunlisted", "unlistedu")

	meta := `{"owner_id":3000210,"title":"Hidden","genre":"Electronic","is_unlisted":true}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xunlisted", meta))

	var trackCount, totalTrackCount int64
	if err := pool.QueryRow(context.Background(),
		"SELECT track_count, total_track_count FROM aggregate_user WHERE user_id = $1", uid).Scan(&trackCount, &totalTrackCount); err != nil {
		t.Fatalf("aggregate_user: %v", err)
	}
	if trackCount != 0 {
		t.Errorf("track_count = %d, want 0 for unlisted track", trackCount)
	}
	if totalTrackCount != 1 {
		t.Errorf("total_track_count = %d, want 1 (counts unlisted)", totalTrackCount)
	}
}

func TestTrigger_HandleTrack_RemixCreatesNotification(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	parentOwner := int64(UserIDOffset + 220)
	remixer := int64(UserIDOffset + 221)
	parentID := int64(TrackIDOffset + 2200)
	remixID := int64(TrackIDOffset + 2201)
	seedUser(t, pool, parentOwner, "0xparent", "parentu")
	seedUser(t, pool, remixer, "0xremixer", "remixu")
	seedTrackFull(t, pool, parentID, parentOwner, "Original")

	meta := `{"owner_id":3000221,"title":"Remix","genre":"Electronic","remix_of":{"tracks":[{"parent_track_id":2002200}]}}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, remixer, remixID, "0xremixer", meta))

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM notification WHERE type = 'remix' AND specifier = $1",
		strconv.FormatInt(remixer, 10)).Scan(&count); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remix notification, got %d", count)
	}
}

func TestTrigger_HandlePlaylist_InitializesAggregates(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 230)
	pid := int64(PlaylistIDOffset + 3000)
	seedUser(t, pool, uid, "0xplowner", "plowner")

	meta := `{"playlist_name":"My Playlist","is_album":false,"is_private":false}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xplowner", meta))

	var aggIsAlbum *bool
	if err := pool.QueryRow(context.Background(),
		"SELECT is_album FROM aggregate_playlist WHERE playlist_id = $1", pid).Scan(&aggIsAlbum); err != nil {
		t.Fatalf("aggregate_playlist row: %v", err)
	}
	if aggIsAlbum == nil || *aggIsAlbum != false {
		t.Errorf("aggregate_playlist.is_album = %v, want false", aggIsAlbum)
	}

	var playlistCount, albumCount int64
	if err := pool.QueryRow(context.Background(),
		"SELECT playlist_count, album_count FROM aggregate_user WHERE user_id = $1", uid).Scan(&playlistCount, &albumCount); err != nil {
		t.Fatalf("aggregate_user: %v", err)
	}
	if playlistCount != 1 {
		t.Errorf("playlist_count = %d, want 1", playlistCount)
	}
	if albumCount != 0 {
		t.Errorf("album_count = %d, want 0", albumCount)
	}
}

func TestTrigger_HandlePlaylist_AlbumIncrementsAlbumCount(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 240)
	pid := int64(PlaylistIDOffset + 3100)
	seedUser(t, pool, uid, "0xalbumowner", "albumown")

	meta := `{"playlist_name":"My Album","is_album":true,"is_private":false}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xalbumowner", meta))

	var playlistCount, albumCount int64
	if err := pool.QueryRow(context.Background(),
		"SELECT playlist_count, album_count FROM aggregate_user WHERE user_id = $1", uid).Scan(&playlistCount, &albumCount); err != nil {
		t.Fatalf("aggregate_user: %v", err)
	}
	if playlistCount != 0 {
		t.Errorf("playlist_count = %d, want 0", playlistCount)
	}
	if albumCount != 1 {
		t.Errorf("album_count = %d, want 1", albumCount)
	}
}

func TestTrigger_HandlePlaylist_PrivateDoesNotIncrement(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 250)
	pid := int64(PlaylistIDOffset + 3200)
	seedUser(t, pool, uid, "0xprivate", "privu")

	meta := `{"playlist_name":"Hidden Playlist","is_album":false,"is_private":true}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xprivate", meta))

	var playlistCount int64
	if err := pool.QueryRow(context.Background(),
		"SELECT playlist_count FROM aggregate_user WHERE user_id = $1", uid).Scan(&playlistCount); err != nil {
		t.Fatalf("aggregate_user: %v", err)
	}
	if playlistCount != 0 {
		t.Errorf("playlist_count = %d, want 0 for private playlist", playlistCount)
	}
}
