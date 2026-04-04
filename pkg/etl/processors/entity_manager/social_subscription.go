package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
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
	if params.UserID == params.EntityID {
		return NewValidationError("user cannot subscribe to themselves")
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	exists, err := userExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("subscription target user %d does not exist", params.EntityID)
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
	_, err := params.DBTX.Exec(ctx,
		"UPDATE subscriptions SET is_current = false WHERE subscriber_id = $1 AND user_id = $2 AND is_current = true",
		params.UserID, params.EntityID)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO subscriptions (
			subscriber_id, user_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, true, $3, $4, $5, $6)
	`, params.UserID, params.EntityID, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
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
