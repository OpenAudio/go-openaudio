package entity_manager

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGetRemixParentTrackIDs(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     []int64
	}{
		{
			name:     "single parent",
			metadata: `{"remix_of":{"tracks":[{"parent_track_id":2000123}]}}`,
			want:     []int64{2000123},
		},
		{
			name:     "multiple parents",
			metadata: `{"remix_of":{"tracks":[{"parent_track_id":2000001},{"parent_track_id":2000002}]}}`,
			want:     []int64{2000001, 2000002},
		},
		{
			name:     "missing remix_of",
			metadata: `{"title":"foo"}`,
			want:     nil,
		},
		{
			name:     "remix_of without tracks",
			metadata: `{"remix_of":{}}`,
			want:     nil,
		},
		{
			name:     "remix_of with non-int parent_track_id is skipped",
			metadata: `{"remix_of":{"tracks":[{"parent_track_id":"abc"},{"parent_track_id":2000003}]}}`,
			want:     []int64{2000003},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta map[string]any
			if err := json.Unmarshal([]byte(tt.metadata), &meta); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := getRemixParentTrackIDs(meta)
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

func TestTrackCreate_PopulatesRemixesTable(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 110)
	parent1 := int64(TrackIDOffset + 300)
	parent2 := int64(TrackIDOffset + 301)
	childID := int64(TrackIDOffset + 302)

	seedUser(t, pool, uid, "0xremixowner", "remixowner")
	seedTrackFull(t, pool, parent1, uid, "Original A")
	seedTrackFull(t, pool, parent2, uid, "Original B")

	meta := `{"owner_id":3000110,"title":"Remix Master","genre":"Electronic","remix_of":{"tracks":[{"parent_track_id":2000300},{"parent_track_id":2000301}]}}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, childID, "0xremixowner", meta)
	mustHandle(t, TrackCreate(), params)

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM remixes WHERE child_track_id = $1", childID).Scan(&count); err != nil {
		t.Fatalf("count remixes: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 remixes rows, got %d", count)
	}
}

func TestTrackUpdate_RefreshesRemixes(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 140)
	parent1 := int64(TrackIDOffset + 600)
	parent2 := int64(TrackIDOffset + 601)
	childID := int64(TrackIDOffset + 602)

	seedUser(t, pool, uid, "0xremixupd", "remixupd")
	seedTrackFull(t, pool, parent1, uid, "Original X")
	seedTrackFull(t, pool, parent2, uid, "Original Y")
	seedTrackFull(t, pool, childID, uid, "My Remix")

	// Pre-seed an existing remix link to parent1
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO remixes (parent_track_id, child_track_id) VALUES ($1, $2)",
		parent1, childID); err != nil {
		t.Fatalf("seed remix: %v", err)
	}

	// Update the child track to remix only parent2 — parent1 link should be removed.
	meta := `{"remix_of":{"tracks":[{"parent_track_id":2000601}]}}`
	params := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, childID, "0xremixupd", meta)
	mustHandle(t, TrackUpdate(), params)

	var hasParent1, hasParent2 bool
	_ = pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM remixes WHERE parent_track_id = $1 AND child_track_id = $2)",
		parent1, childID).Scan(&hasParent1)
	_ = pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM remixes WHERE parent_track_id = $1 AND child_track_id = $2)",
		parent2, childID).Scan(&hasParent2)

	if hasParent1 {
		t.Error("expected parent1 remix link to be removed")
	}
	if !hasParent2 {
		t.Error("expected parent2 remix link to be present")
	}
}
