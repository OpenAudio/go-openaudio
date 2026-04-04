package entity_manager

import (
	"context"
	"testing"
)

func TestTrackDownload_TxType(t *testing.T) {
	h := TrackDownload()
	if h.EntityType() != EntityTypeTrack {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeTrack)
	}
	if h.Action() != ActionDownload {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionDownload)
	}
}

func TestTrackDownload_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 1)
	ownerID := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xdownloader", "downloader")
	seedUser(t, pool, ownerID, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, ownerID)

	meta := `{"city":"San Francisco","region":"CA","country":"US"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionDownload, uid, tid, "0xDownloader", meta)
	mustHandle(t, TrackDownload(), params)

	var city, region, country string
	err := pool.QueryRow(context.Background(),
		"SELECT city, region, country FROM track_downloads WHERE track_id = $1 AND user_id = $2",
		tid, uid).Scan(&city, &region, &country)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if city != "San Francisco" || region != "CA" || country != "US" {
		t.Errorf("got city=%q region=%q country=%q", city, region, country)
	}
}

func TestTrackDownload_SkipsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 2)
	ownerID := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xdownloader", "downloader")
	seedUser(t, pool, ownerID, "0xtrackowner", "trackowner")
	seedTrack(t, pool, tid, ownerID)

	params := buildParams(t, pool, EntityTypeTrack, ActionDownload, uid, tid, "0xDownloader", `{}`)
	mustHandle(t, TrackDownload(), params)
	// Same txhash should silently skip
	mustHandle(t, TrackDownload(), params)

	var count int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM track_downloads WHERE track_id = $1", tid).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 download, got %d", count)
	}
}

func TestTrackDownload_RejectsNonexistentTrack(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xdownloader", "downloader")
	params := buildParams(t, pool, EntityTypeTrack, ActionDownload, uid, TrackIDOffset+999, "0xDownloader", `{}`)
	mustReject(t, TrackDownload(), params, "does not exist")
}
