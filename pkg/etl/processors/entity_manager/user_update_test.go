package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUserUpdate_TxType(t *testing.T) {
	h := UserUpdate()
	if h.EntityType() != EntityTypeUser {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeUser)
	}
	if h.Action() != ActionUpdate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionUpdate)
	}
}

func TestUserUpdate_StatelessValidation(t *testing.T) {
	tests := []struct {
		name       string
		entityType string
		action     string
		metadata   string
		wantErr    string
	}{
		{
			name:       "wrong entity type",
			entityType: EntityTypeTrack,
			action:     ActionUpdate,
			metadata:   `{"name":"Alice"}`,
			wantErr:    "wrong entity type",
		},
		{
			name:       "wrong action",
			entityType: EntityTypeUser,
			action:     ActionCreate,
			metadata:   `{"name":"Alice"}`,
			wantErr:    "wrong action",
		},
		{
			name:       "bio too long",
			entityType: EntityTypeUser,
			action:     ActionUpdate,
			metadata:   `{"bio":"` + strings.Repeat("x", CharacterLimitUserBio+1) + `"}`,
			wantErr:    "bio exceeds",
		},
		{
			name:       "name too long",
			entityType: EntityTypeUser,
			action:     ActionUpdate,
			metadata:   `{"name":"` + strings.Repeat("x", CharacterLimitUserName+1) + `"}`,
			wantErr:    "name exceeds",
		},
		{
			name:       "handle illegal characters",
			entityType: EntityTypeUser,
			action:     ActionUpdate,
			metadata:   `{"handle":"alice@#$"}`,
			wantErr:    "illegal characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &Params{
				UserID:     UserIDOffset + 1,
				EntityID:   UserIDOffset + 1,
				EntityType: tt.entityType,
				Action:     tt.action,
				Signer:     "0xabc123",
			}
			if tt.metadata != "" {
				params.RawMetadata = tt.metadata
				var meta map[string]any
				if err := json.Unmarshal([]byte(tt.metadata), &meta); err == nil {
					params.Metadata = meta
				}
			}
			err := validateUserUpdate(context.Background(), params)
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

// Database-backed tests (skipped unless ETL_TEST_DB_URL is set)

func TestUserUpdate_Success_ChangesName(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	h := UserUpdate()
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", `{"name":"Alice Updated"}`)
	mustHandle(t, h, params)

	var name string
	err := pool.QueryRow(context.Background(), "SELECT name FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&name)
	if err != nil {
		t.Fatalf("failed to query updated user: %v", err)
	}
	if name != "Alice Updated" {
		t.Errorf("name = %q, want %q", name, "Alice Updated")
	}
}

func TestUserUpdate_HandleChange(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedUser(t, pool, UserIDOffset+2, "0xotherwallet", "other")
	h := UserUpdate()
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", `{"handle":"alice2"}`)
	mustHandle(t, h, params)

	var handle string
	err := pool.QueryRow(context.Background(), "SELECT handle FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&handle)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if handle != "alice2" {
		t.Errorf("handle = %q, want %q", handle, "alice2")
	}
}

func TestUserUpdate_RejectsHandleCollision(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedUser(t, pool, UserIDOffset+2, "0xotherwallet", "bob")
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", `{"handle":"Bob"}`)
	mustReject(t, UserUpdate(), params, "handle")
}

func TestUserUpdate_RejectsSignerMismatch(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xWrongWallet", `{"name":"Hacked"}`)
	mustReject(t, UserUpdate(), params, "signer")
}

func TestUserUpdate_RejectsUserNotFound(t *testing.T) {
	pool := setupTestDB(t)
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xNewWallet", `{"name":"Alice"}`)
	mustReject(t, UserUpdate(), params, "does not exist")
}

func TestUserUpdate_ArtistPickTrackId(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedTrack(t, pool, TrackIDOffset+1, UserIDOffset+1)
	h := UserUpdate()
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", `{"artist_pick_track_id":2000001}`)
	mustHandle(t, h, params)

	var artistPick *int64
	err := pool.QueryRow(context.Background(), "SELECT artist_pick_track_id FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&artistPick)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if artistPick == nil || *artistPick != TrackIDOffset+1 {
		t.Errorf("artist_pick_track_id = %v, want %d", artistPick, TrackIDOffset+1)
	}
}

func TestUserUpdate_RejectsArtistPickTrackNotOwned(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedUser(t, pool, UserIDOffset+2, "0xbobwallet", "bob")
	seedTrack(t, pool, TrackIDOffset+1, UserIDOffset+2) // track owned by bob
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", `{"artist_pick_track_id":2000001}`)
	mustReject(t, UserUpdate(), params, "does not exist")
}
