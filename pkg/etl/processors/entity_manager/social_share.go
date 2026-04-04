package entity_manager

import (
	"context"
	"strings"
)

type shareHandler struct{}

func (h *shareHandler) EntityType() string { return EntityTypeAny }
func (h *shareHandler) Action() string     { return ActionShare }

func (h *shareHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}

	shareType := shareTypeFromEntityType(params.EntityType)
	if shareType == "" {
		shareType = shareTypeFromEntityType(params.MetadataString("type"))
	}
	if shareType == "" {
		return NewValidationError("cannot determine share type for entity %d", params.EntityID)
	}

	if err := validateShareTarget(ctx, params, shareType); err != nil {
		return err
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO shares (
			user_id, share_item_id, share_type, created_at, txhash, blocknumber
		) VALUES ($1, $2, $3::sharetype, $4, $5, $6)
	`, params.UserID, params.EntityID, shareType, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

func shareTypeFromEntityType(entityType string) string {
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

func validateShareTarget(ctx context.Context, params *Params, shareType string) error {
	var exists bool
	switch shareType {
	case "track":
		e, err := trackExists(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		exists = e
	case "playlist", "album":
		e, err := playlistExists(ctx, params.DBTX, params.EntityID)
		if err != nil {
			return err
		}
		exists = e
	}
	if !exists {
		return NewValidationError("%s %d does not exist", shareType, params.EntityID)
	}
	return nil
}

func Share() Handler { return &shareHandler{} }
