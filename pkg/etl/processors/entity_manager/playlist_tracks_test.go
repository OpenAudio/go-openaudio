package entity_manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
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
		{
			name: "hashid-encoded track ids decode",
			raw:  `{"playlist_contents":{"track_ids":[{"track":"1aV5byE","time":100}]}}`,
			// Hashids(min_length=5, salt='azowernasdfoia').decode('1aV5byE') == (1031900541,)
			want: []int64{1031900541},
		},
		{
			name: "numeric string track id falls back to atoi",
			raw:  `{"playlist_contents":{"track_ids":[{"track":"2007777","time":100}]}}`,
			want: []int64{2007777},
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

// buildParamsAt is buildParams with an explicit block time, so a test can
// replay a transaction as of a historical block rather than "now".
func buildParamsAt(t *testing.T, pool *pgxpool.Pool, entityType, action string, userID, entityID int64, signer, metadata string, blockTime time.Time) *Params {
	t.Helper()
	tx := buildManageEntityTx(entityType, action, userID, entityID, signer, metadata)
	logger, _ := zap.NewDevelopment()
	return NewParams(
		tx.GetManageEntity(),
		100,
		blockTime,
		fmt.Sprintf("blockhash-%s-%s-%d", entityType, action, entityID),
		fmt.Sprintf("txhash-%s-%s-%d", entityType, action, entityID),
		pool,
		logger,
	)
}

// playlist_tracks timestamps must come from the block, not the wall clock.
// A genesis replay walks years of history in minutes: if these columns take
// now(), every row in the table collapses onto the instant the replay ran and
// the junction table can no longer be ordered or windowed by time. The sibling
// playlists.created_at already writes params.BlockTime — this asserts the
// junction table agrees.
func TestPlaylistTracks_TimestampsUseBlockTime(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 420)
	pid := int64(PlaylistIDOffset + 4200)
	t1 := int64(TrackIDOffset + 6200)
	t2 := int64(TrackIDOffset + 6201)

	seedUser(t, pool, uid, "0xpltrxtime", "ptt")
	seedTrackFull(t, pool, t1, uid, "T1")
	seedTrackFull(t, pool, t2, uid, "T2")

	createTime := time.Date(2019, 4, 30, 12, 0, 0, 0, time.UTC)
	updateTime := time.Date(2021, 9, 9, 8, 30, 0, 0, time.UTC)

	createMeta := `{"playlist_name":"Historical","is_album":false,"is_private":false,"playlist_contents":{"track_ids":[{"track":2006200,"time":1700000000},{"track":2006201,"time":1700000100}]}}`
	mustHandle(t, PlaylistCreate(),
		buildParamsAt(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xpltrxtime", createMeta, createTime))

	readTimes := func(trackID int64) (time.Time, time.Time) {
		t.Helper()
		var createdAt, updatedAt time.Time
		if err := pool.QueryRow(context.Background(),
			"SELECT created_at, updated_at FROM playlist_tracks WHERE playlist_id = $1 AND track_id = $2",
			pid, trackID).Scan(&createdAt, &updatedAt); err != nil {
			t.Fatalf("read timestamps for track %d: %v", trackID, err)
		}
		return createdAt.UTC(), updatedAt.UTC()
	}

	// INSERT path: both columns are the creating block's time, not the
	// column DEFAULT CURRENT_TIMESTAMP.
	for _, trackID := range []int64{t1, t2} {
		createdAt, updatedAt := readTimes(trackID)
		if !createdAt.Equal(createTime) {
			t.Errorf("track %d created_at = %s, want block time %s", trackID, createdAt, createTime)
		}
		if !updatedAt.Equal(createTime) {
			t.Errorf("track %d updated_at = %s, want block time %s", trackID, updatedAt, createTime)
		}
	}

	// UPDATE path: removing t2 stamps updated_at with the removing block's
	// time and leaves created_at on the original block.
	updateMeta := `{"playlist_contents":{"track_ids":[{"track":2006200,"time":1700000000}]}}`
	mustHandle(t, PlaylistUpdate(),
		buildParamsAt(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xpltrxtime", updateMeta, updateTime))

	createdAt, updatedAt := readTimes(t2)
	if !createdAt.Equal(createTime) {
		t.Errorf("removed track created_at = %s, want unchanged block time %s", createdAt, createTime)
	}
	if !updatedAt.Equal(updateTime) {
		t.Errorf("removed track updated_at = %s, want block time %s", updatedAt, updateTime)
	}

	// And the recovery path: adding t2 back clears is_removed with the
	// re-adding block's time.
	readdTime := time.Date(2022, 1, 15, 3, 0, 0, 0, time.UTC)
	mustHandle(t, PlaylistUpdate(),
		buildParamsAt(t, pool, EntityTypePlaylist, ActionUpdate, uid, pid, "0xpltrxtime", createMeta, readdTime))

	createdAt, updatedAt = readTimes(t2)
	if !createdAt.Equal(createTime) {
		t.Errorf("re-added track created_at = %s, want unchanged block time %s", createdAt, createTime)
	}
	if !updatedAt.Equal(readdTime) {
		t.Errorf("re-added track updated_at = %s, want block time %s", updatedAt, readdTime)
	}
}
