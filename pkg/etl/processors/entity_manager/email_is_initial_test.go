package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

// email_access.is_initial records which key the encrypted_key is wrapped
// against: a shared EMAIL_ENCRYPTION_UUID when true, the email owner's user id
// when false. Clients branch on it to choose a decryption key, so a grant that
// replays without it cannot be decrypted at all -- not merely mislabelled.
// 8,279 of 9,282 grants on the 2026-08-16 snapshot carry true.
func TestEncryptedEmailCreate_ReplaysIsInitial(t *testing.T) {
	pool := setupTestDB(t)
	owner := int64(UserIDOffset + 771)
	seller := int64(UserIDOffset + 772)
	grantee := int64(UserIDOffset + 773)
	seedUser(t, pool, owner, "0xemailowner", "emailowner")
	seedUser(t, pool, seller, "0xemailseller", "emailseller")
	seedUser(t, pool, grantee, "0xemailgrantee", "emailgrantee")

	meta := fmt.Sprintf(`{
		"email_owner_user_id": %d,
		"encrypted_email": "cipher",
		"access_grants": [
			{"receiving_user_id": %d, "grantor_user_id": %d, "encrypted_key": "k-backfilled", "is_initial": true},
			{"receiving_user_id": %d, "grantor_user_id": %d, "encrypted_key": "k-live", "is_initial": false},
			{"receiving_user_id": %d, "grantor_user_id": %d, "encrypted_key": "k-absent"}
		]
	}`, owner, owner, owner, seller, owner, grantee, seller)

	mustHandle(t, EncryptedEmailCreate(),
		buildParams(t, pool, EntityTypeEncryptedEmail, ActionCreate, owner, owner, "0xEmailOwner", meta))

	for _, c := range []struct {
		receiver int64
		grantor  int64
		want     bool
		why      string
	}{
		{owner, owner, true, "explicit true must survive"},
		{seller, owner, false, "explicit false must survive"},
		{grantee, seller, false, "absent flag defaults to the client scheme"},
	} {
		var got bool
		err := pool.QueryRow(context.Background(),
			`SELECT is_initial FROM email_access
			 WHERE email_owner_user_id = $1 AND receiving_user_id = $2 AND grantor_user_id = $3`,
			owner, c.receiver, c.grantor).Scan(&got)
		if err != nil {
			t.Fatalf("query grant receiver=%d: %v", c.receiver, err)
		}
		if got != c.want {
			t.Errorf("receiver=%d is_initial = %v, want %v (%s)", c.receiver, got, c.want, c.why)
		}
	}
}
