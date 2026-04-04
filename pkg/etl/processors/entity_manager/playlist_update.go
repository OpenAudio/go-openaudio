package entity_manager

import (
	"context"
)

type playlistUpdateHandler struct{}

func (h *playlistUpdateHandler) EntityType() string { return EntityTypePlaylist }
func (h *playlistUpdateHandler) Action() string     { return ActionUpdate }

func (h *playlistUpdateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validatePlaylistUpdate(ctx, params); err != nil {
		return err
	}
	return updatePlaylist(ctx, params)
}

func validatePlaylistUpdate(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypePlaylist {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Action != ActionUpdate {
		return NewValidationError("wrong action %s", params.Action)
	}
	if params.Metadata != nil {
		if desc := params.MetadataString("description"); desc != "" {
			if err := ValidateDescription(desc); err != nil {
				return err
			}
		}
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	ok, err := playlistExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !ok {
		return NewValidationError("playlist %d does not exist", params.EntityID)
	}
	row, err := loadCurrentPlaylistRow(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if row.PlaylistOwnerID != params.UserID {
		return NewValidationError("playlist %d does not match user", params.EntityID)
	}
	return nil
}

func updatePlaylist(ctx context.Context, params *Params) error {
	base, err := loadCurrentPlaylistRow(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	oldName := ""
	if base.PlaylistName != nil {
		oldName = *base.PlaylistName
	}
	merged := mergePlaylistFromMetadata(params, base)

	if err := updatePlaylistRow(ctx, params.DBTX, merged, params.BlockTime, params.TxHash, params.BlockNumber); err != nil {
		return err
	}

	// Update playlist route if name changed
	newName := ""
	if merged.PlaylistName != nil {
		newName = *merged.PlaylistName
	}
	if params.MetadataString("playlist_name") != "" && newName != oldName {
		_, err = params.DBTX.Exec(ctx, `
			UPDATE playlist_routes SET is_current = false WHERE playlist_id = $1 AND is_current = true
		`, params.EntityID)
		if err != nil {
			return err
		}
		slug, titleSlug, collisionID, err := GeneratePlaylistSlugAndCollisionID(ctx, params.DBTX, params.UserID, params.EntityID, newName)
		if err != nil {
			return err
		}
		_, err = params.DBTX.Exec(ctx, `
			INSERT INTO playlist_routes (
				slug, title_slug, collision_id, owner_id, playlist_id, is_current,
				blockhash, blocknumber, txhash
			) VALUES (
				$1, $2, $3, $4, $5, true,
				$6, $7, $8
			)
		`, slug, titleSlug, collisionID, params.UserID, params.EntityID, params.BlockHash, params.BlockNumber, params.TxHash)
		return err
	}
	return nil
}

// PlaylistUpdate returns the Playlist Update handler.
func PlaylistUpdate() Handler { return &playlistUpdateHandler{} }
