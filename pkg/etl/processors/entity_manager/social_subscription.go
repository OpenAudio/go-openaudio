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
	entityType := subscriptionEntityType(params)
	if entityType == EntityTypeUser && params.UserID == params.EntityID {
		return NewValidationError("user cannot subscribe to themselves")
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	exists, err := subscriptionTargetExists(ctx, params)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("subscription target %s %d does not exist", entityType, params.EntityID)
	}
	dup, err := subscriptionExists(ctx, params.DBTX, params.UserID, params.EntityID, entityType)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("subscription already exists from %d to %d", params.UserID, params.EntityID)
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
	dup, err := subscriptionExists(ctx, params.DBTX, params.UserID, params.EntityID, subscriptionEntityType(params))
	if err != nil {
		return err
	}
	if !dup {
		return NewValidationError("no active subscription from %d to %d", params.UserID, params.EntityID)
	}
	return nil
}

func insertSubscription(ctx context.Context, params *Params, isDelete bool) error {
	entityType := subscriptionEntityType(params)

	// Upsert the single current row in place (arbiter: subscriptions_current_uniq_idx),
	// matching the Follow auto-subscribe path in social_follow.go. The prior
	// demote-then-insert was a two-statement write: between the demote and the
	// insert another subscription writer could land a second current row, which
	// is how duplicate is_current rows accumulated here but not in the
	// single-writer reposts/saves/follows tables.
	//
	// entity_type is part of the identity (migration 0037): user_id is
	// overloaded with the event id for Event subscriptions, so a User and an
	// Event subscription to the same numeric id are distinct rows.
	//
	// entity_id is the canonical target for both entity types (0038 backfilled
	// the pre-existing User rows); user_id is kept as a legacy mirror until
	// all readers key on (entity_type, entity_id).
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO subscriptions (
			subscriber_id, user_id, entity_type, entity_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, true, $5, $6, $7, $8)
		ON CONFLICT (subscriber_id, user_id, entity_type) WHERE is_current = true
		DO UPDATE SET
			entity_id = EXCLUDED.entity_id,
			is_delete = EXCLUDED.is_delete,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
	`, params.UserID, params.EntityID, entityType, params.EntityID, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func subscriptionEntityType(params *Params) string {
	if params.EntityType == EntityTypeEvent {
		return EntityTypeEvent
	}
	return EntityTypeUser
}

func subscriptionTargetExists(ctx context.Context, params *Params) (bool, error) {
	if subscriptionEntityType(params) == EntityTypeEvent {
		return eventExists(ctx, params.DBTX, params.EntityID)
	}
	return userExists(ctx, params.DBTX, params.EntityID)
}

func subscriptionExists(ctx context.Context, dbtx db.DBTX, subscriberID, userID int64, entityType string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM subscriptions WHERE subscriber_id = $1 AND user_id = $2 AND entity_type = $3 AND is_current = true AND is_delete = false)",
		subscriberID, userID, entityType).Scan(&exists)
	return exists, err
}

func Subscribe() Handler   { return &subscribeHandler{} }
func Unsubscribe() Handler { return &unsubscribeHandler{} }
