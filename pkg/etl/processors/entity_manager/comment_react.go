package entity_manager

import (
	"context"
)

// --- React ---

type commentReactHandler struct{}

func (h *commentReactHandler) EntityType() string { return EntityTypeComment }
func (h *commentReactHandler) Action() string     { return ActionReact }

func (h *commentReactHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Comment must exist and not be deleted
	exists, err := commentExistsActive(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("comment %d does not exist or is deleted", params.EntityID)
	}
	// Check for duplicate reaction
	dup, err := commentReactionExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("user %d already reacted to comment %d", params.UserID, params.EntityID)
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO comment_reactions (
			comment_id, user_id, created_at, updated_at, is_delete,
			txhash, blockhash, blocknumber
		) VALUES ($1, $2, $3, $3, false, $4, $5, $6)
		ON CONFLICT (comment_id, user_id) DO UPDATE SET is_delete = false, updated_at = $3, txhash = $4, blocknumber = $6
	`, params.EntityID, params.UserID, params.BlockTime, params.TxHash, params.BlockHash, params.BlockNumber)
	return err
}

// --- Unreact ---

type commentUnreactHandler struct{}

func (h *commentUnreactHandler) EntityType() string { return EntityTypeComment }
func (h *commentUnreactHandler) Action() string     { return ActionUnreact }

func (h *commentUnreactHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Must have active reaction
	exists, err := commentReactionExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("user %d has not reacted to comment %d", params.UserID, params.EntityID)
	}

	_, err = params.DBTX.Exec(ctx, `
		UPDATE comment_reactions SET
			is_delete = true, updated_at = $1, txhash = $2, blocknumber = $3
		WHERE comment_id = $4 AND user_id = $5
	`, params.BlockTime, params.TxHash, params.BlockNumber, params.EntityID, params.UserID)
	return err
}

// CommentReact returns the Comment React handler.
func CommentReact() Handler { return &commentReactHandler{} }

// CommentUnreact returns the Comment Unreact handler.
func CommentUnreact() Handler { return &commentUnreactHandler{} }
