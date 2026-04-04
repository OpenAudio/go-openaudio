package entity_manager

import (
	"context"
)

type commentDeleteHandler struct{}

func (h *commentDeleteHandler) EntityType() string { return EntityTypeComment }
func (h *commentDeleteHandler) Action() string     { return ActionDelete }

func (h *commentDeleteHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	// Comment must exist
	exists, err := commentExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !exists {
		return NewValidationError("comment %d does not exist", params.EntityID)
	}

	// Signer must be comment owner or track owner
	commentOwner, err := getCommentOwner(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if params.UserID != commentOwner {
		// Check if user is the track owner
		entityID, err := getCommentEntityID(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		trackOwner, err := getTrackOwner(ctx, params.DBTX, entityID)
		if err != nil {
			return err
		}
		if params.UserID != trackOwner {
			return NewValidationError("only comment owner or track owner can delete comment %d", params.EntityID)
		}
	}

	_, err = params.DBTX.Exec(ctx, `
		UPDATE comments SET
			is_delete = true, updated_at = $1,
			txhash = $2, blocknumber = $3
		WHERE comment_id = $4
	`, params.BlockTime, params.TxHash, params.BlockNumber, params.EntityID)
	return err
}

// CommentDelete returns the Comment Delete handler.
func CommentDelete() Handler { return &commentDeleteHandler{} }
