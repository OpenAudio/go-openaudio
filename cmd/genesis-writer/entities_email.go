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

type encryptedEmailMetadata struct {
	EncryptedEmail string                   `json:"encrypted_email"`
	AccessGrants   []emailAccessGrantInline `json:"access_grants,omitempty"`
	CreatedAt      string                   `json:"created_at,omitempty"`
}

type emailAccessGrantInline struct {
	ReceivingUserID int64  `json:"receiving_user_id"`
	GrantorUserID   int64  `json:"grantor_user_id"`
	EncryptedKey    string `json:"encrypted_key"`
}

type sourceEncryptedEmail struct {
	EmailOwnerUserID int64
	EncryptedEmail   string
	CreatedAt        time.Time
}

func (w *Writer) writeEncryptedEmails(ctx context.Context) error {
	// Pre-load email access grants keyed by email_owner_user_id.
	accessRows, err := w.srcDB.Query(ctx,
		`SELECT email_owner_user_id, receiving_user_id, grantor_user_id, encrypted_key
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
		if err := accessRows.Scan(&ownerID, &receivingID, &grantorID, &encKey); err != nil {
			return fmt.Errorf("scan email_access: %w", err)
		}
		accessByOwner[ownerID] = append(accessByOwner[ownerID], emailAccessGrantInline{
			ReceivingUserID: receivingID,
			GrantorUserID:   grantorID,
			EncryptedKey:    encKey,
		})
	}
	if err := accessRows.Err(); err != nil {
		return fmt.Errorf("email_access rows: %w", err)
	}
	accessRows.Close()

	return processBatched(ctx, w, "encrypted_emails",
		`SELECT count(*) FROM encrypted_emails`,
		`SELECT email_owner_user_id, encrypted_email, created_at
		FROM encrypted_emails
		ORDER BY email_owner_user_id`,
		func(rows pgx.Rows) (sourceEncryptedEmail, error) {
			var e sourceEncryptedEmail
			err := rows.Scan(&e.EmailOwnerUserID, &e.EncryptedEmail, &e.CreatedAt)
			return e, err
		},
		func(ctx context.Context, e sourceEncryptedEmail) error {
			meta := encryptedEmailMetadata{
				EncryptedEmail: e.EncryptedEmail,
				AccessGrants:   accessByOwner[e.EmailOwnerUserID],
				CreatedAt:      e.CreatedAt.Format(time.RFC3339),
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal encrypted email metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     e.EmailOwnerUserID,
				EntityType: "EncryptedEmail",
				EntityId:   0,
				Action:     "AddEmail",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Email Access ---
// Email access grants that are not tied to an encrypted email (standalone grants).
// Most are pre-loaded with their parent email above. This handles any orphans.

func (w *Writer) writeEmailAccess(ctx context.Context) error {
	type emailAccess struct {
		emailOwnerUserID int64
		receivingUserID  int64
		grantorUserID    int64
		encryptedKey     string
		createdAt        time.Time
	}
	return processBatched(ctx, w, "email_access",
		`SELECT count(*) FROM email_access ea
		WHERE NOT EXISTS (SELECT 1 FROM encrypted_emails ee WHERE ee.email_owner_user_id = ea.email_owner_user_id)`,
		`SELECT ea.email_owner_user_id, ea.receiving_user_id, ea.grantor_user_id, ea.encrypted_key, ea.created_at
		FROM email_access ea
		WHERE NOT EXISTS (SELECT 1 FROM encrypted_emails ee WHERE ee.email_owner_user_id = ea.email_owner_user_id)
		ORDER BY ea.email_owner_user_id, ea.receiving_user_id`,
		func(rows pgx.Rows) (emailAccess, error) {
			var ea emailAccess
			err := rows.Scan(&ea.emailOwnerUserID, &ea.receivingUserID, &ea.grantorUserID, &ea.encryptedKey, &ea.createdAt)
			return ea, err
		},
		func(ctx context.Context, ea emailAccess) error {
			metaJSON, err := json.Marshal(map[string]interface{}{
				"receiving_user_id": ea.receivingUserID,
				"grantor_user_id":   ea.grantorUserID,
				"encrypted_key":     ea.encryptedKey,
				"created_at":        ea.createdAt.Format(time.RFC3339),
			})
			if err != nil {
				return fmt.Errorf("marshal email access metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     ea.emailOwnerUserID,
				EntityType: "EmailAccess",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			})
		},
	)
}
