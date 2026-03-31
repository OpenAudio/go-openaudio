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
	ok, err := playlistExistsActive(ctx, params.DBTX, params.EntityID)
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
	base, err := loadCurrentPlaylistRow(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}

	if err := markNotCurrent(ctx, params.DBTX, "playlists", "playlist_id", params.EntityID); err != nil {
		return err
	}

	return insertPlaylistRow(ctx, params.DBTX, base, true, params.BlockTime, params.TxHash, params.BlockNumber)
}

// PlaylistDelete returns the Playlist Delete handler.
func PlaylistDelete() Handler { return &playlistDeleteHandler{} }
