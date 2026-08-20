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
		JOIN users u ON u.user_id = e.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
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
				CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			if e.EntityType != nil {
				meta.EntityType = *e.EntityType
			}
			if e.EndDate != nil {
				meta.EndDate = e.EndDate.UTC().Format(time.RFC3339Nano)
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

// --- Event subscriptions ---

// writeEventSubscriptions replays subscriptions to an event — a listener
// following a remix contest so they are notified when the host posts an update.
//
// These share the `subscriptions` table with user subscriptions but are a
// different kind of row: entity_type is 'Event', and the target is an event id,
// carried in entity_id and mirrored into the overloaded user_id column.
//
// writeSubscriptions cannot carry them and never did. It hardcodes EntityType
// "User" and joins the target id against `users`; no event id matches a user id,
// so all 606 Event rows on a production clone were dropped before they reached
// the emit step — the step was silently a no-op for them rather than emitting a
// mistyped transaction.
//
// The entity type has to travel in the transaction, because the indexer's
// Subscribe handler is registered against the wildcard entity type and reads
// params.EntityType to decide what it is writing, and the current-subscription
// identity is (subscriber_id, user_id, entity_type). This matches what a live
// client sends: EventsApi.followEventWithEntityManager submits Subscribe with
// entityType Event and the event id as the entity id.
//
// Ordered after writeEvents: the indexer's validateSubscribe rejects a
// subscription whose target event does not exist yet.
func (w *Writer) writeEventSubscriptions(ctx context.Context) error {
	type eventSubscription struct {
		subscriberID, eventID int64
		wallet                string
		createdAt             time.Time
		isDelete              bool
	}
	return processBatched(ctx, w, "event subscriptions",
		`SELECT count(*) FROM subscriptions s
		JOIN users u ON u.user_id = s.subscriber_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		JOIN events e ON e.event_id = s.entity_id AND e.is_deleted = false
		WHERE s.is_current = true AND s.entity_type = 'Event'`,
		`SELECT s.subscriber_id, s.entity_id, LOWER(u.wallet), s.created_at, s.is_delete
		FROM subscriptions s
		JOIN users u ON u.user_id = s.subscriber_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		JOIN events e ON e.event_id = s.entity_id AND e.is_deleted = false
		WHERE s.is_current = true AND s.entity_type = 'Event'
		ORDER BY s.subscriber_id, s.entity_id`,
		func(rows pgx.Rows) (eventSubscription, error) {
			var s eventSubscription
			err := rows.Scan(&s.subscriberID, &s.eventID, &s.wallet, &s.createdAt, &s.isDelete)
			return s, err
		},
		func(ctx context.Context, s eventSubscription) error {
			// socialMeta, not a bespoke payload: the indexer replays this through
			// the same migratedSocial(Subscribe) handler as a user subscription,
			// which reads is_delete from metadata so a soft-deleted subscription
			// is one transaction instead of a subscribe/unsubscribe pair.
			metaJSON, err := json.Marshal(socialMeta{CreatedAt: fmtCreatedAt(s.createdAt), IsDelete: s.isDelete})
			if err != nil {
				return fmt.Errorf("marshal event subscription metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.subscriberID,
				EntityType: "Event",
				EntityId:   s.eventID,
				Action:     "Subscribe",
				Metadata:   string(metaJSON),
			}, s.wallet)
		},
	)
}
