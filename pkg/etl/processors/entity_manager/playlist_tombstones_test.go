package entity_manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// A migrated Playlist/Create carrying removal history must reproduce it: the
// junction tombstones with the source's own timestamps, and the reverse index
// on the track. The removal record is what the API's usdc_purchase check reads
// to decide whether someone who bought the album still has access to a track
// that later left it, so a migration that drops the history silently revokes
// access for every such purchase — 19,441 tracks in the source have one.
func TestMigratedPlaylistCreate_CarriesRemovalHistory(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 421)
	pid := int64(PlaylistIDOffset + 4201)
	kept := int64(TrackIDOffset + 6300)
	left := int64(TrackIDOffset + 6301)

	seedUser(t, pool, uid, "0xpltomb", "ptb")
	seedTrackFull(t, pool, kept, uid, "Kept")
	seedTrackFull(t, pool, left, uid, "Left")

	joined := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	departed := time.Date(2025, 7, 14, 8, 30, 0, 0, time.UTC)

	meta := fmt.Sprintf(`{
		"playlist_name": "Album",
		"is_album": true,
		"playlist_contents": {"track_ids": [{"track": %d, "time": 1700000000}]},
		"removed_tracks": [{"track_id": %d, "created_at": %q, "updated_at": %q}]
	}`, kept, left, joined.Format(time.RFC3339), departed.Format(time.RFC3339))

	mustHandle(t, migratedPlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xpltomb", meta))

	// The junction row is a tombstone, not a member, and carries the source's
	// timestamps: created_at is when the track joined, updated_at when it left.
	var isRemoved bool
	var createdAt, updatedAt time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT is_removed, created_at, updated_at FROM playlist_tracks
		 WHERE playlist_id = $1 AND track_id = $2`, pid, left).
		Scan(&isRemoved, &createdAt, &updatedAt); err != nil {
		t.Fatalf("removed track has no playlist_tracks row: %v", err)
	}
	if !isRemoved {
		t.Errorf("playlist_tracks row for the removed track is not a tombstone")
	}
	if !createdAt.Equal(joined) {
		t.Errorf("created_at = %s, want the source join time %s", createdAt.UTC(), joined)
	}
	if !updatedAt.Equal(departed) {
		t.Errorf("updated_at = %s, want the source removal time %s", updatedAt.UTC(), departed)
	}

	// The live track is unaffected.
	var keptRemoved bool
	if err := pool.QueryRow(context.Background(),
		`SELECT is_removed FROM playlist_tracks WHERE playlist_id = $1 AND track_id = $2`,
		pid, kept).Scan(&keptRemoved); err != nil {
		t.Fatalf("kept track has no playlist_tracks row: %v", err)
	}
	if keptRemoved {
		t.Errorf("kept track was marked removed")
	}

	readIndex := func(trackID int64) ([]int32, string) {
		t.Helper()
		var containing []int32
		var previously string
		if err := pool.QueryRow(context.Background(),
			`SELECT playlists_containing_track, playlists_previously_containing_track::text
			 FROM tracks WHERE track_id = $1 AND is_current = true`,
			trackID).Scan(&containing, &previously); err != nil {
			t.Fatalf("read reverse index for %d: %v", trackID, err)
		}
		return containing, previously
	}
	holds := func(ids []int32, want int64) bool {
		for _, id := range ids {
			if int64(id) == want {
				return true
			}
		}
		return false
	}

	if containing, _ := readIndex(kept); !holds(containing, pid) {
		t.Errorf("kept track missing from playlists_containing_track: %v", containing)
	}

	containing, previously := readIndex(left)
	if holds(containing, pid) {
		t.Errorf("removed track still lists the playlist as containing it: %v", containing)
	}
	// The shape is fixed by the API contract: a jsonb object keyed by playlist
	// id, whose value carries the removal time as a Unix epoch.
	var record map[string]map[string]int64
	if err := json.Unmarshal([]byte(previously), &record); err != nil {
		t.Fatalf("removal record %q is not a jsonb object: %v", previously, err)
	}
	entry, ok := record[itoa(pid)]
	if !ok {
		t.Fatalf("removal record %q has no entry for playlist %d", previously, pid)
	}
	if entry["time"] != departed.Unix() {
		t.Errorf("removal time = %d, want the source removal time %d", entry["time"], departed.Unix())
	}
}

// A production create carries no removal history, so it must write no
// tombstones — the zero playlistState has to stay a no-op.
func TestPlaylistCreate_WritesNoTombstones(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 422)
	pid := int64(PlaylistIDOffset + 4202)
	tid := int64(TrackIDOffset + 6302)

	seedUser(t, pool, uid, "0xplnotomb", "pnt")
	seedTrackFull(t, pool, tid, uid, "Only")

	meta := fmt.Sprintf(`{"playlist_name":"Plain","playlist_contents":{"track_ids":[{"track":%d,"time":1700000000}]}}`, tid)
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xplnotomb", meta))

	var tombstones int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM playlist_tracks WHERE playlist_id = $1 AND is_removed = true`,
		pid).Scan(&tombstones); err != nil {
		t.Fatalf("count tombstones: %v", err)
	}
	if tombstones != 0 {
		t.Errorf("a production create wrote %d tombstones, want 0", tombstones)
	}
}

