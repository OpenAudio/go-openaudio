package entity_manager

import (
	"context"
	"database/sql"
	"testing"
)

// Regression: playlists.last_added_to was never written (perpetually NULL),
// breaking "recently added to" sort and playlist-update notifications. It must
// be the block time of the most recent track add: set on create-with-tracks,
// bumped when an update adds a track, and preserved on rename/reorder/removal.
func TestPlaylist_LastAddedTo(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 1700)
	seedUser(t, pool, uid, "0xlastadded", "lastaddedu")

	lastAddedTo := func(pid int64) sql.NullTime {
		var v sql.NullTime
		if err := pool.QueryRow(context.Background(),
			"SELECT last_added_to FROM playlists WHERE playlist_id=$1 AND is_current=true", pid).Scan(&v); err != nil {
			t.Fatalf("query last_added_to(%d): %v", pid, err)
		}
		return v
	}

	// 1. Create an empty playlist → last_added_to NULL.
	pEmpty := int64(PlaylistIDOffset + 17000)
	mustHandle(t, PlaylistCreate(), buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pEmpty, "0xlastadded",
		`{"playlist_name":"Empty","playlist_contents":[]}`))
	if lastAddedTo(pEmpty).Valid {
		t.Error("empty playlist create: last_added_to should be NULL")
	}

	// 2. Metadata-only rename → still NULL (must not set it).
	mustHandle(t, PlaylistUpdate(), buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pEmpty, "0xlastadded",
		`{"playlist_name":"Renamed"}`))
	if lastAddedTo(pEmpty).Valid {
		t.Error("metadata-only rename: last_added_to should stay NULL")
	}

	// 3. Add a track → last_added_to set.
	mustHandle(t, PlaylistUpdate(), buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pEmpty, "0xlastadded",
		`{"playlist_contents":{"track_ids":[{"track":2017001,"time":1700000000}]}}`))
	added := lastAddedTo(pEmpty)
	if !added.Valid {
		t.Fatal("adding a track: last_added_to should be set")
	}

	// 4. Rename again (no playlist_contents) → preserved, unchanged.
	mustHandle(t, PlaylistUpdate(), buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pEmpty, "0xlastadded",
		`{"playlist_name":"Renamed2"}`))
	if got := lastAddedTo(pEmpty); !got.Valid || !got.Time.Equal(added.Time) {
		t.Errorf("rename after add: last_added_to changed; want %v got %v", added.Time, got.Time)
	}

	// 5. Remove the track (empty contents) → preserved (removal must not clear).
	mustHandle(t, PlaylistUpdate(), buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pEmpty, "0xlastadded",
		`{"playlist_contents":[]}`))
	if got := lastAddedTo(pEmpty); !got.Valid || !got.Time.Equal(added.Time) {
		t.Errorf("removal: last_added_to should be preserved; want %v got %v", added.Time, got.Time)
	}

	// 6. Create WITH tracks → last_added_to set immediately.
	pWith := int64(PlaylistIDOffset + 17001)
	mustHandle(t, PlaylistCreate(), buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pWith, "0xlastadded",
		`{"playlist_name":"WithTracks","playlist_contents":{"track_ids":[{"track":2017002,"time":1700000000}]}}`))
	if !lastAddedTo(pWith).Valid {
		t.Error("create with tracks: last_added_to should be set")
	}
}
