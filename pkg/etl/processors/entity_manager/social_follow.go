package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// --- Follow ---

type followHandler struct{}

func (h *followHandler) EntityType() string { return EntityTypeAny }
func (h *followHandler) Action() string     { return ActionFollow }

func (h *followHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateFollow(ctx, params); err != nil {
		return err
	}
	return insertFollow(ctx, params, false)
}

func validateFollow(ctx context.Context, params *Params) error {
	if params.UserID == params.EntityID {
		return NewValidationError("user cannot follow themselves")
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	exists, err := userExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("followee user %d does not exist", params.EntityID)
	}
	// Check for duplicate active follow
	dup, err := followExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("follow already exists from %d to %d", params.UserID, params.EntityID)
	}
	return nil
}

// --- Unfollow ---

type unfollowHandler struct{}

func (h *unfollowHandler) EntityType() string { return EntityTypeAny }
func (h *unfollowHandler) Action() string     { return ActionUnfollow }

func (h *unfollowHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUnfollow(ctx, params); err != nil {
		return err
	}
	return insertFollow(ctx, params, true)
}

func validateUnfollow(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Follow must exist and not be deleted
	dup, err := followExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if !dup {
		return NewValidationError("no active follow from %d to %d", params.UserID, params.EntityID)
	}
	return nil
}

// --- shared ---

func insertFollow(ctx context.Context, params *Params, isDelete bool) error {
	_, err := params.DBTX.Exec(ctx,
		"UPDATE follows SET is_current = false WHERE follower_user_id = $1 AND followee_user_id = $2 AND is_current = true",
		params.UserID, params.EntityID)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO follows (
			follower_user_id, followee_user_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, true, $3, $4, $5, $6)
	`, params.UserID, params.EntityID, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
	if err != nil {
		return err
	}

	// Python parity: Follow/Unfollow also creates/deletes a Subscription record
	_, err = params.DBTX.Exec(ctx,
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

func followExists(ctx context.Context, dbtx db.DBTX, followerID, followeeID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM follows WHERE follower_user_id = $1 AND followee_user_id = $2 AND is_current = true AND is_delete = false)",
		followerID, followeeID).Scan(&exists)
	return exists, err
}

func Follow() Handler   { return &followHandler{} }
func Unfollow() Handler { return &unfollowHandler{} }
