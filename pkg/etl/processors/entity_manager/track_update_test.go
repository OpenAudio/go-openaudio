package entity_manager

import (
	"context"
	"testing"
)

func TestTrackUpdate_TxType(t *testing.T) {
	h := TrackUpdate()
	if h.EntityType() != EntityTypeTrack || h.Action() != ActionUpdate {
		t.Fatalf("unexpected handler type")
	}
}

func TestTrackUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 60)
	seedUser(t, pool, uid, "0xowner", "ou")
	seedTrackFull(t, pool, tid, uid, "Old Title")
	meta := `{"title":"New Title","genre":"Jazz"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", meta)
	mustHandle(t, TrackUpdate(), params)

	var title string
	_ = pool.QueryRow(context.Background(), "SELECT title FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&title)
	if title != "New Title" {
		t.Errorf("title = %q, want New Title", title)
	}
}

func TestTrackUpdate_PersistsBpmAndKey(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 62)
	seedUser(t, pool, uid, "0xowner", "ou")
	seedTrackFull(t, pool, tid, uid, "Old Title")
	meta := `{"title":"Old Title","bpm":90.0,"musical_key":"A major","is_custom_bpm":true,"is_custom_musical_key":false,"audio_upload_id":"up-xyz"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", meta)
	mustHandle(t, TrackUpdate(), params)

	var bpm float64
	var key, uploadID string
	var customBpm, customKey bool
	err := pool.QueryRow(context.Background(),
		"SELECT bpm, musical_key, is_custom_bpm, is_custom_musical_key, audio_upload_id FROM tracks WHERE track_id = $1 AND is_current = true", tid).
		Scan(&bpm, &key, &customBpm, &customKey, &uploadID)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	if bpm != 90.0 {
		t.Errorf("bpm = %v, want 90.0", bpm)
	}
	if key != "A major" {
		t.Errorf("musical_key = %q, want %q", key, "A major")
	}
	if !customBpm || customKey {
		t.Errorf("is_custom_bpm = %v (want true), is_custom_musical_key = %v (want false)", customBpm, customKey)
	}
	if uploadID != "up-xyz" {
		t.Errorf("audio_upload_id = %q, want %q", uploadID, "up-xyz")
	}
}

// TestTrackUpdate_KeepsBpmKeyOnZeroOrInvalid verifies the apps-parity "keep"
// behavior: an update carrying bpm=0 or an invalid musical_key must NOT
// overwrite previously-set values (only an explicit null clears them).
func TestTrackUpdate_KeepsBpmKeyOnZeroOrInvalid(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 63)
	seedUser(t, pool, uid, "0xowner", "ou")
	seedTrackFull(t, pool, tid, uid, "Old Title")

	// First, set valid values.
	set := `{"title":"Old Title","bpm":120.0,"musical_key":"C minor"}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", set))

	// Then an update with bpm=0 and an invalid key must leave them unchanged.
	noop := `{"title":"Old Title","bpm":0,"musical_key":"H sharp"}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", noop))

	var bpm float64
	var key string
	err := pool.QueryRow(context.Background(),
		"SELECT bpm, musical_key FROM tracks WHERE track_id = $1 AND is_current = true", tid).
		Scan(&bpm, &key)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	if bpm != 120.0 {
		t.Errorf("bpm = %v, want 120.0 (unchanged)", bpm)
	}
	if key != "C minor" {
		t.Errorf("musical_key = %q, want %q (unchanged)", key, "C minor")
	}
}

// TestTrackUpdate_KeepsCidsOnNull verifies that an update carrying explicit
// null CIDs does NOT wipe previously-set content identifiers. Clients send
// the full track object on edit and can carry "track_cid": null; letting it
// through makes the track unstreamable even though the audio still exists on
// content nodes (regression: track 1960897973, wiped by a 2026-07-14 edit).
func TestTrackUpdate_KeepsCidsOnNull(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 64)
	seedUser(t, pool, uid, "0xowner", "ou")
	seedTrackFull(t, pool, tid, uid, "Old Title")

	// First, set CIDs the way the upload flow does.
	set := `{"title":"Old Title","track_cid":"baecid320","orig_file_cid":"baecidorig","audio_upload_id":"up-abc"}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", set))

	// Then an edit that carries explicit nulls must leave them unchanged.
	wipe := `{"title":"Old Title","track_cid":null,"orig_file_cid":null,"audio_upload_id":null}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", wipe))

	var trackCid, origCid, uploadID string
	err := pool.QueryRow(context.Background(),
		"SELECT COALESCE(track_cid, ''), COALESCE(orig_file_cid, ''), COALESCE(audio_upload_id, '') FROM tracks WHERE track_id = $1 AND is_current = true", tid).
		Scan(&trackCid, &origCid, &uploadID)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	if trackCid != "baecid320" {
		t.Errorf("track_cid = %q, want %q (unchanged)", trackCid, "baecid320")
	}
	if origCid != "baecidorig" {
		t.Errorf("orig_file_cid = %q, want %q (unchanged)", origCid, "baecidorig")
	}
	if uploadID != "up-abc" {
		t.Errorf("audio_upload_id = %q, want %q (unchanged)", uploadID, "up-abc")
	}
}

func TestTrackUpdate_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 99)
	// Seed user wallet matches the signer so ValidateSigner passes and the
	// test reaches the track-existence check it actually means to exercise.
	seedUser(t, pool, uid, "0xowner", "o")
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xOwner", `{"title":"X"}`)
	mustReject(t, TrackUpdate(), params, "does not exist")
}

func TestTrackUpdate_OwnerMismatch(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	other := int64(UserIDOffset + 2)
	tid := int64(TrackIDOffset + 61)
	seedUser(t, pool, uid, "0xa", "a")
	seedUser(t, pool, other, "0xb", "b")
	seedTrackFull(t, pool, tid, other, "T")
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xa", `{"title":"Hack"}`)
	mustReject(t, TrackUpdate(), params, "does not match")
}
