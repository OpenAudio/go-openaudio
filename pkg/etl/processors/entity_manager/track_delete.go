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

func deleteTrack(ctx context.Context, params *Params) error {
	base, err := loadCurrentTrackRow(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}

	if err := markNotCurrent(ctx, params.DBTX, "tracks", "track_id", params.EntityID); err != nil {
		return err
	}

	base.StemOf = nil
	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO tracks (
			track_id, owner_id, is_current, is_delete, title, genre, mood, tags, description,
			cover_art, cover_art_sizes, is_unlisted, field_visibility, remix_of, stem_of,
			track_cid, preview_cid, orig_file_cid, duration,
			is_downloadable, is_download_gated, download_conditions, is_stream_gated, stream_conditions,
			release_date, is_scheduled_release, ai_attribution_user_id, is_playlist_upload, ddex_app, ddex_release_ids,
			is_available, track_segments, created_at, updated_at, txhash, blocknumber
		) VALUES (
			$1, $2, true, true, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21, $22,
			$23, $24, $25, $26, $27, $28,
			$29, $30, $31, $32, $33, $34
		)
	`,
		base.TrackID,
		base.OwnerID,
		base.Title,
		strPtrVal(base.Genre),
		strPtrVal(base.Mood),
		strPtrVal(base.Tags),
		strPtrVal(base.Description),
		strPtrVal(base.CoverArt),
		strPtrVal(base.CoverArtSizes),
		base.IsUnlisted,
		base.FieldVisibility,
		base.RemixOf,
		base.StemOf,
		strPtrVal(base.TrackCID),
		strPtrVal(base.PreviewCID),
		strPtrVal(base.OrigFileCID),
		base.Duration,
		base.IsDownloadable,
		base.IsDownloadGated,
		base.DownloadConditions,
		base.IsStreamGated,
		base.StreamConditions,
		base.ReleaseDate,
		base.IsScheduledRelease,
		base.AiAttributionUserID,
		base.IsPlaylistUpload,
		strPtrVal(base.DdexApp),
		base.DdexReleaseIDs,
		base.IsAvailable,
		base.TrackSegments,
		base.CreatedAt,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	return err
}

// TrackDelete returns the Track Delete handler.
func TrackDelete() Handler { return &trackDeleteHandler{} }
