package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

type tipReactionMetadata struct {
	ReactedTo     string `json:"reacted_to"`
	ReactionValue string `json:"reaction_value"`
	CreatedAt     string `json:"created_at,omitempty"`
}

type sourceTipReaction struct {
	SenderWallet  string
	ReactedTo     string
	ReactionValue string
	Timestamp     time.Time
}

func (w *Writer) writeTipReactions(ctx context.Context) error {
	// Pre-load wallet → user_id mapping so we can set UserId on the ManageEntity.
	walletToUser, err := preloadMap[string, int64](ctx, w.srcDB,
		`SELECT LOWER(wallet), user_id FROM users WHERE is_current = true AND wallet IS NOT NULL ORDER BY wallet, user_id`)
	if err != nil {
		return fmt.Errorf("preload wallet→user map: %w", err)
	}

	return processBatched(ctx, w, "tip_reactions",
		`SELECT count(*) FROM reactions WHERE reaction_type = 'tip'`,
		`SELECT sender_wallet, reacted_to, reaction_value, timestamp
		FROM reactions
		WHERE reaction_type = 'tip'
		ORDER BY id`,
		func(rows pgx.Rows) (sourceTipReaction, error) {
			var tr sourceTipReaction
			err := rows.Scan(&tr.SenderWallet, &tr.ReactedTo, &tr.ReactionValue, &tr.Timestamp)
			return tr, err
		},
		func(ctx context.Context, tr sourceTipReaction) error {
			// Look up user ID from sender wallet.
			users := walletToUser[strings.ToLower(tr.SenderWallet)]
			if len(users) == 0 {
				w.logger.Warn("tip reaction sender wallet not found, skipping",
					zap.String("wallet", tr.SenderWallet))
				return nil
			}
			userID := users[0]

			metaJSON, err := json.Marshal(tipReactionMetadata{
				ReactedTo:     tr.ReactedTo,
				ReactionValue: tr.ReactionValue,
				CreatedAt:     tr.Timestamp.Format(time.RFC3339),
			})
			if err != nil {
				return fmt.Errorf("marshal tip reaction metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     userID,
				EntityType: "Tip",
				EntityId:   0,
				Action:     "React",
				Metadata:   string(metaJSON),
			})
		},
	)
}
