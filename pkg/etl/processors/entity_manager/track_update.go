package entity_manager

import (
	"context"
)

type trackUpdateHandler struct{}

func (h *trackUpdateHandler) EntityType() string { return EntityTypeTrack }
func (h *trackUpdateHandler) Action() string     { return ActionUpdate }

func (h *trackUpdateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateTrackUpdate(ctx, params); err != nil {
		return err
	}
	return updateTrack(ctx, params)
}

func validateTrackUpdate(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypeTrack {
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
		if genre := params.MetadataString("genre"); genre != "" {
			if err := ValidateGenre(genre); err != nil {
				return err
			}
		}
	}
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	ok, err := trackExistsActive(ctx, params.DBTX, params.EntityID)
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

func updateTrack(ctx context.Context, params *Params) error {
	base, err := loadCurrentTrackRow(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	oldTitle := base.Title
	merged := mergeTrackFromMetadata(params, base)

	if err := markNotCurrent(ctx, params.DBTX, "tracks", "track_id", params.EntityID); err != nil {
		return err
	}

	if err := insertTrackRow(ctx, params.DBTX, merged, params.BlockTime, params.TxHash, params.BlockNumber); err != nil {
		return err
	}

	if params.MetadataString("title") != "" && merged.Title != oldTitle {
		_, err = params.DBTX.Exec(ctx, `
			UPDATE track_routes SET is_current = false WHERE track_id = $1 AND is_current = true
		`, params.EntityID)
		if err != nil {
			return err
		}
		slug, titleSlug, collisionID, err := GenerateSlugAndCollisionID(ctx, params.DBTX, params.UserID, params.EntityID, merged.Title)
		if err != nil {
			return err
		}
		_, err = params.DBTX.Exec(ctx, `
			INSERT INTO track_routes (
				slug, title_slug, collision_id, owner_id, track_id, is_current,
				blockhash, blocknumber, txhash
			) VALUES (
				$1, $2, $3, $4, $5, true,
				'', $6, $7
			)
		`, slug, titleSlug, collisionID, params.UserID, params.EntityID, params.BlockNumber, params.TxHash)
		return err
	}
	return nil
}

// TrackUpdate returns the Track Update handler.
func TrackUpdate() Handler { return &trackUpdateHandler{} }
