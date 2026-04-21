package entity_manager

import (
	"context"
)

// --- Pin ---

type commentPinHandler struct{}

func (h *commentPinHandler) EntityType() string { return EntityTypeComment }
func (h *commentPinHandler) Action() string     { return ActionPin }

func (h *commentPinHandler) Handle(ctx context.Context, params *Params) error {
	if err := validatePinTx(ctx, params, true); err != nil {
		return err
	}
	trackID, _ := params.MetadataInt64("entity_id")

	// Update the track's pinned_comment_id
	_, err := params.DBTX.Exec(ctx, `
		UPDATE tracks SET pinned_comment_id = $1, updated_at = $2
		WHERE track_id = $3 AND is_current = true
	`, params.EntityID, params.BlockTime, trackID)
	return err
}

// --- Unpin ---

type commentUnpinHandler struct{}

func (h *commentUnpinHandler) EntityType() string { return EntityTypeComment }
func (h *commentUnpinHandler) Action() string     { return ActionUnpin }

func (h *commentUnpinHandler) Handle(ctx context.Context, params *Params) error {
	if err := validatePinTx(ctx, params, false); err != nil {
		return err
	}
	trackID, _ := params.MetadataInt64("entity_id")

	// Clear pinned_comment_id
	_, err := params.DBTX.Exec(ctx, `
		UPDATE tracks SET pinned_comment_id = NULL, updated_at = $1
		WHERE track_id = $2 AND is_current = true
	`, params.BlockTime, trackID)
	return err
}

func validatePinTx(ctx context.Context, params *Params, isPin bool) error {
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

	// Track must exist and signer must be track owner
	trackID, ok := params.MetadataInt64("entity_id")
	if !ok {
		return NewValidationError("entity_id is required for pin/unpin")
	}
	trackOwner, err := getTrackOwner(ctx, params.DBTX, trackID)
	if err != nil {
		return NewValidationError("track %d does not exist", trackID)
	}
	if params.UserID != trackOwner {
		return NewValidationError("only track owner can pin/unpin comments")
	}

	// Check current pin state
	var pinnedCommentID *int64
	err = params.DBTX.QueryRow(ctx,
		"SELECT pinned_comment_id FROM tracks WHERE track_id = $1 AND is_current = true",
		trackID).Scan(&pinnedCommentID)
	if err != nil {
		return err
	}

	if isPin && pinnedCommentID != nil && *pinnedCommentID == params.EntityID {
		return NewValidationError("comment %d is already pinned", params.EntityID)
	}
	if !isPin && (pinnedCommentID == nil || *pinnedCommentID != params.EntityID) {
		return NewValidationError("comment %d is not pinned", params.EntityID)
	}

	return nil
}

// CommentPin returns the Comment Pin handler.
func CommentPin() Handler { return &commentPinHandler{} }

// CommentUnpin returns the Comment Unpin handler.
func CommentUnpin() Handler { return &commentUnpinHandler{} }
