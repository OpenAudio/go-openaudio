package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// --- Encrypted Emails ---

// encryptedEmailMetadata is the metadata encryptedEmailHandler reads. Every
// field below is required by that handler: it takes the owner from
// email_owner_user_id rather than from the transaction's user_id, and it rejects
// the transaction outright when access_grants is absent. access_grants is
// therefore serialized even when empty — `omitempty` would drop it and the
// handler cannot tell "absent" from "no grants".
type encryptedEmailMetadata struct {
	EmailOwnerUserID int64                    `json:"email_owner_user_id"`
	EncryptedEmail   string                   `json:"encrypted_email"`
	AccessGrants     []emailAccessGrantInline `json:"access_grants"`
	CreatedAt        string                   `json:"created_at,omitempty"`
}

type emailAccessGrantInline struct {
	ReceivingUserID int64  `json:"receiving_user_id"`
	GrantorUserID   int64  `json:"grantor_user_id"`
	EncryptedKey    string `json:"encrypted_key"`
	// Which key the encrypted_key is wrapped against. Clients branch on it to
	// decrypt, so a grant that replays without it cannot be opened at all.
	IsInitial bool `json:"is_initial,omitempty"`
}

type sourceEncryptedEmail struct {
	EmailOwnerUserID int64
	OwnerWallet      string
	EncryptedEmail   string
	CreatedAt        time.Time
}

func (w *Writer) writeEncryptedEmails(ctx context.Context) error {
	// Pre-load email access grants keyed by email_owner_user_id.
	accessRows, err := w.srcDB.Query(ctx,
		`SELECT email_owner_user_id, receiving_user_id, grantor_user_id, encrypted_key, is_initial
		FROM email_access
		ORDER BY email_owner_user_id`)
	if err != nil {
		return fmt.Errorf("query email_access: %w", err)
	}
	defer accessRows.Close()

	accessByOwner := make(map[int64][]emailAccessGrantInline)
	for accessRows.Next() {
		var ownerID, receivingID, grantorID int64
		var encKey string
		var isInitial bool
		if err := accessRows.Scan(&ownerID, &receivingID, &grantorID, &encKey, &isInitial); err != nil {
			return fmt.Errorf("scan email_access: %w", err)
		}
		accessByOwner[ownerID] = append(accessByOwner[ownerID], emailAccessGrantInline{
			ReceivingUserID: receivingID,
			GrantorUserID:   grantorID,
			EncryptedKey:    encKey,
			IsInitial:       isInitial,
		})
	}
	if err := accessRows.Err(); err != nil {
		return fmt.Errorf("email_access rows: %w", err)
	}
	accessRows.Close()

	// The join to users supplies the owner's wallet as the signer, matching the
	// other entity steps. An email whose owner is not a current user with a
	// wallet is skipped rather than emitted unsigned; on a production clone every
	// row qualifies.
	return processBatched(ctx, w, "encrypted_emails",
		`SELECT count(*) FROM encrypted_emails ee
		JOIN users u ON u.user_id = ee.email_owner_user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''`,
		`SELECT ee.email_owner_user_id, LOWER(u.wallet), ee.encrypted_email, ee.created_at
		FROM encrypted_emails ee
		JOIN users u ON u.user_id = ee.email_owner_user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		ORDER BY ee.email_owner_user_id`,
		func(rows pgx.Rows) (sourceEncryptedEmail, error) {
			var e sourceEncryptedEmail
			err := rows.Scan(&e.EmailOwnerUserID, &e.OwnerWallet, &e.EncryptedEmail, &e.CreatedAt)
			return e, err
		},
		func(ctx context.Context, e sourceEncryptedEmail) error {
			grants := accessByOwner[e.EmailOwnerUserID]
			if grants == nil {
				grants = []emailAccessGrantInline{}
			}
			meta := encryptedEmailMetadata{
				EmailOwnerUserID: e.EmailOwnerUserID,
				EncryptedEmail:   e.EncryptedEmail,
				AccessGrants:     grants,
				CreatedAt:        e.CreatedAt.UTC().Format(time.RFC3339),
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal encrypted email metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     e.EmailOwnerUserID,
				EntityType: "EncryptedEmail",
				EntityId:   0,
				Action:     "AddEmail",
				Metadata:   string(metaJSON),
			}, e.OwnerWallet)
		},
	)
}

// --- Email Access ---
// Email access grants that are not tied to an encrypted email (standalone grants).
// Most are pre-loaded with their parent email above. This handles any orphans.
//
// emailAccessHandler is registered for EmailAccess/Update and reads the same
// {email_owner_user_id, access_grants} shape as the create path, so that is what
// this step emits. It additionally requires the grantor to already hold access
// to the email, which an orphan's first grant cannot satisfy — there is no
// parent row to have granted from. The production snapshot contains no orphan
// grants at all (every email_access row has an encrypted_emails parent), so this
// step emits nothing there; correcting the shape means that if orphans ever do
// appear they reach the handler and are accepted or rejected on their merits
// instead of being dropped by a routing mismatch.

type emailAccessMetadata struct {
	EmailOwnerUserID int64                    `json:"email_owner_user_id"`
	AccessGrants     []emailAccessGrantInline `json:"access_grants"`
	CreatedAt        string                   `json:"created_at,omitempty"`
}

func (w *Writer) writeEmailAccess(ctx context.Context) error {
	type emailAccess struct {
		emailOwnerUserID int64
		ownerWallet      string
		receivingUserID  int64
		grantorUserID    int64
		encryptedKey     string
		createdAt        time.Time
	}
	return processBatched(ctx, w, "email_access",
		`SELECT count(*) FROM email_access ea
		JOIN users u ON u.user_id = ea.email_owner_user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE NOT EXISTS (SELECT 1 FROM encrypted_emails ee WHERE ee.email_owner_user_id = ea.email_owner_user_id)`,
		`SELECT ea.email_owner_user_id, LOWER(u.wallet), ea.receiving_user_id, ea.grantor_user_id, ea.encrypted_key, ea.created_at
		FROM email_access ea
		JOIN users u ON u.user_id = ea.email_owner_user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE NOT EXISTS (SELECT 1 FROM encrypted_emails ee WHERE ee.email_owner_user_id = ea.email_owner_user_id)
		ORDER BY ea.email_owner_user_id, ea.receiving_user_id`,
		func(rows pgx.Rows) (emailAccess, error) {
			var ea emailAccess
			err := rows.Scan(&ea.emailOwnerUserID, &ea.ownerWallet, &ea.receivingUserID, &ea.grantorUserID, &ea.encryptedKey, &ea.createdAt)
			return ea, err
		},
		func(ctx context.Context, ea emailAccess) error {
			metaJSON, err := json.Marshal(emailAccessMetadata{
				EmailOwnerUserID: ea.emailOwnerUserID,
				AccessGrants: []emailAccessGrantInline{{
					ReceivingUserID: ea.receivingUserID,
					GrantorUserID:   ea.grantorUserID,
					EncryptedKey:    ea.encryptedKey,
				}},
				CreatedAt: ea.createdAt.UTC().Format(time.RFC3339),
			})
			if err != nil {
				return fmt.Errorf("marshal email access metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     ea.emailOwnerUserID,
				EntityType: "EmailAccess",
				EntityId:   0,
				Action:     "Update",
				Metadata:   string(metaJSON),
			}, ea.ownerWallet)
		},
	)
}
