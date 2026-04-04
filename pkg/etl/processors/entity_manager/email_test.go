package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

func TestEncryptedEmailCreate_TxType(t *testing.T) {
	h := EncryptedEmailCreate()
	if h.EntityType() != EntityTypeEncryptedEmail {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeEncryptedEmail)
	}
	if h.Action() != ActionAddEmail {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionAddEmail)
	}
}

func TestEncryptedEmailCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xemailowner", "emailowner")

	meta := `{
		"email_owner_user_id": ` + fmt.Sprintf("%d", uid) + `,
		"encrypted_email": "encrypted-data-here",
		"access_grants": [
			{"receiving_user_id": ` + fmt.Sprintf("%d", uid) + `, "grantor_user_id": ` + fmt.Sprintf("%d", uid) + `, "encrypted_key": "key1"}
		]
	}`
	params := buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner", meta)
	mustHandle(t, EncryptedEmailCreate(), params)

	var encEmail string
	err := pool.QueryRow(context.Background(),
		"SELECT encrypted_email FROM encrypted_emails WHERE email_owner_user_id = $1", uid).Scan(&encEmail)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if encEmail != "encrypted-data-here" {
		t.Errorf("encrypted_email = %q", encEmail)
	}

	var count int
	err = pool.QueryRow(context.Background(),
		"SELECT count(*) FROM email_access WHERE email_owner_user_id = $1", uid).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 access record, got %d", count)
	}
}

func TestEncryptedEmailCreate_SkipsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xemailowner", "emailowner")

	meta := `{
		"email_owner_user_id": ` + fmt.Sprintf("%d", uid) + `,
		"encrypted_email": "encrypted-data-here",
		"access_grants": []
	}`
	params := buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner", meta)
	mustHandle(t, EncryptedEmailCreate(), params)
	// Second call should silently skip
	mustHandle(t, EncryptedEmailCreate(), params)
}

func TestEmailAccessUpdate_TxType(t *testing.T) {
	h := EmailAccessUpdate()
	if h.EntityType() != EntityTypeEmailAccess {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeEmailAccess)
	}
	if h.Action() != ActionUpdate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionUpdate)
	}
}

func TestEmailAccessUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xemailowner", "emailowner")
	seedUser(t, pool, uid2, "0xreceiver", "receiver")

	// First create the email with an initial grant to uid
	createMeta := `{
		"email_owner_user_id": ` + fmt.Sprintf("%d", uid) + `,
		"encrypted_email": "encrypted-data",
		"access_grants": [
			{"receiving_user_id": ` + fmt.Sprintf("%d", uid) + `, "grantor_user_id": ` + fmt.Sprintf("%d", uid) + `, "encrypted_key": "key1"}
		]
	}`
	mustHandle(t, EncryptedEmailCreate(), buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner", createMeta))

	// Now grant access to uid2 via uid (grantor)
	grantMeta := `{
		"email_owner_user_id": ` + fmt.Sprintf("%d", uid) + `,
		"access_grants": [
			{"receiving_user_id": ` + fmt.Sprintf("%d", uid2) + `, "grantor_user_id": ` + fmt.Sprintf("%d", uid) + `, "encrypted_key": "key2"}
		]
	}`
	mustHandle(t, EmailAccessUpdate(), buildParams(t, pool, EntityTypeEmailAccess, ActionUpdate, uid, uid, "0xEmailowner", grantMeta))

	var count int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM email_access WHERE email_owner_user_id = $1", uid).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 access records, got %d", count)
	}
}

func TestEncryptedEmailCreate_RejectsMissingFields(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xemailowner", "emailowner")

	mustReject(t, EncryptedEmailCreate(), buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner",
		`{"encrypted_email":"x","access_grants":[]}`), "missing required field: email_owner_user_id")
	mustReject(t, EncryptedEmailCreate(), buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner",
		fmt.Sprintf(`{"email_owner_user_id":%d,"access_grants":[]}`, uid)), "missing required field: encrypted_email")
	mustReject(t, EncryptedEmailCreate(), buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner",
		fmt.Sprintf(`{"email_owner_user_id":%d,"encrypted_email":"x"}`, uid)), "missing required field: access_grants")
}

func TestEncryptedEmailCreate_RejectsMalformedGrant(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xemailowner", "emailowner")

	// Grant missing encrypted_key
	meta := fmt.Sprintf(`{"email_owner_user_id":%d,"encrypted_email":"x","access_grants":[{"receiving_user_id":%d,"grantor_user_id":%d}]}`, uid, uid, uid)
	mustReject(t, EncryptedEmailCreate(), buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner", meta), "encrypted_key")
}

func TestEmailAccessUpdate_RejectsUnauthorizedGrantor(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	uid3 := int64(UserIDOffset + 3)
	seedUser(t, pool, uid, "0xemailowner", "emailowner")
	seedUser(t, pool, uid2, "0xreceiver", "receiver")
	seedUser(t, pool, uid3, "0xbadgrantor", "badgrantor")

	// Create email with grant only to uid
	createMeta := `{
		"email_owner_user_id": ` + fmt.Sprintf("%d", uid) + `,
		"encrypted_email": "encrypted-data",
		"access_grants": [
			{"receiving_user_id": ` + fmt.Sprintf("%d", uid) + `, "grantor_user_id": ` + fmt.Sprintf("%d", uid) + `, "encrypted_key": "key1"}
		]
	}`
	mustHandle(t, EncryptedEmailCreate(), buildParams(t, pool, EntityTypeEncryptedEmail, ActionAddEmail, uid, uid, "0xEmailowner", createMeta))

	// uid3 tries to grant but doesn't have access
	grantMeta := `{
		"email_owner_user_id": ` + fmt.Sprintf("%d", uid) + `,
		"access_grants": [
			{"receiving_user_id": ` + fmt.Sprintf("%d", uid2) + `, "grantor_user_id": ` + fmt.Sprintf("%d", uid3) + `, "encrypted_key": "key2"}
		]
	}`
	mustReject(t, EmailAccessUpdate(), buildParams(t, pool, EntityTypeEmailAccess, ActionUpdate, uid3, uid, "0xBadgrantor", grantMeta), "does not have access")
}
