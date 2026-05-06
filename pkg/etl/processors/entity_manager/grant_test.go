package entity_manager

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

// --- ValidateSigner: grant-based authorization ---
//
// These tests use GrantCreate itself as the host action. After establishing an
// initial grant from user → grantee, we have grantee try to create a *second*
// grant on the user's behalf — which exercises ValidateSigner's grant-fallback
// path against the grant we just stored.

func seedAppFor(t *testing.T, pool *pgxpool.Pool, ownerID int64, ownerSigner, address, name string) {
	t.Helper()
	meta := fmt.Sprintf(`{"address":%q,"name":%q}`, address, name)
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, ownerID, 1, ownerSigner, meta))
}

func TestValidateSigner_AppGrant_Allows(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")
	seedAppFor(t, pool, uid, "0xGrantor", "0xappa", "AppA")
	seedAppFor(t, pool, uid, "0xGrantor", "0xappb", "AppB")

	// Grant AppA permission to act for the user.
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", `{"grantee_address":"0xappa"}`))
	// AppA, on the user's behalf, creates a grant for AppB.
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 2, "0xAppA", `{"grantee_address":"0xappb"}`))

	var exists bool
	if err := pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM grants WHERE grantee_address = '0xappb' AND user_id = $1 AND is_current = true AND is_revoked = false)",
		uid).Scan(&exists); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Fatal("expected grant for 0xappb to be created via app-grant signer")
	}
}

func TestValidateSigner_UserGrant_Unapproved_Rejects(t *testing.T) {
	pool := setupTestDB(t)
	grantor := int64(UserIDOffset + 1)
	grantee := int64(UserIDOffset + 2)
	seedUser(t, pool, grantor, "0xgrantor", "grantor")
	seedUser(t, pool, grantee, "0xgrantee", "grantee")
	seedAppFor(t, pool, grantor, "0xGrantor", "0xtargetapp", "TargetApp")

	// User-to-user grant created but not yet approved by the grantee.
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, grantor, 1, "0xGrantor", `{"grantee_address":"0xgrantee"}`))

	// Grantee attempts to use the unapproved grant.
	mustReject(t, GrantCreate(),
		buildParams(t, pool, EntityTypeGrant, ActionCreate, grantor, 2, "0xGrantee", `{"grantee_address":"0xtargetapp"}`),
		"not approved")
}

func TestValidateSigner_UserGrant_Approved_Allows(t *testing.T) {
	pool := setupTestDB(t)
	grantor := int64(UserIDOffset + 1)
	grantee := int64(UserIDOffset + 2)
	seedUser(t, pool, grantor, "0xgrantor", "grantor")
	seedUser(t, pool, grantee, "0xgrantee", "grantee")
	seedAppFor(t, pool, grantor, "0xGrantor", "0xtargetapp", "TargetApp")

	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, grantor, 1, "0xGrantor", `{"grantee_address":"0xgrantee"}`))
	approveMeta := fmt.Sprintf(`{"grantee_address":"0xgrantee","grantor_user_id":%d}`, grantor)
	mustHandle(t, GrantApprove(), buildParams(t, pool, EntityTypeGrant, ActionApprove, grantee, 1, "0xGrantee", approveMeta))

	// Approved grantee can now act on behalf of grantor.
	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, grantor, 2, "0xGrantee", `{"grantee_address":"0xtargetapp"}`))
}

func TestValidateSigner_RevokedGrant_Rejects(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")
	seedAppFor(t, pool, uid, "0xGrantor", "0xrevapp", "RevApp")
	seedAppFor(t, pool, uid, "0xGrantor", "0xtargetapp", "TargetApp")

	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xGrantor", `{"grantee_address":"0xrevapp"}`))
	mustHandle(t, GrantDelete(), buildParams(t, pool, EntityTypeGrant, ActionDelete, uid, 1, "0xGrantor", `{"grantee_address":"0xrevapp"}`))

	mustReject(t, GrantCreate(),
		buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 2, "0xRevApp", `{"grantee_address":"0xtargetapp"}`),
		"revoked")
}

func TestValidateSigner_NoGrant_Rejects(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xgrantor", "grantor")
	seedAppFor(t, pool, uid, "0xGrantor", "0xtargetapp", "TargetApp")

	mustReject(t, GrantCreate(),
		buildParams(t, pool, EntityTypeGrant, ActionCreate, uid, 1, "0xstranger", `{"grantee_address":"0xtargetapp"}`),
		"not authorized")
}

func TestValidateSigner_GranteeDeactivated_Rejects(t *testing.T) {
	pool := setupTestDB(t)
	grantor := int64(UserIDOffset + 1)
	grantee := int64(UserIDOffset + 2)
	seedUser(t, pool, grantor, "0xgrantor", "grantor")
	seedUser(t, pool, grantee, "0xgrantee", "grantee")
	seedAppFor(t, pool, grantor, "0xGrantor", "0xtargetapp", "TargetApp")

	mustHandle(t, GrantCreate(), buildParams(t, pool, EntityTypeGrant, ActionCreate, grantor, 1, "0xGrantor", `{"grantee_address":"0xgrantee"}`))
	approveMeta := fmt.Sprintf(`{"grantee_address":"0xgrantee","grantor_user_id":%d}`, grantor)
	mustHandle(t, GrantApprove(), buildParams(t, pool, EntityTypeGrant, ActionApprove, grantee, 1, "0xGrantee", approveMeta))

	if _, err := pool.Exec(context.Background(),
		"UPDATE users SET is_deactivated = true WHERE user_id = $1 AND is_current = true", grantee); err != nil {
		t.Fatalf("deactivate grantee: %v", err)
	}

	mustReject(t, GrantCreate(),
		buildParams(t, pool, EntityTypeGrant, ActionCreate, grantor, 2, "0xGrantee", `{"grantee_address":"0xtargetapp"}`),
		"no longer a valid")
}
