package entity_manager

import (
	"context"
)

type trackDeleteHandler struct{}

func (h *trackDeleteHandler) EntityType() string { return EntityTypeTrack }
func (h *trackDeleteHandler) Action() string     { return ActionDelete }

func (h *trackDeleteHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateTrackDelete(ctx, params); err != nil {
		return err
	}
	return deleteTrack(ctx, params)
}

func validateTrackDelete(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypeTrack {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Action != ActionDelete {
		return NewValidationError("wrong action %s", params.Action)
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	ok, err := trackExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if !ok {
		return NewValidationError("track %d does not exist", params.EntityID)
	}
	row, err := loadCurrentTrackRow(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if row.OwnerID != params.UserID {
		return NewValidationError("track %d does not match user", params.EntityID)
	}
	return nil
}

func deleteTrack(ctx context.Context, params *Params) error {
	_, err := params.DBTX.Exec(ctx, `
		UPDATE tracks SET
			is_delete = true, stem_of = NULL,
			updated_at = $2, txhash = $3, blocknumber = $4
		WHERE track_id = $1 AND is_current = true
	`, params.EntityID, params.BlockTime, params.TxHash, params.BlockNumber)
	return err
}

// TrackDelete returns the Track Delete handler.
func TrackDelete() Handler { return &trackDeleteHandler{} }
