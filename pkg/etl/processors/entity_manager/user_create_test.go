package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUserCreate_TxType(t *testing.T) {
	h := UserCreate()
	if h.EntityType() != EntityTypeUser {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeUser)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestUserCreate_StatelessValidation(t *testing.T) {
	tests := []struct {
		name       string
		entityType string
		userID     int64
		metadata   string
		wantErr    string
	}{
		{
			name:       "wrong entity type",
			entityType: EntityTypeTrack,
			userID:     UserIDOffset + 1,
			metadata:   `{"handle":"alice","name":"Alice"}`,
			wantErr:    "wrong entity type",
		},
		{
			name:       "user_id below offset",
			entityType: EntityTypeUser,
			userID:     999,
			metadata:   `{"handle":"alice","name":"Alice"}`,
			wantErr:    "below offset",
		},
		{
			name:       "bio too long",
			entityType: EntityTypeUser,
			userID:     UserIDOffset + 1,
			metadata:   `{"handle":"alice","name":"Alice","bio":"` + strings.Repeat("x", CharacterLimitUserBio+1) + `"}`,
			wantErr:    "bio exceeds",
		},
		{
			name:       "name too long",
			entityType: EntityTypeUser,
			userID:     UserIDOffset + 1,
			metadata:   `{"handle":"alice","name":"` + strings.Repeat("x", CharacterLimitUserName+1) + `"}`,
			wantErr:    "name exceeds",
		},
		{
			name:       "handle too long",
			entityType: EntityTypeUser,
			userID:     UserIDOffset + 1,
			metadata:   `{"handle":"` + strings.Repeat("a", CharacterLimitHandle+1) + `","name":"Alice"}`,
			wantErr:    "exceeds",
		},
		{
			name:       "handle illegal characters",
			entityType: EntityTypeUser,
			userID:     UserIDOffset + 1,
			metadata:   `{"handle":"alice@#$","name":"Alice"}`,
			wantErr:    "illegal characters",
		},
		{
			name:       "handle reserved word",
			entityType: EntityTypeUser,
			userID:     UserIDOffset + 1,
			metadata:   `{"handle":"admin","name":"Admin"}`,
			wantErr:    "reserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &Params{
				UserID:     tt.userID,
				EntityType: tt.entityType,
				Action:     ActionCreate,
				Signer:     "0xabc123",
			}

			if tt.metadata != "" {
				params.RawMetadata = tt.metadata
				var meta map[string]any
				if err := json.Unmarshal([]byte(tt.metadata), &meta); err == nil {
					params.Metadata = meta
				}
			}

			err := validateUserCreate(context.Background(), params)
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

// Tests below require a database. They are skipped unless ETL_TEST_DB_URL is set.

func TestUserCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	h := UserCreate()
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, UserIDOffset+1, UserIDOffset+1, "0xNewWallet123", `{"handle":"alice","name":"Alice","bio":"hello world"}`)
	mustHandle(t, h, params)

	var handle string
	err := pool.QueryRow(context.Background(), "SELECT handle FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&handle)
	if err != nil {
		t.Fatalf("failed to query inserted user: %v", err)
	}
	if handle != "alice" {
		t.Errorf("handle = %q, want %q", handle, "alice")
	}
}

func TestUserCreate_RejectsExistingUser(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xexistingwallet", "existing")
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, UserIDOffset+1, UserIDOffset+1, "0xNewWallet999", `{"handle":"newhandle","name":"New User"}`)
	mustReject(t, UserCreate(), params, "already exists")
}

func TestUserCreate_RejectsDuplicateWallet(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xsharedwallet", "existinguser")
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, UserIDOffset+2, UserIDOffset+2, "0xSharedWallet", `{"handle":"newuser","name":"New"}`)
	mustReject(t, UserCreate(), params, "wallet")
}

func TestUserCreate_RejectsDuplicateHandle(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xwallet1", "alice")
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, UserIDOffset+2, UserIDOffset+2, "0xwallet2", `{"handle":"Alice","name":"Alice 2"}`)
	mustReject(t, UserCreate(), params, "handle")
}

func TestUserCreate_NoMetadata(t *testing.T) {
	pool := setupTestDB(t)
	h := UserCreate()
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, UserIDOffset+1, UserIDOffset+1, "0xNewWallet", "")
	mustHandle(t, h, params)

	var wallet string
	err := pool.QueryRow(context.Background(), "SELECT wallet FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&wallet)
	if err != nil {
		t.Fatalf("failed to query inserted user: %v", err)
	}
	if wallet != "0xnewwallet" {
		t.Errorf("wallet = %q, want %q", wallet, "0xnewwallet")
	}
}
