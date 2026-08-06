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
}

func (w *Writer) writeDeveloperApps(ctx context.Context) error {
	return processBatched(ctx, w, "developer_apps",
		`SELECT count(*) FROM developer_apps WHERE is_current = true AND is_delete = false`,
		`SELECT d.address, d.user_id, COALESCE(LOWER(u.wallet), ''),
			d.name, d.description, d.image_url, d.is_personal_access, d.created_at
		FROM developer_apps d
		JOIN users u ON u.user_id = d.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE d.is_current = true AND d.is_delete = false
		ORDER BY d.user_id, d.address`,
		func(rows pgx.Rows) (sourceDeveloperApp, error) {
			var d sourceDeveloperApp
			err := rows.Scan(&d.Address, &d.UserID, &d.OwnerWallet, &d.Name, &d.Description, &d.ImageURL, &d.IsPersonalAccess, &d.CreatedAt)
			return d, err
		},
		func(ctx context.Context, d sourceDeveloperApp) error {
			meta := developerAppMetadata{
				Address:          d.Address,
				Name:             deref(d.Name),
				Description:      deref(d.Description),
				ImageURL:         deref(d.ImageURL),
				IsPersonalAccess: d.IsPersonalAccess,
				CreatedAt:        d.CreatedAt.Format(time.RFC3339),
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
}

type sourceGrant struct {
	GranteeAddress string
	UserID         int64
	CreatedAt      time.Time
	GrantorWallet  string
}

func (w *Writer) writeGrants(ctx context.Context) error {
	return processBatched(ctx, w, "grants",
		`SELECT count(*) FROM grants WHERE is_current = true AND is_revoked = false AND is_approved = true`,
		`SELECT g.grantee_address, g.user_id, COALESCE(LOWER(u.wallet), ''), g.created_at
		FROM grants g
		JOIN users u ON u.user_id = g.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE g.is_current = true AND g.is_revoked = false AND g.is_approved = true
		ORDER BY g.user_id, g.grantee_address`,
		func(rows pgx.Rows) (sourceGrant, error) {
			var g sourceGrant
			err := rows.Scan(&g.GranteeAddress, &g.UserID, &g.GrantorWallet, &g.CreatedAt)
			return g, err
		},
		func(ctx context.Context, g sourceGrant) error {
			metaJSON, err := json.Marshal(grantMetadata{
				GranteeAddress: g.GranteeAddress,
				CreatedAt:      g.CreatedAt.Format(time.RFC3339),
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
