package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlaylistCreate_TxType(t *testing.T) {
	h := PlaylistCreate()
	if h.EntityType() != EntityTypePlaylist {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypePlaylist)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestPlaylistCreate_StatelessValidation(t *testing.T) {
	baseMeta := `{"playlist_name":"My Playlist"}`
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
			entityID:   PlaylistIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   baseMeta,
			wantErr:    "wrong entity type",
		},
		{
			name:       "wrong action",
			entityType: EntityTypePlaylist,
			action:     ActionUpdate,
			entityID:   PlaylistIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   baseMeta,
			wantErr:    "wrong action",
		},
		{
			name:       "playlist id below offset",
			entityType: EntityTypePlaylist,
			action:     ActionCreate,
			entityID:   100,
			userID:     UserIDOffset + 1,
			metadata:   baseMeta,
			wantErr:    "below offset",
		},
		{
			name:       "missing metadata",
			entityType: EntityTypePlaylist,
			action:     ActionCreate,
			entityID:   PlaylistIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   "",
			wantErr:    "metadata is required",
		},
		{
			name:       "description too long",
			entityType: EntityTypePlaylist,
			action:     ActionCreate,
			entityID:   PlaylistIDOffset + 1,
			userID:     UserIDOffset + 1,
			metadata:   `{"playlist_name":"P","description":"` + strings.Repeat("x", CharacterLimitDescription+1) + `"}`,
			wantErr:    "description exceeds",
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
			err := validatePlaylistCreate(context.Background(), params)
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

func TestPlaylistCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 42)
	seedUser(t, pool, uid, "0xplaylistowner", "playlistowner")
	h := PlaylistCreate()
	meta := `{"playlist_name":"My Cool Playlist","is_album":false}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xPlaylistOwner", meta)
	mustHandle(t, h, params)

	var name string
	err := pool.QueryRow(context.Background(),
		"SELECT playlist_name FROM playlists WHERE playlist_id = $1 AND is_current = true", pid).Scan(&name)
	if err != nil {
		t.Fatalf("query playlist: %v", err)
	}
	if name != "My Cool Playlist" {
		t.Errorf("playlist_name = %q", name)
	}

	var slug string
	err = pool.QueryRow(context.Background(),
		"SELECT slug FROM playlist_routes WHERE playlist_id = $1 AND is_current = true", pid).Scan(&slug)
	if err != nil {
		t.Fatalf("query route: %v", err)
	}
	if slug == "" {
		t.Error("expected non-empty slug")
	}
}

func TestPlaylistCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 50)
	seedUser(t, pool, uid, "0xowner", "o")
	seedPlaylist(t, pool, pid, uid)
	meta := `{"playlist_name":"Dup"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xOwner", meta)
	mustReject(t, PlaylistCreate(), params, "already exists")
}

func TestPlaylistCreate_RejectsSignerMismatch(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 51)
	seedUser(t, pool, uid, "0xrealowner", "ro")
	meta := `{"playlist_name":"X"}`
	params := buildParams(t, pool, EntityTypePlaylist, ActionCreate, uid, pid, "0xWrong", meta)
	mustReject(t, PlaylistCreate(), params, "signer")
}
