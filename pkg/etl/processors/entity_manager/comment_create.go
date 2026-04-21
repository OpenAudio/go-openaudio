package entity_manager

import (
	"context"
)

type commentCreateHandler struct{}

func (h *commentCreateHandler) EntityType() string { return EntityTypeComment }
func (h *commentCreateHandler) Action() string     { return ActionCreate }

func (h *commentCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateCommentWrite(ctx, params, true); err != nil {
		return err
	}

	body := params.MetadataString("body")
	entityID, _ := params.MetadataInt64("entity_id")
	entityType := params.MetadataString("entity_type")
	if entityType == "" {
		entityType = EntityTypeTrack
	}
	trackTimestamp, hasTimestamp := params.MetadataInt64("track_timestamp_s")

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO comments (
			comment_id, text, user_id, entity_id, entity_type,
			track_timestamp_s, created_at, updated_at,
			is_delete, is_visible, is_edited,
			txhash, blockhash, blocknumber
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $7, false, true, false, $8, $9, $10)
	`, params.EntityID, body, params.UserID, entityID, entityType,
		nullableInt(trackTimestamp, hasTimestamp),
		params.BlockTime, params.TxHash, params.BlockHash, params.BlockNumber)
	if err != nil {
		return err
	}

	// Insert mentions (first 10)
	if mentions, ok := getMetadataMentions(params); ok {
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

	// Insert thread relationship if this is a reply
	if parentID, ok := params.MetadataInt64("parent_comment_id"); ok && parentID > 0 {
		_, err := params.DBTX.Exec(ctx, `
			INSERT INTO comment_threads (parent_comment_id, comment_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, parentID, params.EntityID)
		if err != nil {
			return err
		}
	}

	return nil
}

func validateCommentWrite(ctx context.Context, params *Params, isCreate bool) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	if isCreate {
		exists, err := commentExists(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		if exists {
			return NewValidationError("comment %d already exists", params.EntityID)
		}
	} else {
		exists, err := commentExistsActive(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		if !exists {
			return NewValidationError("comment %d does not exist or is deleted", params.EntityID)
		}
	}

	// entity_type supports Track and FanClub
	if et := params.MetadataString("entity_type"); et != "" && et != EntityTypeTrack && et != "FanClub" {
		return NewValidationError("entity type %q is not supported for comments", et)
	}

	entityID, ok := params.MetadataInt64("entity_id")
	if !ok {
		return NewValidationError("entity_id is required for comment")
	}
	et := params.MetadataString("entity_type")
	if et == "" || et == EntityTypeTrack {
		exists, err := trackExists(ctx, params.DBTX, entityID)
		if err != nil {
			return err
		}
		if !exists {
			return NewValidationError("track %d does not exist", entityID)
		}
	}

	body := params.MetadataString("body")
	if body == "" {
		return NewValidationError("comment body is empty")
	}
	if len(body) > CharacterLimitCommentBody {
		return NewValidationError("comment body exceeds %d character limit", CharacterLimitCommentBody)
	}

	// Validate parent_comment_id if provided
	if parentID, ok := params.MetadataInt64("parent_comment_id"); ok && parentID > 0 {
		pExists, err := commentExists(ctx, params.DBTX, parentID)
		if err != nil {
			return err
		}
		if !pExists {
			return NewValidationError("parent comment %d does not exist", parentID)
		}
	}

	return nil
}

// getMetadataMentions extracts up to 10 mention user IDs from metadata.
func getMetadataMentions(params *Params) ([]int64, bool) {
	if params.Metadata == nil {
		return nil, false
	}
	raw, ok := params.Metadata["mentions"]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	limit := 10
	if len(arr) < limit {
		limit = len(arr)
	}
	ids := make([]int64, 0, limit)
	for i := 0; i < limit; i++ {
		switch v := arr[i].(type) {
		case float64:
			ids = append(ids, int64(v))
		case int:
			ids = append(ids, int64(v))
		case int64:
			ids = append(ids, v)
		}
	}
	return ids, len(ids) > 0
}

func nullableInt(v int64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// CommentCreate returns the Comment Create handler.
func CommentCreate() Handler { return &commentCreateHandler{} }
