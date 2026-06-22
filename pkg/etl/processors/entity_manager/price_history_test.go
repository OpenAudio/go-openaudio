package entity_manager

import (
	"context"
	"testing"
)

func TestTrackCreate_WritesTrackPriceHistory(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 500)
	tid := int64(TrackIDOffset + 7000)
	seedUser(t, pool, uid, "0xpriced", "pricedu")

	meta := `{
		"owner_id":3000500,
		"title":"Premium Track",
		"genre":"Electronic",
		"is_stream_gated":true,
		"is_download_gated":true,
		"stream_conditions":{"usdc_purchase":{"price":199,"splits":[{"user_id":3000500,"percentage":100}]}},
		"download_conditions":{"usdc_purchase":{"price":199,"splits":[{"user_id":3000500,"percentage":100}]}}
	}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xpriced", meta))

	var price int64
	var access string
	if err := pool.QueryRow(context.Background(),
		"SELECT total_price_cents, access::text FROM track_price_history WHERE track_id = $1",
		tid).Scan(&price, &access); err != nil {
		t.Fatalf("track_price_history: %v", err)
	}
	if price != 199 {
		t.Errorf("total_price_cents = %d, want 199", price)
	}
	if access != "stream" {
		t.Errorf("access = %q, want stream", access)
	}
}

func TestTrackCreate_NoPriceHistoryForFreeTrack(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 510)
	tid := int64(TrackIDOffset + 7100)
	seedUser(t, pool, uid, "0xfree", "freeu")

	meta := `{"owner_id":3000510,"title":"Free Track","genre":"Electronic"}`
	mustHandle(t, TrackCreate(),
		buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xfree", meta))

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM track_price_history WHERE track_id = $1",
		tid).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no price history rows for free track, got %d", count)
	}
}

func TestPlaylistCreate_WritesAlbumPriceHistory(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 520)
	pid := int64(PlaylistIDOffset + 8000)
	seedUser(t, pool, uid, "0xalbum", "albu")

	meta := `{
		"playlist_name":"Premium Album",
		"is_album":true,
		"is_private":false,
		"is_stream_gated":true,
		"stream_conditions":{"usdc_purchase":{"price":499,"splits":[{"user_id":3000520,"percentage":100}]}}
	}`
	mustHandle(t, PlaylistCreate(),
		buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xalbum", meta))

	var price int64
	if err := pool.QueryRow(context.Background(),
		"SELECT total_price_cents FROM album_price_history WHERE playlist_id = $1",
		pid).Scan(&price); err != nil {
		t.Fatalf("album_price_history: %v", err)
	}
	if price != 499 {
		t.Errorf("total_price_cents = %d, want 499", price)
	}
}
