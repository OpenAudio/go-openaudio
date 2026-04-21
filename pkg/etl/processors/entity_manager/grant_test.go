package entity_manager

import (
	"context"
	"testing"
)

func TestGrantCreate_TxType(t *testing.T) {
	h := GrantCreate()
	if h.EntityType() != EntityTypeGrant {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeGrant)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestGrantCreate_Success_AppGrant(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")

	// Create a developer app first
	appMeta := `{"address":"0xgrantapp","name":"App"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xGrantor", appMeta))

	// Create a grant to the app
	grantMeta := `{"grantee_address":"0xgrantapp"}`
	params := buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", grantMeta)
	mustHandle(t, GrantCreate(), params)

	var isRevoked bool
	var isApproved *bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_revoked, is_approved FROM grants WHERE grantee_address = '0xgrantapp' AND user_id = $1 AND is_current = true",
		uid).Scan(&isRevoked, &isApproved)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isRevoked {
		t.Error("expected is_revoked = false")
	}
	if isApproved == nil || !*isApproved {
		t.Error("expected is_approved = true for app grant")
	}
}

func TestGrantCreate_Success_UserGrant(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xgrantor", "grantor")
	seedUser(t, pool, uid2, "0xgrantee", "grantee")

	grantMeta := `{"grantee_address":"0xgrantee"}`
	params := buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", grantMeta)
	mustHandle(t, GrantCreate(), params)

	var isApproved *bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_approved FROM grants WHERE grantee_address = '0xgrantee' AND user_id = $1 AND is_current = true",
		uid).Scan(&isApproved)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isApproved != nil {
		t.Error("expected is_approved = nil for user grant")
	}
}

func TestGrantCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")

	appMeta := `{"address":"0xdupgrantapp","name":"App"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xGrantor", appMeta))

	grantMeta := `{"grantee_address":"0xdupgrantapp"}`
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", grantMeta))
	mustReject(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", grantMeta), "already exists")
}

func TestGrantCreate_RejectsUnknownGrantee(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")

	grantMeta := `{"grantee_address":"0xnobody"}`
	mustReject(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", grantMeta), "not a developer app or user wallet")
}

func TestGrantDelete_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")

	appMeta := `{"address":"0xrevokeapp","name":"App"}`
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xGrantor", appMeta))

	grantMeta := `{"grantee_address":"0xrevokeapp"}`
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", grantMeta))
	mustHandle(t, GrantDelete(), buildParams(t, pool, EntityTypeGrant, ActionDelete, uid, 1, "0xGrantor", grantMeta))

	var isRevoked bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_revoked FROM grants WHERE grantee_address = '0xrevokeapp' AND user_id = $1 AND is_current = true",
		uid).Scan(&isRevoked)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isRevoked {
		t.Error("expected is_revoked = true")
	}
}

func TestGrantDelete_RejectsNoActiveGrant(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")

	grantMeta := `{"grantee_address":"0xnoone"}`
	mustReject(t, GrantDelete(), buildParams(t, pool, EntityTypeGrant, ActionDelete, uid, 1, "0xGrantor", grantMeta), "no active grant")
}

func TestGrantApprove_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xgrantor", "grantor")
	seedUser(t, pool, uid2, "0xgrantee2", "grantee2")

	// Create user-to-user grant (is_approved starts as nil)
	grantMeta := `{"grantee_address":"0xgrantee2"}`
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid1, 1, "0xGrantor", grantMeta))

	// Approve it
	approveMeta := `{"grantee_address":"0xgrantee2","grantor_user_id":3000001}`
	mustHandle(t, GrantApprove(), buildParams(t, pool, EntityTypeGrant, ActionApprove, uid2, 1, "0xGrantee2", approveMeta))

	var isApproved *bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_approved FROM grants WHERE grantee_address = '0xgrantee2' AND user_id = $1 AND is_current = true",
		uid1).Scan(&isApproved)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isApproved == nil || !*isApproved {
		t.Error("expected is_approved = true")
	}
}

func TestGrantReject_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xgrantor", "grantor")
	seedUser(t, pool, uid2, "0xgrantee3", "grantee3")

	grantMeta := `{"grantee_address":"0xgrantee3"}`
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid1, 1, "0xGrantor", grantMeta))

	rejectMeta := `{"grantee_address":"0xgrantee3","grantor_user_id":3000001}`
	mustHandle(t, GrantReject(), buildParams(t, pool, EntityTypeGrant, ActionReject, uid2, 1, "0xGrantee3", rejectMeta))

	var isRevoked bool
	var isApproved *bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_revoked, is_approved FROM grants WHERE grantee_address = '0xgrantee3' AND user_id = $1 AND is_current = true",
		uid1).Scan(&isRevoked, &isApproved)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isRevoked {
		t.Error("expected is_revoked = true")
	}
	if isApproved == nil || *isApproved {
		t.Error("expected is_approved = false")
	}
}
