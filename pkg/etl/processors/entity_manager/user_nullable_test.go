package entity_manager

import (
	"context"
	"database/sql"
	"testing"
)

func TestUserCreate_NullableFieldsStayNull(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 99)
	meta := `{"handle":"nulltest"}`
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, uid, uid, "0xNullTestWallet", meta)
	mustHandle(t, UserCreate(), params)

	var name, bio, location, profilePicture, coverPhoto sql.NullString
	err := pool.QueryRow(context.Background(),
		`SELECT name, bio, location, profile_picture, cover_photo
		 FROM users WHERE user_id = $1 AND is_current = true`, uid).Scan(
		&name, &bio, &location, &profilePicture, &coverPhoto,
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if name.Valid {
		t.Errorf("name should be NULL, got %q", name.String)
	}
	if bio.Valid {
		t.Errorf("bio should be NULL, got %q", bio.String)
	}
	if location.Valid {
		t.Errorf("location should be NULL, got %q", location.String)
	}
	if profilePicture.Valid {
		t.Errorf("profile_picture should be NULL, got %q", profilePicture.String)
	}
	if coverPhoto.Valid {
		t.Errorf("cover_photo should be NULL, got %q", coverPhoto.String)
	}
}

func TestUserUpdate_PreservesNulls(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 100)
	// Create user with no optional fields
	createMeta := `{"handle":"nullpreserve"}`
	params := buildParams(t, pool, EntityTypeUser, ActionCreate, uid, uid, "0xNullPreserve", createMeta)
	mustHandle(t, UserCreate(), params)

	// Update only the name, other fields should stay null
	updateMeta := `{"name":"Updated Name"}`
	params = buildParams(t, pool, EntityTypeUser, ActionUpdate, uid, uid, "0xNullPreserve", updateMeta)
	mustHandle(t, UserUpdate(), params)

	var name, bio, location sql.NullString
	err := pool.QueryRow(context.Background(),
		`SELECT name, bio, location FROM users WHERE user_id = $1 AND is_current = true`, uid).Scan(
		&name, &bio, &location,
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !name.Valid || name.String != "Updated Name" {
		t.Errorf("name = %v, want 'Updated Name'", name)
	}
	if bio.Valid {
		t.Errorf("bio should still be NULL, got %q", bio.String)
	}
	if location.Valid {
		t.Errorf("location should still be NULL, got %q", location.String)
	}
}
