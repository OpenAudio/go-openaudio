package main

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// playCountReconciliation emits a ManageEntity transaction that tells the ETL
// to set a track's aggregate play count to a specific value. This covers the
// historical plays that were pruned from the plays table (older than ~400 days)
// and cannot be reconstructed from individual play records.
//
// The delta is: aggregate_plays.count - COUNT(plays for that track).
// Tracks where the plays table already accounts for the full count are skipped.

type playCountReconciliationMetadata struct {
	Delta int64 `json:"delta"`
}

type sourcePlayCountDelta struct {
	PlayItemID int64
	Delta      int64
}

func (w *Writer) writePlayCountReconciliation(ctx context.Context) error {
	return processBatched(ctx, w, "play_count_reconciliation",
		// Count tracks that have a positive delta (pruned plays not in the plays table).
		`SELECT count(*) FROM (
			SELECT ap.play_item_id
			FROM aggregate_plays ap
			LEFT JOIN (
				SELECT play_item_id, count(*) AS cnt
				FROM plays
				GROUP BY play_item_id
			) p ON p.play_item_id = ap.play_item_id
			WHERE ap.count - COALESCE(p.cnt, 0) > 0
		) t`,
		// Select the delta for each track.
		`SELECT ap.play_item_id, ap.count - COALESCE(p.cnt, 0) AS delta
		FROM aggregate_plays ap
		LEFT JOIN (
			SELECT play_item_id, count(*) AS cnt
			FROM plays
			GROUP BY play_item_id
		) p ON p.play_item_id = ap.play_item_id
		WHERE ap.count - COALESCE(p.cnt, 0) > 0
		ORDER BY ap.play_item_id`,
		func(rows pgx.Rows) (sourcePlayCountDelta, error) {
			var d sourcePlayCountDelta
			err := rows.Scan(&d.PlayItemID, &d.Delta)
			return d, err
		},
		func(ctx context.Context, d sourcePlayCountDelta) error {
			metaJSON, err := json.Marshal(playCountReconciliationMetadata{
				Delta: d.Delta,
			})
			if err != nil {
				return fmt.Errorf("marshal play count reconciliation metadata for track %d: %w", d.PlayItemID, err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     0,
				EntityType: "PlayCount",
				EntityId:   d.PlayItemID,
				Action:     "Reconcile",
				Metadata:   string(metaJSON),
			})
		},
	)
}
