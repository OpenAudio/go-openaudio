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

	// Insert thread relationship if this is a reply.
	if parentID, ok := getCommentParentID(params); ok {
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

// getCommentParentID returns the parent comment id from metadata, accepting
// either parent_comment_id (preferred) or parent_id (alt). Clients may send
// either; both are treated as the same field.
func getCommentParentID(params *Params) (int64, bool) {
	if id, ok := params.MetadataInt64("parent_comment_id"); ok && id > 0 {
		return id, true
	}
	if id, ok := params.MetadataInt64("parent_id"); ok && id > 0 {
		return id, true
	}
	return 0, false
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

	// Validate parent_comment_id (or parent_id) if provided.
	if parentID, ok := getCommentParentID(params); ok && isCreate {
		pExists, err := commentExists(ctx, params.DBTX, parentID)
		if err != nil {
			return err
		}
		if !pExists {
			return NewValidationError("parent comment %d does not exist", parentID)
		}

		// Parent must be on the same entity (track/fanclub) as this child.
		var parentEntityType string
		var parentEntityID int64
		err = params.DBTX.QueryRow(ctx,
			"SELECT entity_type, entity_id FROM comments WHERE comment_id = $1",
			parentID).Scan(&parentEntityType, &parentEntityID)
		if err != nil {
			return err
		}
		childEntityType := et
		if childEntityType == "" {
			childEntityType = EntityTypeTrack
		}
		if parentEntityType != childEntityType || parentEntityID != entityID {
			return NewValidationError("parent comment %d does not belong to %s %d",
				parentID, childEntityType, entityID)
		}

		// Reject duplicate (parent, child) pair.
		var dup bool
		err = params.DBTX.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM comment_threads WHERE parent_comment_id = $1 AND comment_id = $2)",
			parentID, params.EntityID).Scan(&dup)
		if err != nil {
			return err
		}
		if dup {
			return NewValidationError("comment_thread (%d, %d) already exists", parentID, params.EntityID)
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
