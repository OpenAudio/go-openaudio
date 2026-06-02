package entity_manager

import (
	"context"
	"database/sql"
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
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 42)
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

func TestTrackCreate_PersistsBpmAndKey(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 43)
	seedUser(t, pool, uid, "0xbpmowner", "bpmowner")
	meta := `{"owner_id":3000001,"title":"Keyed","genre":"Electronic","bpm":128.5,"musical_key":"D flat minor","is_custom_bpm":true,"is_custom_musical_key":true,"audio_upload_id":"upload-abc"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xBpmOwner", meta)
	mustHandle(t, TrackCreate(), params)

	var bpm float64
	var key, uploadID string
	var customBpm, customKey bool
	err := pool.QueryRow(context.Background(),
		"SELECT bpm, musical_key, is_custom_bpm, is_custom_musical_key, audio_upload_id FROM tracks WHERE track_id = $1 AND is_current = true", tid).
		Scan(&bpm, &key, &customBpm, &customKey, &uploadID)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	if bpm != 128.5 {
		t.Errorf("bpm = %v, want 128.5", bpm)
	}
	if key != "D flat minor" {
		t.Errorf("musical_key = %q, want %q", key, "D flat minor")
	}
	if !customBpm || !customKey {
		t.Errorf("is_custom_bpm = %v, is_custom_musical_key = %v, want both true", customBpm, customKey)
	}
	if uploadID != "upload-abc" {
		t.Errorf("audio_upload_id = %q, want %q", uploadID, "upload-abc")
	}
}

// TestTrackCreate_RejectsInvalidMusicalKey mirrors apps' is_valid_musical_key:
// an unrecognized key (here a sharp, which the enum does not use) is dropped,
// leaving musical_key NULL rather than persisting the bad value.
func TestTrackCreate_RejectsInvalidMusicalKey(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 44)
	seedUser(t, pool, uid, "0xbadkey", "badkey")
	meta := `{"owner_id":3000001,"title":"BadKey","genre":"Electronic","bpm":0,"musical_key":"C# minor"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xBadKey", meta)
	mustHandle(t, TrackCreate(), params)

	var bpm sql.NullFloat64
	var key sql.NullString
	err := pool.QueryRow(context.Background(),
		"SELECT bpm, musical_key FROM tracks WHERE track_id = $1 AND is_current = true", tid).
		Scan(&bpm, &key)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	if key.Valid {
		t.Errorf("musical_key = %q, want NULL for invalid key", key.String)
	}
	if bpm.Valid {
		t.Errorf("bpm = %v, want NULL for zero bpm", bpm.Float64)
	}
}

func TestTrackCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 50)
	seedUser(t, pool, uid, "0xowner", "o")
	seedTrack(t, pool, tid, uid)
	meta := `{"owner_id":3000001,"title":"Dup","genre":"Pop"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xOwner", meta)
	mustReject(t, TrackCreate(), params, "already exists")
}

func TestTrackCreate_RejectsSignerMismatch(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 51)
	seedUser(t, pool, uid, "0xrealowner", "ro")
	meta := `{"owner_id":3000001,"title":"X","genre":"Rock"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xWrong", meta)
	mustReject(t, TrackCreate(), params, "signer")
}
