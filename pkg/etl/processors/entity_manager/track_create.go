package entity_manager

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type trackCreateHandler struct{}

func (h *trackCreateHandler) EntityType() string { return EntityTypeTrack }
func (h *trackCreateHandler) Action() string     { return ActionCreate }

func (h *trackCreateHandler) Handle(ctx context.Context, params *Params) error {
	if err := validateTrackCreate(ctx, params); err != nil {
		return err
	}
	return insertTrackAndRoute(ctx, params)
}

func validateTrackCreate(ctx context.Context, params *Params) error {
	if params.EntityType != EntityTypeTrack {
		return NewValidationError("wrong entity type %s", params.EntityType)
	}
	if params.Action != ActionCreate {
		return NewValidationError("wrong action %s", params.Action)
	}
	if params.EntityID < TrackIDOffset {
		return NewValidationError("track id %d below offset %d", params.EntityID, TrackIDOffset)
	}
	if params.Metadata == nil {
		return NewValidationError("metadata is required for track creation")
	}
	if _, ok := params.MetadataInt64("owner_id"); !ok {
		return NewValidationError("owner_id is required in metadata for track creation")
	}
	ownerID, _ := params.MetadataInt64("owner_id")
	if ownerID != params.UserID {
		return NewValidationError("metadata owner_id must match transaction user id")
	}
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
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	exists, err := trackExists(ctx, params.DBTX, params.EntityID)
	if err != nil {
		return err
	}
	if exists {
		return NewValidationError("track %d already exists", params.EntityID)
	}
	return nil
}

func insertTrackAndRoute(ctx context.Context, params *Params) error {
	title := params.MetadataString("title")
	if title == "" {
		return NewValidationError("title is required for track creation")
	}

	genre := params.MetadataString("genre")
	mood := params.MetadataString("mood")
	tags := params.MetadataString("tags")
	description := params.MetadataString("description")
	coverArt := params.MetadataString("cover_art")
	coverArtSizes := params.MetadataString("cover_art_sizes")
	isUnlisted := params.MetadataBoolOr("is_unlisted", false)
	isDownloadable := params.MetadataBoolOr("is_downloadable", false)
	isDownloadGated := params.MetadataBoolOr("is_download_gated", false)
	isStreamGated := params.MetadataBoolOr("is_stream_gated", false)
	isScheduledRelease := params.MetadataBoolOr("is_scheduled_release", false)
	isPlaylistUpload := params.MetadataBoolOr("is_playlist_upload", false)
	ddexApp := params.MetadataString("ddex_app")
	isAvailable := params.MetadataBoolOr("is_available", true)

	fieldVisibility := metadataJSONRaw(params, "field_visibility")
	remixOf := metadataJSONRaw(params, "remix_of")
	stemOf := metadataJSONRaw(params, "stem_of")
	downloadConditions := metadataJSONRaw(params, "download_conditions")
	streamConditions := metadataJSONRaw(params, "stream_conditions")
	ddexReleaseIDs := metadataJSONRaw(params, "ddex_release_ids")

	trackCID := params.MetadataString("track_cid")
	previewCID := params.MetadataString("preview_cid")
	origFileCID := params.MetadataString("orig_file_cid")

	duration := 0
	if d, ok := params.MetadataInt64("duration"); ok && d > 0 {
		duration = int(d)
	}

	var aiAttr *int64
	if v, ok := params.MetadataInt64("ai_attribution_user_id"); ok {
		aiAttr = &v
	}

	var releaseDate pgtype.Timestamp
	if rd := params.MetadataString("release_date"); rd != "" {
		if t, err := time.Parse(time.RFC3339, rd); err == nil {
			releaseDate = pgtype.Timestamp{Time: t, Valid: true}
		}
	}

	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO tracks (
			track_id, owner_id, is_current, is_delete, title, genre, mood, tags, description,
			cover_art, cover_art_sizes, is_unlisted, field_visibility, remix_of, stem_of,
			track_cid, preview_cid, orig_file_cid, duration,
			is_downloadable, is_download_gated, download_conditions, is_stream_gated, stream_conditions,
			release_date, is_scheduled_release, ai_attribution_user_id, is_playlist_upload, ddex_app, ddex_release_ids,
			is_available, track_segments, created_at, updated_at, txhash, blocknumber
		) VALUES (
			$1, $2, true, false, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21, $22,
			$23, $24, $25, $26, $27, $28,
			$29, '[]'::jsonb, $30, $30, $31, $32
		)
	`,
		params.EntityID,
		params.UserID,
		title,
		nullString(genre),
		nullString(mood),
		nullString(tags),
		nullString(description),
		nullString(coverArt),
		nullString(coverArtSizes),
		isUnlisted,
		fieldVisibility,
		remixOf,
		stemOf,
		nullString(trackCID),
		nullString(previewCID),
		nullString(origFileCID),
		duration,
		isDownloadable,
		isDownloadGated,
		downloadConditions,
		isStreamGated,
		streamConditions,
		releaseDate,
		isScheduledRelease,
		aiAttr,
		isPlaylistUpload,
		nullString(ddexApp),
		ddexReleaseIDs,
		isAvailable,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
	)
	if err != nil {
		return err
	}

	slug, titleSlug, collisionID, err := GenerateSlugAndCollisionID(ctx, params.DBTX, params.UserID, params.EntityID, title)
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

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// metadataJSONRaw returns JSON bytes for a metadata key, or nil if absent.
func metadataJSONRaw(p *Params, key string) []byte {
	v, ok := p.MetadataJSON(key)
	if !ok || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// TrackCreate returns the Track Create handler.
func TrackCreate() Handler { return &trackCreateHandler{} }
