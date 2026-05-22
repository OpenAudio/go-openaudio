package entity_manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5"
)

// SubscriptionEntityTypeEvent: when subscriptions.entity_type is "Event",
// subscriptions.user_id is the event_id (the column is overloaded).
const SubscriptionEntityTypeEvent = "Event"

// autoSubscribeToContestOnSubmission writes a subscriptions row when a Track
// Create is a remix submission to an active remix_contest event.
func autoSubscribeToContestOnSubmission(ctx context.Context, params *Params) error {
	if stemMap, ok := params.Metadata["stem_of"].(map[string]any); ok && stemMap != nil {
		return nil
	}
	parentIDs := getRemixParentTrackIDs(params.Metadata)
	if len(parentIDs) == 0 {
		return nil
	}
	parentTrackID := parentIDs[0]

	var eventID, hostID int64
	err := params.DBTX.QueryRow(ctx, `
		SELECT event_id, user_id FROM events
		WHERE event_type = 'remix_contest'
		  AND entity_id = $1
		  AND is_deleted = false
		  AND (end_date IS NULL OR end_date > $2)
		LIMIT 1
	`, parentTrackID, params.BlockTime).Scan(&eventID, &hostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	if params.UserID == hostID {
		return nil
	}

	already, err := contestEventSubscriptionExists(ctx, params.DBTX, params.UserID, eventID)
	if err != nil {
		return err
	}
	if already {
		return nil
	}

	// Synthetic txhash so re-processed chain txs collide on the PK.
	syntheticTxHash := fmt.Sprintf("auto_e%d_%s", eventID, params.TxHash)
	if len(syntheticTxHash) > 200 {
		syntheticTxHash = syntheticTxHash[:200]
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO subscriptions (
			subscriber_id, user_id, entity_type, entity_id,
			is_current, is_delete,
			created_at, txhash, blockhash, blocknumber
		) VALUES (
			$1, $2, $3, $2,
			true, false,
			$4, $5, $6, $7
		)
		ON CONFLICT (subscriber_id, user_id, txhash) DO NOTHING
	`,
		params.UserID, eventID, SubscriptionEntityTypeEvent,
		params.BlockTime, syntheticTxHash, params.BlockHash, params.BlockNumber)
	return err
}

func contestEventSubscriptionExists(ctx context.Context, dbtx db.DBTX, subscriberID, eventID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM subscriptions
			WHERE subscriber_id = $1
			  AND user_id = $2
			  AND entity_type = $3
			  AND is_current = true
			  AND is_delete = false
		)
	`, subscriberID, eventID, SubscriptionEntityTypeEvent).Scan(&exists)
	return exists, err
}
