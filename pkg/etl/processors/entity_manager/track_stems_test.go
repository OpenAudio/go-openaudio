package entity_manager

import (
	"context"
	"testing"
)

func TestUpdateStemsTable_NoOpWithoutStemOf(t *testing.T) {
	// No DB call expected because stem_of is missing.
	meta := map[string]any{"title": "foo"}
	if err := updateStemsTable(context.Background(), nil, 100, meta); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

func TestUpdateStemsTable_NoOpWithoutParentTrackID(t *testing.T) {
	meta := map[string]any{"stem_of": map[string]any{}}
	if err := updateStemsTable(context.Background(), nil, 100, meta); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

func TestTrackCreate_PopulatesStemsTable(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 100)
	parentID := int64(TrackIDOffset + 200)
	childID := int64(TrackIDOffset + 201)

	seedUser(t, pool, uid, "0xstemowner", "stemowner")
	seedTrackFull(t, pool, parentID, uid, "Parent Song")

	meta := `{"owner_id":3000100,"title":"Vocal Stem","genre":"Electronic","stem_of":{"category":"vocals","parent_track_id":2000200}}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, childID, "0xstemowner", meta)
	mustHandle(t, TrackCreate(), params)

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM stems WHERE parent_track_id = $1 AND child_track_id = $2",
		parentID, childID).Scan(&count); err != nil {
		t.Fatalf("count stems: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 stems row, got %d", count)
	}
}

func TestTrackDelete_RemovesStemsForChild(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 160)
	parentID := int64(TrackIDOffset + 800)
	childID := int64(TrackIDOffset + 801)

	seedUser(t, pool, uid, "0xstemdel", "stemdel")
	seedTrackFull(t, pool, parentID, uid, "Parent")
	seedTrackFull(t, pool, childID, uid, "Stem Child")

	if _, err := pool.Exec(context.Background(),
		"INSERT INTO stems (parent_track_id, child_track_id) VALUES ($1, $2)",
		parentID, childID); err != nil {
		t.Fatalf("seed stem: %v", err)
	}

	params := buildParams(t, pool, EntityTypeTrack, ActionDelete, uid, childID, "0xstemdel", "")
	mustHandle(t, TrackDelete(), params)

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM stems WHERE child_track_id = $1", childID).Scan(&count); err != nil {
		t.Fatalf("count stems: %v", err)
	}
	if count != 0 {
		t.Errorf("expected stems for child to be removed, got %d rows", count)
	}
}
