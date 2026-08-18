package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// --- Developer Apps ---

type developerAppMetadata struct {
	// The indexer reads the app address from metadata, not from the signer.
	Address          string `json:"address"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	ImageURL         string `json:"image_url,omitempty"`
	IsPersonalAccess bool   `json:"is_personal_access,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	// Always serialized: `omitempty` would drop a false value and the indexer
	// cannot tell "absent" from "not deleted".
	IsDelete bool `json:"is_delete"`
}

type sourceDeveloperApp struct {
	Address          string
	UserID           int64
	Name             *string
	Description      *string
	ImageURL         *string
	IsPersonalAccess bool
	OwnerWallet      string
	CreatedAt        time.Time
	IsDelete         bool
}

func (w *Writer) writeDeveloperApps(ctx context.Context) error {
	return processBatched(ctx, w, "developer_apps",
		`SELECT count(*) FROM developer_apps WHERE is_current = true`,
		`SELECT d.address, d.user_id, COALESCE(LOWER(u.wallet), ''),
			d.name, d.description, d.image_url, d.is_personal_access, d.created_at, d.is_delete
		FROM developer_apps d
		JOIN users u ON u.user_id = d.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE d.is_current = true
		ORDER BY d.user_id, d.address`,
		func(rows pgx.Rows) (sourceDeveloperApp, error) {
			var d sourceDeveloperApp
			err := rows.Scan(&d.Address, &d.UserID, &d.OwnerWallet, &d.Name, &d.Description, &d.ImageURL, &d.IsPersonalAccess, &d.CreatedAt, &d.IsDelete)
			return d, err
		},
		func(ctx context.Context, d sourceDeveloperApp) error {
			meta := developerAppMetadata{
				IsDelete:         d.IsDelete,
				Address:          d.Address,
				Name:             deref(d.Name),
				Description:      deref(d.Description),
				ImageURL:         deref(d.ImageURL),
				IsPersonalAccess: d.IsPersonalAccess,
				CreatedAt:        d.CreatedAt.UTC().Format(time.RFC3339),
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal developer app %s metadata: %w", d.Address, err)
			}
			// Signed by the owning user, not the app address: ValidateSigner requires
			// the user's wallet, and no grant exists to stand in for it during replay.
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     d.UserID,
				EntityType: "DeveloperApp",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, d.OwnerWallet)
		},
	)
}

// --- Grants ---

type grantMetadata struct {
	GranteeAddress string `json:"grantee_address"`
	CreatedAt      string `json:"created_at,omitempty"`
	// Always serialized, as above.
	IsRevoked bool `json:"is_revoked"`
	// is_approved is nullable and three-valued, so it travels as a pointer:
	// `omitempty` drops only the nil case, and the indexer reads an absent key
	// as "derive it the way a production create would". A non-nil false is still
	// serialized, which is what makes a rejected grant replayable.
	IsApproved *bool `json:"is_approved,omitempty"`
}

type sourceGrant struct {
	GranteeAddress string
	UserID         int64
	CreatedAt      time.Time
	GrantorWallet  string
	IsRevoked      bool
	IsApproved     *bool
}

func (w *Writer) writeGrants(ctx context.Context) error {
	return processBatched(ctx, w, "grants",
		`SELECT count(*) FROM (
			SELECT DISTINCT ON (g.user_id, lower(g.grantee_address)) 1
			FROM grants g WHERE g.is_current = true
			ORDER BY g.user_id, lower(g.grantee_address), g.created_at DESC) d`,
		// DISTINCT ON, not just an ORDER BY. A grant that was granted, revoked,
		// then re-granted leaves TWO is_current rows for one
		// (user_id, grantee_address) -- one revoked, one not. Emitting both puts
		// two Grant/Create transactions in flight for the same authorization,
		// and the auth projection takes the first and declines the rest, so
		// whichever one lands first decides whether the manager keeps access.
		//
		// Ordering alone cannot fix that: processBatched emits each batch from
		// NumCPU goroutines, so emission order is a race no query can constrain.
		// Collapsing to the newest row per pair removes the dependency instead
		// of trying to sequence it -- and newest-created is the current
		// authorization whether the latest action was a grant or a revoke.
		//
		// On the 2026-08-16 snapshot this was 2 of 4,330 current grants. It
		// landed correctly by luck; the other outcome silently revokes them.
		`SELECT DISTINCT ON (g.user_id, lower(g.grantee_address))
			g.grantee_address, g.user_id, COALESCE(LOWER(u.wallet), ''), g.created_at, g.is_revoked, g.is_approved
		FROM grants g
		JOIN users u ON u.user_id = g.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE g.is_current = true
		ORDER BY g.user_id, lower(g.grantee_address), g.created_at DESC`,
		func(rows pgx.Rows) (sourceGrant, error) {
			var g sourceGrant
			err := rows.Scan(&g.GranteeAddress, &g.UserID, &g.GrantorWallet, &g.CreatedAt, &g.IsRevoked, &g.IsApproved)
			return g, err
		},
		func(ctx context.Context, g sourceGrant) error {
			// Approval rides on the Create for the same reason is_revoked does:
			// the source row is already the final state. It cannot be replayed as
			// a follow-up Grant/Approve transaction -- that action forces
			// is_revoked back to false, so the 35 grants that are both approved
			// and revoked would be unreproducible, and it requires the grantee's
			// own wallet as signer, which a manager grant to a deactivated or
			// missing account cannot supply. Without this, the 493 approved
			// user-to-user manager grants replay as NULL and every one of those
			// managers loses the ability to act for the user they manage
			// (ValidateSigner requires is_approved for a non-app grantee).
			metaJSON, err := json.Marshal(grantMetadata{
				IsRevoked:      g.IsRevoked,
				IsApproved:     g.IsApproved,
				GranteeAddress: g.GranteeAddress,
				CreatedAt:      g.CreatedAt.UTC().Format(time.RFC3339),
			})
			if err != nil {
				return fmt.Errorf("marshal grant metadata: %w", err)
			}
			// Signed by the granting user: a grant cannot authorize its own creation,
			// so the grantee address cannot be the signer here.
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     g.UserID,
				EntityType: "Grant",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, g.GrantorWallet)
		},
	)
}
