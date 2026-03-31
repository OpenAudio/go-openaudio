package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// --- Mute ---

type muteUserHandler struct{}

func (h *muteUserHandler) EntityType() string { return EntityTypeUser }
func (h *muteUserHandler) Action() string     { return ActionMute }

func (h *muteUserHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Muted user must exist
	exists, err := userExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("muted user %d does not exist", params.EntityID)
	}
	// Cannot mute yourself
	if params.UserID == params.EntityID {
		return NewValidationError("user cannot mute themselves")
	}
	// Check for duplicate active mute
	dup, err := mutedUserExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("user %d already muted by %d", params.EntityID, params.UserID)
	}
	return insertMutedUser(ctx, params, false)
}

// --- Unmute ---

type unmuteUserHandler struct{}

func (h *unmuteUserHandler) EntityType() string { return EntityTypeUser }
func (h *unmuteUserHandler) Action() string     { return ActionUnmute }

func (h *unmuteUserHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Must have active mute
	exists, err := mutedUserExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("no active mute of user %d by %d", params.EntityID, params.UserID)
	}
	return insertMutedUser(ctx, params, true)
}

// --- shared ---

func insertMutedUser(ctx context.Context, params *Params, isDelete bool) error {
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO muted_users (
			muted_user_id, user_id, is_delete,
			created_at, updated_at, txhash, blocknumber
		) VALUES ($1, $2, $3, $4, $4, $5, $6)
		ON CONFLICT (muted_user_id, user_id)
		DO UPDATE SET is_delete = $3, updated_at = $4, txhash = $5, blocknumber = $6
	`, params.EntityID, params.UserID, isDelete, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func mutedUserExists(ctx context.Context, dbtx db.DBTX, userID, mutedUserID int64) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM muted_users WHERE user_id = $1 AND muted_user_id = $2 AND (is_delete = false OR is_delete IS NULL))",
		userID, mutedUserID).Scan(&exists)
	return exists, err
}

// MuteUser returns the MuteUser handler.
func MuteUser() Handler { return &muteUserHandler{} }

// UnmuteUser returns the UnmuteUser handler.
func UnmuteUser() Handler { return &unmuteUserHandler{} }
