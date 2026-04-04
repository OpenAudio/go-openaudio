package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// --- Save ---

type saveHandler struct{}

func (h *saveHandler) EntityType() string { return EntityTypeAny }
func (h *saveHandler) Action() string     { return ActionSave }

func (h *saveHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateSave(ctx, params); err != nil {
		return err
	}
	return insertSave(ctx, params, false)
}

func validateSave(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Use tx entity_type first (e.g. "Track", "Playlist"), then metadata, then DB inference
	saveType := saveTypeFromEntityType(params.EntityType)
	if saveType == "" {
		saveType = saveTypeFromEntityType(params.MetadataString("type"))
	}
	if saveType == "" {
		saveType = inferSaveType(ctx, params.DBTX, params.EntityID)
	}
	if saveType == "" {
		return NewValidationError("cannot determine save type for entity %d", params.EntityID)
	}
	// Check entity exists
	if err := validateSaveTarget(ctx, params.DBTX, params.EntityID, saveType); err != nil {
		return err
	}
	// Check for duplicate active save
	dup, err := saveExists(ctx, params.DBTX, params.UserID, params.EntityID, saveType)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("save already exists for user %d item %d", params.UserID, params.EntityID)
	}
	return nil
}

// --- Unsave ---

type unsaveHandler struct{}

func (h *unsaveHandler) EntityType() string { return EntityTypeAny }
func (h *unsaveHandler) Action() string     { return ActionUnsave }

func (h *unsaveHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUnsave(ctx, params); err != nil {
		return err
	}
	return insertSave(ctx, params, true)
}

func validateUnsave(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	saveType := saveTypeFromEntityType(params.EntityType)
	if saveType == "" {
		saveType = saveTypeFromEntityType(params.MetadataString("type"))
	}
	if saveType == "" {
		saveType = inferSaveType(ctx, params.DBTX, params.EntityID)
	}
	if saveType == "" {
		return NewValidationError("cannot determine save type for entity %d", params.EntityID)
	}
	dup, err := saveExists(ctx, params.DBTX, params.UserID, params.EntityID, saveType)
	if err != nil {
		return err
	}
	if !dup {
		return NewValidationError("no active save for user %d item %d", params.UserID, params.EntityID)
	}
	return nil
}

// --- shared ---

func insertSave(ctx context.Context, params *Params, isDelete bool) error {
	saveType := saveTypeFromEntityType(params.EntityType)
	if saveType == "" {
		saveType = saveTypeFromEntityType(params.MetadataString("type"))
	}
	if saveType == "" {
		saveType = inferSaveType(ctx, params.DBTX, params.EntityID)
	}
	isSaveOfRepost := params.MetadataBoolOr("is_save_of_repost", false)

	// Mark existing save rows as not current
	_, err := params.DBTX.Exec(ctx,
		"UPDATE saves SET is_current = false WHERE user_id = $1 AND save_item_id = $2 AND save_type = $3::savetype AND is_current = true",
		params.UserID, params.EntityID, saveType)
	if err != nil {
		return err
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO saves (
			user_id, save_item_id, save_type, is_current, is_delete, is_save_of_repost,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, $3::savetype, true, $4, $5, $6, $7, $8)
	`, params.UserID, params.EntityID, saveType, isDelete, isSaveOfRepost, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func saveExists(ctx context.Context, dbtx db.DBTX, userID, itemID int64, saveType string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM saves WHERE user_id = $1 AND save_item_id = $2 AND save_type = $3::savetype AND is_current = true AND is_delete = false)",
		userID, itemID, saveType).Scan(&exists)
	return exists, err
}

func saveTypeFromEntityType(entityType string) string {
	switch strings.ToLower(entityType) {
	case "track":
		return "track"
	case "playlist":
		return "playlist"
	case "album":
		return "album"
	}
	return ""
}

func inferSaveType(ctx context.Context, dbtx db.DBTX, entityID int64) string {
	var exists bool
	_ = dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tracks WHERE track_id = $1)", entityID).Scan(&exists)
	if exists {
		return "track"
	}
	_ = dbtx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM playlists WHERE playlist_id = $1)", entityID).Scan(&exists)
	if exists {
		// Check if it's an album
		var isAlbum bool
		_ = dbtx.QueryRow(ctx, "SELECT is_album FROM playlists WHERE playlist_id = $1 AND is_current = true LIMIT 1", entityID).Scan(&isAlbum)
		if isAlbum {
			return "album"
		}
		return "playlist"
	}
	return ""
}

func validateSaveTarget(ctx context.Context, dbtx db.DBTX, entityID int64, saveType string) error {
	var exists bool
	switch saveType {
	case "track":
		exists2, err := trackExists(ctx, dbtx, entityID)
		if err != nil {
			return err
		}
		exists = exists2
	case "playlist", "album":
		exists2, err := playlistExists(ctx, dbtx, entityID)
		if err != nil {
			return err
		}
		exists = exists2
	}
	if !exists {
		return NewValidationError("%s %d does not exist", saveType, entityID)
	}
	return nil
}

func Save() Handler   { return &saveHandler{} }
func Unsave() Handler { return &unsaveHandler{} }
