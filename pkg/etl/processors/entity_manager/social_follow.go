package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
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
	// Upsert the single current row in place (arbiter: follows_current_uniq_idx).
	// Replaces demote-then-insert: avoids unbounded is_current=false history and
	// gives the aggregate triggers an O(1) is_delete transition to track.
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO follows (
			follower_user_id, followee_user_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, true, $3, $4, $5, $6)
		ON CONFLICT (follower_user_id, followee_user_id) WHERE is_current = true
		DO UPDATE SET
			is_delete = EXCLUDED.is_delete,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
	`, params.UserID, params.EntityID, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
	if err != nil {
		return err
	}

	// Follow/Unfollow also creates/deletes a Subscription record
	// (arbiter: subscriptions_current_uniq_idx). entity_type is written
	// explicitly and is part of the arbiter (migration 0037) so this upsert
	// can never land on — or tombstone — an Event subscription whose event id
	// collides numerically with the followee's user id. entity_id is the
	// canonical target for both entity types (see insertSubscription); the
	// DO UPDATE also heals any pre-0038 NULL left on the row.
	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO subscriptions (
			subscriber_id, user_id, entity_type, entity_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $2, true, $4, $5, $6, $7)
		ON CONFLICT (subscriber_id, user_id, entity_type) WHERE is_current = true
		DO UPDATE SET
			entity_id = EXCLUDED.entity_id,
			is_delete = EXCLUDED.is_delete,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
	`, params.UserID, params.EntityID, EntityTypeUser, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
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
