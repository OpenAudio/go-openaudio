package entity_manager

import (
	"context"
	"testing"
)

// Regression test for track_download dedupe semantics. apps' Python helper
// (track.py:download_track) silently returns when a row already exists at
// (parent_track_id, track_id, txhash). Replays of the same download tx
// (e.g. from chain re-processing) must not produce duplicate rows.
func TestTrackDownload_DedupesByTxHash(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 800)
	owner := int64(UserIDOffset + 801)
	tid := int64(TrackIDOffset + 9700)
	seedUser(t, pool, uid, "0xdler", "dlu")
	seedUser(t, pool, owner, "0xtrkown", "ow2")
	seedTrackFull(t, pool, tid, owner, "Downloadable")

	params := buildParams(t, pool, EntityTypeTrack, ActionDownload, uid, tid, "0xdler", `{}`)
	mustHandle(t, TrackDownload(), params)
	// Same params (same txhash) replayed — should be a no-op.
	mustHandle(t, TrackDownload(), params)

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM track_downloads WHERE track_id = $1",
		tid).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 track_downloads row after replay, got %d", count)
	}
}