// A track cannot be both in the playlist and removed from it. If the source
// says both, the live membership wins: replaying must never mark a present
// track removed.
func TestMigratedPlaylistCreate_LiveMembershipBeatsTombstone(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 423)
	pid := int64(PlaylistIDOffset + 4203)
	tid := int64(TrackIDOffset + 6303)

	seedUser(t, pool, uid, "0xplboth", "pbo")
	seedTrackFull(t, pool, tid, uid, "Contested")

	meta := fmt.Sprintf(`{
		"playlist_name": "Contested",
		"playlist_contents": {"track_ids": [{"track": %d, "time": 1700000000}]},
		"removed_tracks": [{"track_id": %d, "created_at": "2024-03-01T12:00:00Z", "updated_at": "2025-07-14T08:30:00Z"}]
	}`, tid, tid)

	mustHandle(t, migratedPlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xplboth", meta))

	var isRemoved bool
	if err := pool.QueryRow(context.Background(),
		`SELECT is_removed FROM playlist_tracks WHERE playlist_id = $1 AND track_id = $2`,
		pid, tid).Scan(&isRemoved); err != nil {
		t.Fatalf("read junction row: %v", err)
	}
	if isRemoved {
		t.Errorf("a track present in the contents was marked removed")
	}

	var previously string
	if err := pool.QueryRow(context.Background(),
		`SELECT playlists_previously_containing_track::text FROM tracks
		 WHERE track_id = $1 AND is_current = true`, tid).Scan(&previously); err != nil {
		t.Fatalf("read reverse index: %v", err)
	}
	if previously != "{}" {
		t.Errorf("a present track gained a removal record: %q", previously)
	}
}

// Tracks that left at different times must each keep their own timestamp: the
// removal time is compared against a purchase date, so collapsing them onto one
// would change who is entitled.
func TestMigratedPlaylistCreate_PerTrackRemovalTimes(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 424)
	pid := int64(PlaylistIDOffset + 4204)
	early := int64(TrackIDOffset + 6304)
	late := int64(TrackIDOffset + 6305)

	seedUser(t, pool, uid, "0xpltimes", "ptm")
	seedTrackFull(t, pool, early, uid, "Early")
	seedTrackFull(t, pool, late, uid, "Late")

	earlyAt := time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC)
	lateAt := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)

	meta := fmt.Sprintf(`{
		"playlist_name": "Two departures",
		"removed_tracks": [
			{"track_id": %d, "created_at": "2024-01-01T00:00:00Z", "updated_at": %q},
			{"track_id": %d, "created_at": "2024-01-01T00:00:00Z", "updated_at": %q}
		]
	}`, early, earlyAt.Format(time.RFC3339), late, lateAt.Format(time.RFC3339))

	mustHandle(t, migratedPlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xpltimes", meta))

	for trackID, want := range map[int64]time.Time{early: earlyAt, late: lateAt} {
		var previously string
		if err := pool.QueryRow(context.Background(),
			`SELECT playlists_previously_containing_track::text FROM tracks
			 WHERE track_id = $1 AND is_current = true`, trackID).Scan(&previously); err != nil {
			t.Fatalf("read reverse index for %d: %v", trackID, err)
		}
		var record map[string]map[string]int64
		if err := json.Unmarshal([]byte(previously), &record); err != nil {
			t.Fatalf("removal record %q is not a jsonb object: %v", previously, err)
		}
		if got := record[itoa(pid)]["time"]; got != want.Unix() {
			t.Errorf("track %d removal time = %d, want %d", trackID, got, want.Unix())
		}
	}
}
