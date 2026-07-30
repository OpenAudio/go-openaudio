package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

type eventMetadata struct {
	EventType  string `json:"event_type"`
	EndDate    string `json:"end_date,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
	EntityID   *int64 `json:"entity_id,omitempty"`
	EventData  any    `json:"event_data,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
}

type sourceEvent struct {
	EventID    int64
	EventType  string
	UserID     int64
	UserWallet string
	EntityType *string
	EntityID   *int64
	EndDate    *time.Time
	EventData  []byte // JSONB
	CreatedAt  time.Time
}

func (w *Writer) writeEvents(ctx context.Context) error {
	return processBatched(ctx, w, "events",
		`SELECT count(*) FROM events WHERE is_deleted = false`,
		`SELECT e.event_id, e.event_type, e.user_id, COALESCE(LOWER(u.wallet), ''),
			e.entity_type, e.entity_id, e.end_date, e.event_data, e.created_at
		FROM events e
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = e.user_id
		WHERE e.is_deleted = false
		ORDER BY e.event_id`,
		func(rows pgx.Rows) (sourceEvent, error) {
			var e sourceEvent
			err := rows.Scan(&e.EventID, &e.EventType, &e.UserID, &e.UserWallet,
				&e.EntityType, &e.EntityID, &e.EndDate, &e.EventData, &e.CreatedAt)
			return e, err
		},
		func(ctx context.Context, e sourceEvent) error {
			meta := eventMetadata{
				EventType: e.EventType,
				EntityID:  e.EntityID,
				CreatedAt: e.CreatedAt.Format(time.RFC3339),
			}
			if e.EntityType != nil {
				meta.EntityType = *e.EntityType
			}
			if e.EndDate != nil {
				meta.EndDate = e.EndDate.Format(time.RFC3339)
			}
			meta.EventData = unmarshalJSONB(e.EventData)

			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal event %d metadata: %w", e.EventID, err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     e.UserID,
				EntityType: "Event",
				EntityId:   e.EventID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, e.UserWallet)
		},
	)
}
