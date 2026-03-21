package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTrackCreate_TxType(t *testing.T) {
	h := TrackCreate()
	if h.EntityType() != EntityTypeTrack {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeTrack)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestTrackCreate_StatelessValidation(t *testing.T) {
	baseMeta := `{"owner_id":3000001,"title":"T","genre":"Electronic"}`
	tests := []struct {
		name       string
		entityType string
		action     string
		entityID   int64
		userID     int64
		metadata   string
		wantErr    string
	}{
		{
			name:       "wrong entity type",
			entityType: EntityTypeUser,
			action:     ActionCreate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   baseMeta,
			wantErr:    "wrong entity type",
		},
		{
			name:       "wrong action",
			entityType: EntityTypeTrack,
			action:     ActionUpdate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   baseMeta,
			wantErr:    "wrong action",
		},
		{
			name:       "track id below offset",
			entityType: EntityTypeTrack,
			action:     ActionCreate,
			entityID:   100,
			userID:     UserIDOffset + 1,
			metadata:   baseMeta,
			wantErr:    "below offset",
		},
		{
			name:       "missing metadata",
			entityType: EntityTypeTrack,
			action:     ActionCreate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   "",
			wantErr:    "metadata is required",
		},
		{
			name:       "missing owner_id",
			entityType: EntityTypeTrack,
			action:     ActionCreate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   `{"title":"x"}`,
			wantErr:    "owner_id",
		},
		{
			name:       "owner_id mismatch",
			entityType: EntityTypeTrack,
			action:     ActionCreate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   `{"owner_id":9999999,"title":"T"}`,
			wantErr:    "owner_id must match",
		},
		{
			name:       "description too long",
			entityType: EntityTypeTrack,
			action:     ActionCreate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   `{"owner_id":3000001,"title":"T","description":"` + strings.Repeat("x", CharacterLimitDescription+1) + `"}`,
			wantErr:    "description exceeds",
		},
		{
			name:       "invalid genre",
			entityType: EntityTypeTrack,
			action:     ActionCreate,
			entityID:   TrackIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   `{"owner_id":3000001,"title":"T","genre":"Not A Real Genre"}`,
			wantErr:    "allow list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &Params{
				UserID:     tt.userID,
				EntityID:   tt.entityID,
				EntityType: tt.entityType,
				Action:     tt.action,
				Signer:     "0xabc",
			}
			if tt.metadata != "" {
				params.RawMetadata = tt.metadata
				var meta map[string]any
				if err := json.Unmarshal([]byte(tt.metadata), &meta); err == nil {
					params.Metadata = meta
				}
			}
			err := validateTrackCreate(context.Background(), params)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !IsValidationError(err) {
				t.Fatalf("expected ValidationError, got %T: %v", err, err)
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestTrackCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := UserIDOffset + 1
	tid := TrackIDOffset + 42
	seedUser(t, pool, uid, "0xtrackowner", "trackowner")
	h := TrackCreate()
	meta := `{"owner_id":3000001,"title":"Electronic Dreams","genre":"Electronic"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xTrackOwner", meta)
	mustHandle(t, h, params)

	var title string
	err := pool.QueryRow(context.Background(), "SELECT title FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&title)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	if title != "Electronic Dreams" {
		t.Errorf("title = %q", title)
	}
	var slug string
	err = pool.QueryRow(context.Background(), "SELECT slug FROM track_routes WHERE track_id = $1 AND is_current = true", tid).Scan(&slug)
	if err != nil {
		t.Fatalf("query route: %v", err)
	}
	if slug == "" {
		t.Error("expected non-empty slug")
	}
}

func TestTrackCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := UserIDOffset + 1
	tid := TrackIDOffset + 50
	seedUser(t, pool, uid, "0xowner", "o")
	seedTrack(t, pool, tid, uid)
	meta := `{"owner_id":3000001,"title":"Dup","genre":"Pop"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xOwner", meta)
	mustReject(t, TrackCreate(), params, "already exists")
}

func TestTrackCreate_RejectsSignerMismatch(t *testing.T) {
	pool := setupTestDB(t)
	uid := UserIDOffset + 1
	tid := TrackIDOffset + 51
	seedUser(t, pool, uid, "0xrealowner", "ro")
	meta := `{"owner_id":3000001,"title":"X","genre":"Rock"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xWrong", meta)
	mustReject(t, TrackCreate(), params, "signer")
}
