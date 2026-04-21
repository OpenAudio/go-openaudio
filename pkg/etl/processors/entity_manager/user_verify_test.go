package entity_manager

import (
	"context"
	"strings"
	"testing"
)

func TestUserVerify_TxType(t *testing.T) {
	h := UserVerify()
	if h.EntityType() != EntityTypeUser {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeUser)
	}
	if h.Action() != ActionVerify {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionVerify)
	}
}

func TestUserVerify_StatelessValidation(t *testing.T) {
	tests := []struct {
		name       string
		entityType string
		action     string
		wantErr    string
	}{
		{
			name:       "wrong entity type",
			entityType: EntityTypeTrack,
			action:     ActionVerify,
			wantErr:    "wrong entity type",
		},
		{
			name:       "wrong action",
			entityType: EntityTypeUser,
			action:     ActionUpdate,
			wantErr:    "wrong action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &Params{
				UserID:     UserIDOffset + 1,
				EntityID:   UserIDOffset + 1,
				EntityType: tt.entityType,
				Action:     tt.action,
				Signer:     "0xverified",
			}
			err := validateUserVerify(context.Background(), params)
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

func TestUserVerify_Success(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	h := UserVerify()
	params := buildParams(t, pool, EntityTypeUser, ActionVerify, UserIDOffset+1, UserIDOffset+1, "0xVerifiedAddr", `{"is_verified":true,"twitter_handle":"alice_twitter"}`)
	VerifiedAddress = "" // Accept any signer for test
	mustHandle(t, h, params)
	VerifiedAddress = ""

	var isVerified bool
	err := pool.QueryRow(context.Background(), "SELECT is_verified FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&isVerified)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if !isVerified {
		t.Error("is_verified = false, want true")
	}
}

func TestUserVerify_RejectsUserNotFound(t *testing.T) {
	pool := setupTestDB(t)
	params := buildParams(t, pool, EntityTypeUser, ActionVerify, UserIDOffset+1, UserIDOffset+1, "0xVerified", `{"is_verified":true}`)
	mustReject(t, UserVerify(), params, "does not exist")
}

func TestUserVerify_RejectsWrongSignerWhenConfigured(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	VerifiedAddress = "0xVerifiedAddressOnly"
	params := buildParams(t, pool, EntityTypeUser, ActionVerify, UserIDOffset+1, UserIDOffset+1, "0xWrongSigner", `{"is_verified":true}`)
	mustReject(t, UserVerify(), params, "verified address")
	VerifiedAddress = ""
}

func TestUserVerify_Metadata(t *testing.T) {
	pool := setupTestDB(t)
	seedUser(t, pool, UserIDOffset+1, "0xalicewallet", "alice")
	VerifiedAddress = ""
	h := UserVerify()
	params := buildParams(t, pool, EntityTypeUser, ActionVerify, UserIDOffset+1, UserIDOffset+1, "0xAny", `{"is_verified":true,"instagram_handle":"alice_insta","tiktok_handle":"alice_tiktok"}`)
	mustHandle(t, h, params)

	var instagramHandle, tiktokHandle string
	err := pool.QueryRow(context.Background(), "SELECT COALESCE(instagram_handle,''), COALESCE(tiktok_handle,'') FROM users WHERE user_id = $1 AND is_current = true", UserIDOffset+1).Scan(&instagramHandle, &tiktokHandle)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if instagramHandle != "alice_insta" {
		t.Errorf("instagram_handle = %q, want alice_insta", instagramHandle)
	}
	if tiktokHandle != "alice_tiktok" {
		t.Errorf("tiktok_handle = %q, want alice_tiktok", tiktokHandle)
	}
}
