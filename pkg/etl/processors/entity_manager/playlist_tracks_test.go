package entity_manager

import (
	"context"
	"encoding/json"
	"testing"
)

func TestExtractPlaylistTrackIDs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []int64
	}{
		{
			name: "legacy {track_ids: [{track:..}]} format",
			raw:  `{"playlist_contents":{"track_ids":[{"track":2000001,"time":100},{"track":2000002,"time":200}]}}`,
			want: []int64{2000001, 2000002},
		},
		{
			name: "new array format with track_id",
			raw:  `{"playlist_contents":[{"track_id":2000003,"time":300}]}`,
			want: []int64{2000003},
		},
		{
			name: "missing playlist_contents",
			raw:  `{}`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta map[string]any
			_ = json.Unmarshal([]byte(tt.raw), &meta)
			got := extractPlaylistTrackIDs(meta)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (got %v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPlaylistCreate_PopulatesPlaylistTracks(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 400)
	pid := int64(PlaylistIDOffset + 4000)
	t1 := int64(TrackIDOffset + 6000)
	t2 := int64(TrackIDOffset + 6001)

	seedUser(t, pool, uid, "0xpltracksu", "pltrxu")
	seedTrackFull(t, pool, t1, uid, "Track One")
	seedTrackFull(t, pool, t2, uid, "Track Two")

	meta := `{"playlist_name":"With Tracks","is_album":false,"is_private":false,"playlist_contents":{"track_ids":[{"track":2006000,"time":1700000000},{"track":2006001,"time":1700000100}]}}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xpltracksu", meta))

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM playlist_tracks WHERE playlist_id = $1 AND is_removed = false",
		pid).Scan(&count); err != nil {
		t.Fatalf("count playlist_tracks: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 active playlist_tracks rows, got %d", count)
	}
}

func TestPlaylistUpdate_RemovesTrackFromPlaylistTracks(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 410)
	pid := int64(PlaylistIDOffset + 4100)
	t1 := int64(TrackIDOffset + 6100)
	t2 := int64(TrackIDOffset + 6101)

	seedUser(t, pool, uid, "0xpltrxupd", "ptu")
	seedTrackFull(t, pool, t1, uid, "T1")
	seedTrackFull(t, pool, t2, uid, "T2")

	createMeta := `{"playlist_name":"Two Tracks","is_album":false,"is_private":false,"playlist_contents":{"track_ids":[{"track":2006100,"time":1700000000},{"track":2006101,"time":1700000100}]}}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xpltrxupd", createMeta))

	updateMeta := `{"playlist_contents":{"track_ids":[{"track":2006100,"time":1700000000}]}}`
	mustHandle(t, PlaylistUpdate(),
		buildParams(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xpltrxupd", updateMeta))

	var t1Removed, t2Removed bool
	if err := pool.QueryRow(context.Background(),
		"SELECT is_removed FROM playlist_tracks WHERE playlist_id = $1 AND track_id = $2",
		pid, t1).Scan(&t1Removed); err != nil {
		t.Fatalf("t1: %v", err)
	}
	if err := pool.QueryRow(context.Background(),
		"SELECT is_removed FROM playlist_tracks WHERE playlist_id = $1 AND track_id = $2",
		pid, t2).Scan(&t2Removed); err != nil {
		t.Fatalf("t2: %v", err)
	}
	if t1Removed {
		t.Error("t1 should still be active")
	}
	if !t2Removed {
		t.Error("t2 should be marked removed")
	}
}
