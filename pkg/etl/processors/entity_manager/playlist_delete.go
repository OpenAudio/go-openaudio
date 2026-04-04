package entity_manager

import (
	"context"
)

type playlistDeleteHandler struct{}

func (h *playlistDeleteHandler) EntityType() string { return EntityTypePlaylist }
func (h *playlistDeleteHandler) Action() string     { return ActionDelete }

func (h *playlistDeleteHandler) Handle(ctx context.Context, params *Params) error {
	if err := validatePlaylistDelete(ctx, params); err != nil {
		return err
	}
	return deletePlaylist(ctx, params)
}

func validatePlaylistDelete(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypePlaylist {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Action != ActionDelete {
		return NewValidationError("wrong action %s", params.Action)
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

func deletePlaylist(ctx context.Context, params *Params) error {
	_, err := params.DBTX.Exec(ctx, `
		UPDATE playlists SET
			is_delete = true,
			updated_at = $2, txhash = $3, blocknumber = $4
		WHERE playlist_id = $1 AND is_current = true
	`, params.EntityID, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

// PlaylistDelete returns the Playlist Delete handler.
func PlaylistDelete() Handler { return &playlistDeleteHandler{} }
