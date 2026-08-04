package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// --- Repost ---

type repostHandler struct{}

func (h *repostHandler) EntityType() string { return EntityTypeAny }
func (h *repostHandler) Action() string     { return ActionRepost }

func (h *repostHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateRepost(ctx, params); err != nil {
		return err
	}
	return insertRepost(ctx, params, false)
}

func validateRepost(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	repostType := resolveRepostType(ctx, params)
	if repostType == "" {
		return NewValidationError("cannot determine repost type for entity %d", params.EntityID)
	}
	// Check entity exists
	if err := validateRepostTarget(ctx, params.DBTX, params.EntityID, repostType); err != nil {
		return err
	}
	// Check for duplicate active repost
	dup, err := repostExists(ctx, params.DBTX, params.UserID, params.EntityID, repostType)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("repost already exists for user %d item %d", params.UserID, params.EntityID)
	}
	return nil
}

// --- Unrepost ---

type unrepostHandler struct{}

func (h *unrepostHandler) EntityType() string { return EntityTypeAny }
func (h *unrepostHandler) Action() string     { return ActionUnrepost }

func (h *unrepostHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateUnrepost(ctx, params); err != nil {
		return err
	}
	return insertRepost(ctx, params, true)
}

func validateUnrepost(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	repostType := resolveRepostType(ctx, params)
	if repostType == "" {
		return NewValidationError("cannot determine repost type for entity %d", params.EntityID)
	}
	dup, err := repostExists(ctx, params.DBTX, params.UserID, params.EntityID, repostType)
	if err != nil {
		return err
	}
	if !dup {
		return NewValidationError("no active repost for user %d item %d", params.UserID, params.EntityID)
	}
	return nil
}

// --- shared ---

func insertRepost(ctx context.Context, params *Params, isDelete bool) error {
	repostType := resolveRepostType(ctx, params)
	isRepostOfRepost := params.MetadataBoolOr("is_repost_of_repost", false)

	// Upsert the single current row in place (arbiter: reposts_current_uniq_idx).
	// Replaces demote-then-insert: avoids unbounded is_current=false history and
	// gives the aggregate triggers an O(1) is_delete transition to track.
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO reposts (
			user_id, repost_item_id, repost_type, is_current, is_delete, is_repost_of_repost,
			created_at, txhash, blocknumber
		) VALUES ($1, $2, $3::reposttype, true, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id, repost_item_id, repost_type) WHERE is_current = true
		DO UPDATE SET
			is_delete = EXCLUDED.is_delete,
			is_repost_of_repost = EXCLUDED.is_repost_of_repost,
			created_at = EXCLUDED.created_at,
			txhash = EXCLUDED.txhash,
			blocknumber = EXCLUDED.blocknumber
	`, params.UserID, params.EntityID, repostType, isDelete, isRepostOfRepost, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func repostExists(ctx context.Context, dbtx db.DBTX, userID, itemID int64, repostType string) (bool, error) {
	var exists bool
	err := dbtx.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM reposts WHERE user_id = $1 AND repost_item_id = $2 AND repost_type = $3::reposttype AND is_current = true AND is_delete = false)",
		userID, itemID, repostType).Scan(&exists)
	return exists, err
}

// resolveRepostType — same priority as resolveSaveType (metadata.type wins,
// chain entity_type next, DB inference last), and albums likewise record as
// "playlist". See social_save.go:resolveSaveType for full rationale.
// Crucially: when entity_type is "Playlist" we never cross over to "track"
// even if a same-id track exists.
func resolveRepostType(ctx context.Context, params *Params) string {
	if t := repostTypeFromEntityType(params.MetadataString("type")); t != "" {
		return t
	}
	switch repostTypeFromEntityType(params.EntityType) {
	case "track":
		return "track"
	case "playlist":
		return "playlist"
	}
	return inferRepostType(ctx, params.DBTX, params.EntityID)
}

func repostTypeFromEntityType(entityType string) string {
	switch strings.ToLower(entityType) {
	case "track":
		return "track"
	case "playlist", "album":
		return "playlist"
	}
	return ""
}

func inferRepostType(ctx context.Context, dbtx db.DBTX, entityID int64) string {
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

func validateRepostTarget(ctx context.Context, dbtx db.DBTX, entityID int64, repostType string) error {
	var exists bool
	switch repostType {
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
		return NewValidationError("%s %d does not exist", repostType, entityID)
	}
	return nil
}

func Repost() Handler   { return &repostHandler{} }
func Unrepost() Handler { return &unrepostHandler{} }
