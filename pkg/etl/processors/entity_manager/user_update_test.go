package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// artistPick reads the stored artist_pick_track_id for a user.
func artistPick(t *testing.T, pool *pgxpool.Pool, userID int64) *int64 {
	t.Helper()
	var pick *int64
	if err := pool.QueryRow(context.Background(),
		"SELECT artist_pick_track_id FROM users WHERE user_id = $1 AND is_current = true", userID).Scan(&pick); err != nil {
		t.Fatalf("query artist_pick_track_id(%d): %v", userID, err)
	}
	return pick
}

// An unownable pick is dropped, not rejected: the rest of the update still
// applies. Previously this returned a ValidationError and threw away the
// whole edit.
func TestUserUpdate_DropsArtistPickTrackNotOwned(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedUser(t, pool, UserIDOffset+2, "0xbobwallet", "bob")
	seedTrack(t, pool, TrackIDOffset+1, UserIDOffset+2) // track owned by bob
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet",
		`{"artist_pick_track_id":2000001,"name":"Alice Renamed"}`)
	mustHandle(t, UserUpdate(), params)

	if got := artistPick(t, pool, UserIDOffset+1); got != nil {
		t.Errorf("artist_pick_track_id = %v, want nil (dropped)", *got)
	}
	var name *string
	if err := pool.QueryRow(context.Background(),
		"SELECT name FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&name); err != nil {
		t.Fatalf("query name: %v", err)
	}
	if name == nil || *name != "Alice Renamed" {
		t.Errorf("name = %v, want the edit to still apply", name)
	}
}

// Regression (prod, 2026-08-11): deleting the track you'd set as your artist
// pick used to deadlock your whole profile. Clients resend the full user
// object on every edit, so the stale pick rode along with an unrelated
// profile-picture change; validation rejected it, the indexer swallowed the
// error, and the user watched their new photo revert on refresh. 1,241
// accounts were stuck this way. The edit must now land and the dangling pick
// must be cleared.
func TestUserUpdate_DeletedArtistPickDoesNotBlockProfileEdit(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedTrack(t, pool, TrackIDOffset+1, UserIDOffset+1)

	// Alice sets her artist pick, then deletes that track.
	mustHandle(t, UserUpdate(), buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1,
		"0xAliceWallet", `{"artist_pick_track_id":2000001}`))
	if _, err := pool.Exec(context.Background(),
		"UPDATE tracks SET is_delete = true WHERE track_id = $1", TrackIDOffset+1); err != nil {
		t.Fatalf("delete track: %v", err)
	}

	// A later profile-picture edit still carries the now-dangling pick.
	mustHandle(t, UserUpdate(), buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1,
		"0xAliceWallet", `{"artist_pick_track_id":2000001,"profile_picture_sizes":"baeaaaiqsenewpic"}`))

	var pfp *string
	if err := pool.QueryRow(context.Background(),
		"SELECT profile_picture_sizes FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&pfp); err != nil {
		t.Fatalf("query profile_picture_sizes: %v", err)
	}
	if pfp == nil || *pfp != "baeaaaiqsenewpic" {
		t.Errorf("profile_picture_sizes = %v, want the new picture to stick", pfp)
	}
	if got := artistPick(t, pool, UserIDOffset+1); got != nil {
		t.Errorf("artist_pick_track_id = %v, want nil (self-healed)", *got)
	}
}

// A chain-supplied null clears the pick. It previously read as "no change",
// so an account could not remove its artist pick at all.
func TestUserUpdate_NullArtistPickClears(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedTrack(t, pool, TrackIDOffset+1, UserIDOffset+1)
	mustHandle(t, UserUpdate(), buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1,
		"0xAliceWallet", `{"artist_pick_track_id":2000001}`))
	if got := artistPick(t, pool, UserIDOffset+1); got == nil || *got != TrackIDOffset+1 {
		t.Fatalf("setup: artist_pick_track_id = %v, want %d", got, TrackIDOffset+1)
	}

	mustHandle(t, UserUpdate(), buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1,
		"0xAliceWallet", `{"artist_pick_track_id":null}`))
	if got := artistPick(t, pool, UserIDOffset+1); got != nil {
		t.Errorf("artist_pick_track_id = %v, want nil after explicit null", *got)
	}
}

// A valid pick must survive an unrelated edit that doesn't mention it.
func TestUserUpdate_PreservesValidArtistPick(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	seedTrack(t, pool, TrackIDOffset+1, UserIDOffset+1)
	mustHandle(t, UserUpdate(), buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1,
		"0xAliceWallet", `{"artist_pick_track_id":2000001}`))

	mustHandle(t, UserUpdate(), buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1,
		"0xAliceWallet", `{"bio":"new bio"}`))
	if got := artistPick(t, pool, UserIDOffset+1); got == nil || *got != TrackIDOffset+1 {
		t.Errorf("artist_pick_track_id = %v, want %d preserved", got, TrackIDOffset+1)
	}
}

func TestUserUpdate_IsDeactivated(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	h := UserUpdate()
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", `{"is_deactivated":true}`)
	mustHandle(t, h, params)

	var isDeactivated bool
	err := pool.QueryRow(context.Background(), "SELECT is_deactivated FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&isDeactivated)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if !isDeactivated {
		t.Error("is_deactivated = false, want true")
	}
}

// Regression: a profile edit (User Update) must persist social links
// (instagram/twitter/tiktok/website/donation). These were silently dropped by
// the writer, so users could not add or change their IG/links on profile.
func TestUserUpdate_SocialLinks(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	h := UserUpdate()
	meta := `{"instagram_handle":"alice_ig","twitter_handle":"alice_tw","tiktok_handle":"alice_tt","website":"https://alice.example","donation":"tips welcome"}`
	params := buildParams(t, pool, EntityTypeUser, ActionUpdate, UserIDOffset+1, UserIDOffset+1, "0xAliceWallet", meta)
	mustHandle(t, h, params)

	var ig, tw, tk, web, don string
	var verifiedIG bool
	err := pool.QueryRow(context.Background(),
		"SELECT instagram_handle, twitter_handle, tiktok_handle, website, donation, verified_with_instagram FROM users WHERE user_id = $1 AND is_current = true",
		UserIDOffset+1).Scan(&ig, &tw, &tk, &web, &don, &verifiedIG)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if ig != "alice_ig" || tw != "alice_tw" || tk != "alice_tt" || web != "https://alice.example" || don != "tips welcome" {
		t.Errorf("social links = ig:%q tw:%q tk:%q web:%q don:%q; want all persisted", ig, tw, tk, web, don)
	}
	// A profile edit must not grant verification.
	if verifiedIG {
		t.Error("verified_with_instagram = true; profile edit must not touch verification flags")
	}
}
