package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

type subscribeHandler struct{}

func (h *subscribeHandler) EntityType() string { return EntityTypeAny }
func (h *subscribeHandler) Action() string     { return ActionSubscribe }

func (h *subscribeHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateSubscribe(ctx, params); err != nil {
		return err
	}
	return insertSubscription(ctx, params, false)
}

func validateSubscribe(ctx context.Context, params *Params) error {
	// The self-subscribe guard only makes sense for User subscriptions, where
	// UserID and EntityID are both user ids. For an Event follow EntityID is an
	// event id in a separate namespace, so skip it there.
	if params.EntityType != EntityTypeEvent && params.UserID == params.EntityID {
		return NewValidationError("user cannot subscribe to themselves")
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	if err := validateSubscriptionTarget(ctx, params); err != nil {
		return err
	}
	dup, err := subscriptionExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("subscription already exists from %d to %d", params.UserID, params.EntityID)
	}
	return nil
}

// validateSubscriptionTarget confirms the entity being subscribed to exists.
// Subscribe/Unsubscribe are wildcard (EntityTypeAny) handlers, so the same
// action covers both legacy User subscriptions and remix-contest Event
// follows — each must be validated against its own table. Previously an Event
// follow was checked against the users table (its event_id read as a user_id),
// failed the existence check, and was rejected before any row was written —
// which is why following a contest never took effect.
func validateSubscriptionTarget(ctx context.Context, params *Params) error {
	if params.EntityType == EntityTypeEvent {
		exists, err := eventExists(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		if !exists {
			return NewValidationError("subscription target event %d does not exist", params.EntityID)
		}
		return nil
	}
	exists, err := userExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("subscription target user %d does not exist", params.EntityID)
	}
	return nil
}

type unsubscribeHandler struct{}

func (h *unsubscribeHandler) EntityType() string { return EntityTypeAny }
func (h *unsubscribeHandler) Action() string     { return ActionUnsubscribe }

func (h *unsubscribeHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUnsubscribe(ctx, params); err != nil {
		return err
	}
	return insertSubscription(ctx, params, true)
}

func validateUnsubscribe(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	dup, err := subscriptionExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if !dup {
		return NewValidationError("no active subscription from %d to %d", params.UserID, params.EntityID)
	}
	return nil
}

func insertSubscription(ctx context.Context, params *Params, isDelete bool) error {
	// subscriptions.user_id is overloaded: for an Event subscription it holds
	// the event_id, and entity_type/entity_id record the real target so reads
	// can tell a contest follow apart from a legacy User subscription that
	// happens to share a numeric id (see migration 0007 + the Track-Create
	// auto-subscribe in track_contest_subscribe.go). A User subscription keeps
	// entity_type='User' and a NULL entity_id, matching the column defaults.
	entityType := EntityTypeUser
	var entityID *int64
	if params.EntityType == EntityTypeEvent {
		entityType = EntityTypeEvent
		eventID := params.EntityID
		entityID = &eventID
	}

	// Upsert the single current row in place (arbiter: subscriptions_current_uniq_idx),
	// matching the Follow auto-subscribe path in social_follow.go. The prior
	// demote-then-insert was a two-statement write: between the demote and the
	// insert another subscription writer could land a second current row, which
	// is how duplicate is_current rows accumulated here but not in the
	// single-writer reposts/saves/follows tables.
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO subscriptions (
			subscriber_id, user_id, entity_type, entity_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, true, $5, $6, $7, $8)
		ON CONFLICT (subscriber_id, user_id) WHERE is_current = true
		DO UPDATE SET
			entity_type = EXCLUDED.entity_type,
			entity_id = EXCLUDED.entity_id,
			is_delete = EXCLUDED.is_delete,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
	`, params.UserID, params.EntityID, entityType, entityID, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func subscriptionExists(ctx context.Context, dbtx db.DBTX, subscriberID, userID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM subscriptions WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true AND is_delete = false)",
		subscriberID, userID).Scan(&exists)
	return exists, err
}

func Subscribe() Handler   { return &subscribeHandler{} }
func Unsubscribe() Handler { return &unsubscribeHandler{} }
