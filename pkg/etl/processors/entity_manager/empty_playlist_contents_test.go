package entity_manager

import (
	"context"
	"encoding/json"
	"testing"
)

// Regression: removing the last track from a playlist via an Update should
// empty the playlist on reload (both the JSONB column and the playlist_tracks
// junction). The handler's key-exists check (`_, ok :=`) correctly
// distinguishes an empty list from a missing key; these tests lock that in.

func TestPlaylistUpdate_EmptyArrayMarksAllTracksRemoved(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 1300)
	pid := int64(PlaylistIDOffset + 12000)
	t1 := int64(TrackIDOffset + 13000)
	seedUser(t, pool, uid, "0xemptyowner", "emptyu")
	seedTrackFull(t, pool, t1, uid, "Lone Track")

	createMeta := `{"playlist_name":"Single","is_album":false,"is_private":false,"playlist_contents":{"track_ids":[{"track":2013000,"time":1700000000}]}}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xemptyowner", createMeta))

	// Sanity: one row, not removed.
	var beforeCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM playlist_tracks WHERE playlist_id = $1 AND is_removed = false",
		pid).Scan(&beforeCount); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if beforeCount != 1 {
		t.Fatalf("expected 1 active row before update, got %d", beforeCount)
	}

	// SDK-style empty list at the new array form.
	updateMeta := `{"playlist_contents":[]}`
	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xemptyowner", updateMeta))

	var activeAfter int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM playlist_tracks WHERE playlist_id = $1 AND is_removed = false",
		pid).Scan(&activeAfter); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if activeAfter != 0 {
		t.Errorf("after empty-array update, expected 0 active playlist_tracks rows, got %d", activeAfter)
	}

	// Legacy dict form `{track_ids: []}` should behave the same.
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid+1, "0xemptyowner",
			`{"playlist_name":"Single2","is_album":false,"is_private":false,"playlist_contents":{"track_ids":[{"track":2013000,"time":1700000000}]}}`))

	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid+1, "0xemptyowner",
			`{"playlist_contents":{"track_ids":[]}}`))

	var legacyActive int
	_ = pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM playlist_tracks WHERE playlist_id = $1 AND is_removed = false",
		pid+1).Scan(&legacyActive)
	if legacyActive != 0 {
		t.Errorf("legacy {track_ids:[]} form: expected 0 active rows, got %d", legacyActive)
	}
}

func TestPlaylistUpdate_EmptyArrayWritesJSONBColumn(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 1310)
	pid := int64(PlaylistIDOffset + 12100)
	t1 := int64(TrackIDOffset + 13100)
	seedUser(t, pool, uid, "0xjsonbu", "jsonbu")
	seedTrackFull(t, pool, t1, uid, "Track")

	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xjsonbu",
			`{"playlist_name":"P","is_album":false,"is_private":false,"playlist_contents":{"track_ids":[{"track":2013100,"time":1}]}}`))

	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xjsonbu",
			`{"playlist_contents":[]}`))

	var contentsRaw []byte
	if err := pool.QueryRow(context.Background(),
		"SELECT playlist_contents::text FROM playlists WHERE playlist_id = $1 AND is_current = true",
		pid).Scan(&contentsRaw); err != nil {
		t.Fatalf("query: %v", err)
	}

	// The persisted JSONB shape is always `{"track_ids":[...]}` regardless of
	// the SDK's input shape (bare array or legacy dict). Lock that in here.
	contentsStr := string(contentsRaw)
	if !isEffectivelyEmpty(contentsStr) {
		t.Errorf("playlist_contents = %q, want empty representation", contentsStr)
	}
	var asMap map[string]any
	if err := json.Unmarshal(contentsRaw, &asMap); err != nil {
		t.Fatalf("playlist_contents not a JSON object: %v (raw=%q)", err, contentsStr)
	}
	if _, ok := asMap["track_ids"]; !ok {
		t.Errorf("expected normalized {track_ids:[]} form, got %q", contentsStr)
	}
}

func TestNormalizePlaylistContentsJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing key", raw: `{}`, want: `{"track_ids":[]}`},
		{name: "explicit null", raw: `{"playlist_contents":null}`, want: `{"track_ids":[]}`},
		{name: "bare empty array (new SDK shape)", raw: `{"playlist_contents":[]}`, want: `{"track_ids":[]}`},
		{name: "legacy empty dict", raw: `{"playlist_contents":{"track_ids":[]}}`, want: `{"track_ids":[]}`},
		{name: "bare array with entries", raw: `{"playlist_contents":[{"track":1,"time":2}]}`, want: `{"track_ids":[{"time":2,"track":1}]}`},
		{name: "legacy dict with entries", raw: `{"playlist_contents":{"track_ids":[{"track":3,"time":4}]}}`, want: `{"track_ids":[{"time":4,"track":3}]}`},
		// SDK alias shape: track_id / timestamp / metadata_timestamp are
		// canonicalized onto track / time / metadata_time. This is the shape
		// that broke Dabow's playlist — the api reader only understands `track`.
		{name: "track_id alias", raw: `{"playlist_contents":[{"track_id":5,"timestamp":6}]}`, want: `{"track_ids":[{"time":6,"track":5}]}`},
		{name: "track_id alias with metadata_timestamp", raw: `{"playlist_contents":[{"track_id":7,"timestamp":8,"metadata_timestamp":9}]}`, want: `{"track_ids":[{"metadata_time":9,"time":8,"track":7}]}`},
		{name: "hashid string track", raw: `{"playlist_contents":[{"track":"LjjBL","time":10}]}`, want: `{"track_ids":[{"time":10,"track":777}]}`},
		{name: "mixed alias and canonical", raw: `{"playlist_contents":[{"track":1,"time":2},{"track_id":3,"timestamp":4}]}`, want: `{"track_ids":[{"time":2,"track":1},{"time":4,"track":3}]}`},
		// Entries with no resolvable track id are dropped.
		{name: "entry without track id dropped", raw: `{"playlist_contents":[{"time":1},{"track":2,"time":3}]}`, want: `{"track_ids":[{"time":3,"track":2}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta map[string]any
			_ = json.Unmarshal([]byte(tt.raw), &meta)
			got := string(normalizePlaylistContentsJSON(meta))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// isEffectivelyEmpty returns true for JSONB values that represent "no tracks":
// "[]", "{}", or {"track_ids": []}.
func isEffectivelyEmpty(s string) bool {
	switch s {
	case "[]", "{}":
		return true
	}
	var asMap map[string]any
	if err := json.Unmarshal([]byte(s), &asMap); err == nil {
		if v, ok := asMap["track_ids"]; ok {
			if arr, ok := v.([]any); ok {
				return len(arr) == 0
			}
		}
		// Any other top-level keys but no track_ids → also empty.
		if len(asMap) == 0 {
			return true
		}
	}
	var asArr []any
	if err := json.Unmarshal([]byte(s), &asArr); err == nil {
		return len(asArr) == 0
	}
	return false
}
