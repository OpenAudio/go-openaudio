package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

type dashboardWalletMetadata struct {
	Wallet    string `json:"wallet"`
	CreatedAt string `json:"created_at,omitempty"`
}

type sourceDashboardWalletUser struct {
	Wallet    string
	UserID    int64
	CreatedAt time.Time
}

func (w *Writer) writeDashboardWalletUsers(ctx context.Context) error {
	return processBatched(ctx, w, "dashboard_wallet_users",
		`SELECT count(*) FROM dashboard_wallet_users WHERE is_delete = false`,
		`SELECT wallet, user_id, created_at
		FROM dashboard_wallet_users
		WHERE is_delete = false
		ORDER BY user_id, wallet`,
		func(rows pgx.Rows) (sourceDashboardWalletUser, error) {
			var d sourceDashboardWalletUser
			err := rows.Scan(&d.Wallet, &d.UserID, &d.CreatedAt)
			return d, err
		},
		func(ctx context.Context, d sourceDashboardWalletUser) error {
			metaJSON, err := json.Marshal(dashboardWalletMetadata{
				Wallet:    d.Wallet,
				CreatedAt: d.CreatedAt.Format(time.RFC3339),
			})
			if err != nil {
				return fmt.Errorf("marshal dashboard wallet user metadata: %w", err)
			}
			// DP uses params.signer (the wallet) as identity for DashboardWalletUser.
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     d.UserID,
				EntityType: "DashboardWalletUser",
				EntityId:   0,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, d.Wallet)
		},
	)
}
