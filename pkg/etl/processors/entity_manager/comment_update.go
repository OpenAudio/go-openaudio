package entity_manager

import (
	"context"
)

type commentUpdateHandler struct{}

func (h *commentUpdateHandler) EntityType() string { return EntityTypeComment }
func (h *commentUpdateHandler) Action() string     { return ActionUpdate }

func (h *commentUpdateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateCommentWrite(ctx, params, false); err != nil {
		return err
	}

	body := params.MetadataString("body")

	_, err := params.DBTX.Exec(ctx, `
		UPDATE comments SET
			text = $1, is_edited = true, updated_at = $2,
			txhash = $3, blocknumber = $4
		WHERE comment_id = $5
	`, body, params.BlockTime, params.TxHash, params.BlockNumber, params.EntityID)
	if err != nil {
		return err
	}

	// Handle mention updates
	if mentions, ok := getMetadataMentions(params); ok {
		// Mark all existing mentions as deleted
		_, err := params.DBTX.Exec(ctx, `
			UPDATE comment_mentions SET is_delete = true, updated_at = $1, txhash = $2, blocknumber = $3
			WHERE comment_id = $4 AND is_delete = false
		`, params.BlockTime, params.TxHash, params.BlockNumber, params.EntityID)
		if err != nil {
			return err
		}
		// Upsert the new mention set
		for _, mentionUserID := range mentions {
			_, err := params.DBTX.Exec(ctx, `
				INSERT INTO comment_mentions (
					comment_id, user_id, created_at, updated_at, is_delete,
					txhash, blockhash, blocknumber
				) VALUES ($1, $2, $3, $3, false, $4, $5, $6)
				ON CONFLICT (comment_id, user_id) DO UPDATE SET is_delete = false, updated_at = $3, txhash = $4, blocknumber = $6
			`, params.EntityID, mentionUserID, params.BlockTime, params.TxHash, params.BlockHash, params.BlockNumber)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// CommentUpdate returns the Comment Update handler.
func CommentUpdate() Handler { return &commentUpdateHandler{} }
