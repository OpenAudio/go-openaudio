package entity_manager

import (
	"context"
	"encoding/json"
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
	if params.MetadataString("title") == "" {
		return NewValidationError("title is required for track creation")
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
		// Normalize in place so the canonical form is what gets inserted.
		params.Metadata["genre"] = NormalizeGenre(genre)
	}
	if err := ValidateAccessConditions(params); err != nil {
		return err
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

// insertTrackAndRoute writes a newly created track. A new track is never created
// already-deleted; only the genesis migration replays a deleted row, via
// insertTrackAndRouteWithState.
func insertTrackAndRoute(ctx context.Context, params *Params) error {
	return insertTrackAndRouteWithState(ctx, params, trackState{})
}

// trackRoute is a track's route as it already exists elsewhere, carried
// verbatim instead of being recomputed.
type trackRoute struct {
	Slug        string
	TitleSlug   string
	CollisionID int
}

// trackState is the non-metadata state a track is created with. Production
// passes the zero value; only the migration replay fills it in.
type trackState struct {
	IsDelete bool

	// Route, when set, replaces slug generation. Legacy slugs are not
	// reproducible from the title: the rules changed over the catalog's life,
	// so 618,066 of 1,955,877 tracks on a production clone regenerate to a
	// different slug than they actually serve -- 330,856 of them because the
	// old scheme appended the track id, the rest over punctuation and
	// collision numbering. A slug is a permanent URL, so the migration carries
	// the real one rather than deriving a plausible one.
	Route *trackRoute
}

func insertTrackAndRouteWithState(ctx context.Context, params *Params, state trackState) error {
	// A required title is a validation rule (see validateTrackCreate), not a
	// property of writing the row: a handful of legacy tracks have an empty
	// title, and the genesis migration keeps them rather than dropping them.
	title := params.MetadataString("title")

	handle, err := getTrackOwnerHandle(ctx, params.DBTX, params.UserID)
	if err != nil {
		return err
	}
	routeID := CreateTrackRouteID(title, handle)

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
	isCustomBpm := params.MetadataBoolOr("is_custom_bpm", false)
	isCustomMusicalKey := params.MetadataBoolOr("is_custom_musical_key", false)
	audioUploadID := params.MetadataString("audio_upload_id")
	license := params.MetadataString("license")
	isrc := params.MetadataString("isrc")
	iswc := params.MetadataString("iswc")
	coverOriginalSongTitle := params.MetadataString("cover_original_song_title")
	coverOriginalArtist := params.MetadataString("cover_original_artist")
	commentsDisabled := params.MetadataBoolOr("comments_disabled", false)
	noAIUse := params.MetadataBoolOr("no_ai_use", false)

	var previewStartSeconds *float64
	if v, ok := params.MetadataFloat64("preview_start_seconds"); ok {
		previewStartSeconds = &v
	}

	// musical_key: only persist recognized keys (mirrors apps' is_valid_musical_key).
	// An invalid/empty key leaves the column NULL on create.
	musicalKey := params.MetadataString("musical_key")
	if !isValidMusicalKey(musicalKey) {
		musicalKey = ""
	}

	// bpm: any nonzero number persists; 0/absent/non-numeric leaves it NULL
	// (mirrors apps' track.py `bpm_float != 0` check).
	var bpm *float64
	if v, ok := params.MetadataFloat64("bpm"); ok && v != 0 {
		bpm = &v
	}

	fieldVisibility := metadataJSONRaw(params, "field_visibility")
	remixOf := metadataJSONRaw(params, "remix_of")
	stemOf := metadataJSONRaw(params, "stem_of")
	downloadConditions := metadataJSONRaw(params, "download_conditions")
	streamConditions := metadataJSONRaw(params, "stream_conditions")
	ddexReleaseIDs := metadataJSONRaw(params, "ddex_release_ids")

	trackCID := params.MetadataString("track_cid")
	previewCID := params.MetadataString("preview_cid")
	origFileCID := params.MetadataString("orig_file_cid")
	origFilename := params.MetadataString("orig_filename")

	duration := 0
	if d, ok := params.MetadataInt64("duration"); ok && d > 0 {
		duration = int(d)
	}

	var aiAttr *int64
	if v, ok := params.MetadataInt64("ai_attribution_user_id"); ok {
		aiAttr = &v
	}

	releaseDate := releaseDateOrDefault(params.MetadataString("release_date"), params.BlockTime)

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO tracks (
			track_id, owner_id, is_current, is_delete, title, genre, mood, tags, description,
			cover_art, cover_art_sizes, is_unlisted, field_visibility, remix_of, stem_of,
			track_cid, preview_cid, orig_file_cid, duration,
			is_downloadable, is_download_gated, download_conditions, is_stream_gated, stream_conditions,
			release_date, is_scheduled_release, ai_attribution_user_id, is_playlist_upload, ddex_app, ddex_release_ids,
			bpm, musical_key, is_custom_bpm, is_custom_musical_key, audio_upload_id,
			is_available, license, isrc, iswc, preview_start_seconds, comments_disabled,
			cover_original_song_title, cover_original_artist, no_ai_use,
			route_id, track_segments, created_at, updated_at, txhash, blocknumber,
			orig_filename
		) VALUES (
			$1, $2, true, $48, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21, $22,
			$23, $24, $25, $26, $27, $28,
			$29, $30, $31, $32, $33,
			$34, $35, $36, $37, $38, $39,
			$40, $41, $42,
			$43, '[]'::jsonb, $44, $44, $45, $46,
			$47
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
		bpm,
		nullString(musicalKey),
		isCustomBpm,
		isCustomMusicalKey,
		nullString(audioUploadID),
		isAvailable,
		nullString(license),
		nullString(isrc),
		nullString(iswc),
		previewStartSeconds,
		commentsDisabled,
		nullString(coverOriginalSongTitle),
		nullString(coverOriginalArtist),
		noAIUse,
		routeID,
		params.BlockTime,
		params.TxHash,
		params.BlockNumber,
		nullString(origFilename),
		state.IsDelete,
	)
	if err != nil {
		return err
	}

	if err := updateStemsTable(ctx, params.DBTX, params.EntityID, params.Metadata); err != nil {
		return err
	}
	if trackCreateHasRemixParents(params.Metadata) {
		if err := updateRemixesTable(ctx, params.DBTX, params.EntityID, params.Metadata); err != nil {
			return err
		}
	}
	if trackCreateHasCollaborators(params.Metadata, params.UserID) {
		if err := updateTrackCollaboratorsTable(ctx, params.DBTX, params.EntityID, params.UserID, params.Metadata, params.BlockTime, params.TxHash, params.BlockNumber); err != nil {
			return err
		}
	}
	if err := autoSubscribeToContestOnSubmission(ctx, params); err != nil {
		return err
	}
	if err := updateTrackPriceHistory(ctx, params.DBTX, params.EntityID, params.BlockNumber, params.BlockTime, params.Metadata); err != nil {
		return err
	}
	if err := applyAccessNormalization(ctx, params.DBTX, params.EntityID, params.Metadata); err != nil {
		return err
	}

	var (
		slug        string
		titleSlug   string
		collisionID int
	)
	if state.Route != nil {
		slug, titleSlug, collisionID = state.Route.Slug, state.Route.TitleSlug, state.Route.CollisionID
	} else {
		slug, titleSlug, collisionID, err = GenerateSlugAndCollisionID(ctx, params.DBTX, params.UserID, params.EntityID, title)
		if err != nil {
			return err
		}
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO track_routes (
			slug, title_slug, collision_id, owner_id, track_id, is_current,
			blockhash, blocknumber, txhash
		) VALUES (
			$1, $2, $3, $4, $5, true,
			$6, $7, $8
		)
	`, slug, titleSlug, collisionID, params.UserID, params.EntityID, params.BlockHash, params.BlockNumber, params.TxHash)
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func trackCreateHasRemixParents(metadata map[string]any) bool {
	return len(getRemixParentTrackIDs(metadata)) > 0
}

func trackCreateHasCollaborators(metadata map[string]any, ownerID int64) bool {
	return len(getCollaboratorUserIDs(metadata, ownerID)) > 0
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
