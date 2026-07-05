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
	CreatedAt        time.Time
}

func (w *Writer) writeDeveloperApps(ctx context.Context) error {
	return processBatched(ctx, w, "developer_apps",
		`SELECT count(*) FROM developer_apps WHERE is_current = true AND is_delete = false`,
		`SELECT address, user_id, name, description, image_url, is_personal_access, created_at
		FROM developer_apps
		WHERE is_current = true AND is_delete = false
		ORDER BY user_id, address`,
		func(rows pgx.Rows) (sourceDeveloperApp, error) {
			var d sourceDeveloperApp
			err := rows.Scan(&d.Address, &d.UserID, &d.Name, &d.Description, &d.ImageURL, &d.IsPersonalAccess, &d.CreatedAt)
			return d, err
		},
		func(ctx context.Context, d sourceDeveloperApp) error {
			meta := developerAppMetadata{
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
			// DP uses params.signer (the app address) as the DeveloperApp identity.
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     d.UserID,
				EntityType: "DeveloperApp",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, d.Address)
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
}

func (w *Writer) writeGrants(ctx context.Context) error {
	return processBatched(ctx, w, "grants",
		`SELECT count(*) FROM grants WHERE is_current = true AND is_revoked = false AND is_approved = true`,
		`SELECT grantee_address, user_id, created_at
		FROM grants
		WHERE is_current = true AND is_revoked = false AND is_approved = true
		ORDER BY user_id, grantee_address`,
		func(rows pgx.Rows) (sourceGrant, error) {
			var g sourceGrant
			err := rows.Scan(&g.GranteeAddress, &g.UserID, &g.CreatedAt)
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
			// DP uses the grantee address as signer for Grant creation.
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     g.UserID,
				EntityType: "Grant",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, g.GranteeAddress)
		},
	)
}
