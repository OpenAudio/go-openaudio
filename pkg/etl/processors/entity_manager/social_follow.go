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

// insertFollow records a live follow: the follow itself and the subscription it
// implies.
func insertFollow(ctx context.Context, params *Params, isDelete bool) error {
	if err := insertFollowRow(ctx, params, isDelete); err != nil {
		return err
	}
	return insertImpliedSubscription(ctx, params, isDelete)
}

// insertMigratedFollow records the follow without inferring a subscription.
//
// The genesis migration replays the source's own subscriptions table as
// explicit Subscribe transactions, covering 6,341,259 of its 6,341,432 rows, so
// inferring one per follow adds subscriptions the user never had. A full replay
// produced 26,115,620 subscription rows against a source of 6,341,432 -- a 4.1x
// inflation -- and every explicit Subscribe then collided with a row the follow
// had already written (726,607 rejections).
func insertMigratedFollow(ctx context.Context, params *Params, isDelete bool) error {
	return insertFollowRow(ctx, params, isDelete)
}

// insertFollowRow upserts the follow itself.
func insertFollowRow(ctx context.Context, params *Params, isDelete bool) error {
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

	return nil
}

// insertImpliedSubscription writes the subscription a live follow implies.
// Only the production path calls it -- see insertMigratedFollow.
// (arbiter: subscriptions_current_uniq_idx)
func insertImpliedSubscription(ctx context.Context, params *Params, isDelete bool) error {
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO subscriptions (
			subscriber_id, user_id, is_current, is_delete,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, true, $3, $4, $5, $6)
		ON CONFLICT (subscriber_id, user_id) WHERE is_current = true
		DO UPDATE SET
			is_delete = EXCLUDED.is_delete,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
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
