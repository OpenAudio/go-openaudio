package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
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
	saveType := resolveSaveType(ctx, params)
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
	saveType := resolveSaveType(ctx, params)
	isSaveOfRepost := params.MetadataBoolOr("is_save_of_repost", false)

	// Upsert the single current row in place (arbiter: saves_current_uniq_idx).
	// Replaces demote-then-insert: avoids unbounded is_current=false history and
	// gives the aggregate triggers an O(1) is_delete transition to track.
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO saves (
			user_id, save_item_id, save_type, is_current, is_delete, is_save_of_repost,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, $3::savetype, true, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id, save_item_id, save_type) WHERE is_current = true
		DO UPDATE SET
			is_delete = EXCLUDED.is_delete,
			is_save_of_repost = EXCLUDED.is_save_of_repost,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
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

// resolveSaveType determines the save_type for the saves row.
//
// Priority:
//  1. Metadata `type` if explicitly set ("track" / "playlist" / "album" —
//     "album" is accepted as input but records as "playlist", see below).
//  2. Chain entity_type — the chain only distinguishes "Track" vs "Playlist".
//     We deliberately do NOT fall back to track inference here: the chain said
//     Playlist, so a same-id track is unrelated.
//  3. Pure DB inference when neither metadata nor entity_type tells us.
//
// The "do not cross over to track when entity_type is Playlist" rule
// matters: track_id and playlist_id namespaces can collide, and treating a
// Playlist save as a Track save (via inferSaveType, which checks tracks
// first) writes the wrong row — observed in production.
//
// Albums resolve to "playlist", not "album". An album is a playlist with
// is_album = true, and that flag is mutable, whereas save_type is written
// once and is part of the saves primary key — deriving it from is_album made
// the same chain history index differently depending on when it was replayed.
// Nothing reads the distinction (every consumer is track/not-track, or ORs
// the two together), and callers that want it should read playlists.is_album
// at query time, as the notification triggers already do.
func resolveSaveType(ctx context.Context, params *Params) string {
	if t := saveTypeFromEntityType(params.MetadataString("type")); t != "" {
		return t
	}
	if t := saveTypeFromEntityType(params.EntityType); t != "" {
		return t
	}
	return inferSaveType(ctx, params.DBTX, params.EntityID)
}

func saveTypeFromEntityType(entityType string) string {
	switch strings.ToLower(entityType) {
	case "track":
		return "track"
	case "playlist", "album":
		return "playlist"
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
		// Albums are playlists with is_album = true; both record as "playlist".
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
