package entity_manager

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// trackRow holds columns we read/write for track create/update parity.
type trackRow struct {
	TrackID                int64
	OwnerID                int64
	Title                  string
	Genre                  *string
	Mood                   *string
	Tags                   *string
	Description            *string
	CoverArt               *string
	CoverArtSizes          *string
	IsUnlisted             bool
	FieldVisibility        []byte
	RemixOf                []byte
	StemOf                 []byte
	TrackCID               *string
	PreviewCID             *string
	OrigFileCID            *string
	OrigFilename           *string
	Duration               int
	IsDownloadable         bool
	IsDownloadGated        bool
	DownloadConditions     []byte
	IsStreamGated          bool
	StreamConditions       []byte
	ReleaseDate            pgtype.Timestamp
	IsScheduledRelease     bool
	AiAttributionUserID    *int64
	IsPlaylistUpload       bool
	DdexApp                *string
	DdexReleaseIDs         []byte
	Bpm                    *float64
	MusicalKey             *string
	IsCustomBpm            bool
	IsCustomMusicalKey     bool
	AudioUploadID          *string
	IsAvailable            bool
	License                *string
	ISRC                   *string
	ISWC                   *string
	PreviewStartSeconds    *float64
	CommentsDisabled       bool
	CoverOriginalSongTitle *string
	CoverOriginalArtist    *string
	NoAIUse                bool
	TrackSegments          []byte
	CreatedAt              time.Time
}

func loadCurrentTrackRow(ctx context.Context, dbtx db.DBTX, trackID int64) (*trackRow, error) {
	var r trackRow
	var title, genre, mood, tags, desc, cover, coverSz, tcid, pcid, ofcid, ofname, ddex, musicalKey, audioUploadID sql.NullString
	var license, isrc, iswc, coverOrigSong, coverOrigArtist sql.NullString
	var fieldVis, remix, stem, dlCond, streamCond, ddexRel, segments []byte
	var aiAttr sql.NullInt64
	var bpm, previewStart sql.NullFloat64
	var releaseDate pgtype.Timestamp

	err := dbtx.QueryRow(ctx, `
		SELECT track_id, owner_id, title, genre, mood, tags, description,
			cover_art, cover_art_sizes, is_unlisted, field_visibility, remix_of, stem_of,
			track_cid, preview_cid, orig_file_cid, orig_filename, duration,
			is_downloadable, is_download_gated, download_conditions, is_stream_gated, stream_conditions,
			release_date, is_scheduled_release, ai_attribution_user_id, is_playlist_upload, ddex_app, ddex_release_ids,
			bpm, musical_key, is_custom_bpm, is_custom_musical_key, audio_upload_id,
			is_available, license, isrc, iswc, preview_start_seconds, comments_disabled,
			cover_original_song_title, cover_original_artist, no_ai_use, track_segments, created_at
		FROM tracks WHERE track_id = $1 AND is_current = true AND is_delete = false LIMIT 1
	`, trackID).Scan(
		&r.TrackID, &r.OwnerID, &title, &genre, &mood, &tags, &desc,
		&cover, &coverSz, &r.IsUnlisted, &fieldVis, &remix, &stem,
		&tcid, &pcid, &ofcid, &ofname, &r.Duration,
		&r.IsDownloadable, &r.IsDownloadGated, &dlCond, &r.IsStreamGated, &streamCond,
		&releaseDate, &r.IsScheduledRelease, &aiAttr, &r.IsPlaylistUpload, &ddex, &ddexRel,
		&bpm, &musicalKey, &r.IsCustomBpm, &r.IsCustomMusicalKey, &audioUploadID,
		&r.IsAvailable, &license, &isrc, &iswc, &previewStart, &r.CommentsDisabled,
		&coverOrigSong, &coverOrigArtist, &r.NoAIUse, &segments, &r.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Track was soft-deleted between validation and now (validateTrackUpdate
		// uses trackExists which accepts is_delete=true; this function filters
		// is_delete=false). Convert raw ErrNoRows to a ValidationError so the
		// dispatcher logs it at WARN, not ERROR.
		return nil, NewValidationError("track %d is deleted or does not exist", trackID)
	}
	if err != nil {
		return nil, err
	}
	if title.Valid {
		r.Title = title.String
	}
	r.Genre = nullStrPtr(genre)
	r.Mood = nullStrPtr(mood)
	r.Tags = nullStrPtr(tags)
	r.Description = nullStrPtr(desc)
	r.CoverArt = nullStrPtr(cover)
	r.CoverArtSizes = nullStrPtr(coverSz)
	r.FieldVisibility = fieldVis
	r.RemixOf = remix
	r.StemOf = stem
	r.TrackCID = nullStrPtr(tcid)
	r.PreviewCID = nullStrPtr(pcid)
	r.OrigFileCID = nullStrPtr(ofcid)
	r.OrigFilename = nullStrPtr(ofname)
	r.DownloadConditions = dlCond
	r.StreamConditions = streamCond
	r.ReleaseDate = releaseDate
	r.AiAttributionUserID = nullInt64Ptr(aiAttr)
	r.DdexApp = nullStrPtr(ddex)
	r.DdexReleaseIDs = ddexRel
	r.Bpm = nullFloat64Ptr(bpm)
	r.MusicalKey = nullStrPtr(musicalKey)
	r.AudioUploadID = nullStrPtr(audioUploadID)
	r.License = nullStrPtr(license)
	r.ISRC = nullStrPtr(isrc)
	r.ISWC = nullStrPtr(iswc)
	r.PreviewStartSeconds = nullFloat64Ptr(previewStart)
	r.CoverOriginalSongTitle = nullStrPtr(coverOrigSong)
	r.CoverOriginalArtist = nullStrPtr(coverOrigArtist)
	r.TrackSegments = segments
	if len(r.TrackSegments) == 0 {
		r.TrackSegments = []byte("[]")
	}
	return &r, nil
}

func nullStrPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

func nullInt64Ptr(ni sql.NullInt64) *int64 {
	if !ni.Valid {
		return nil
	}
	v := ni.Int64
	return &v
}

func nullFloat64Ptr(nf sql.NullFloat64) *float64 {
	if !nf.Valid {
		return nil
	}
	v := nf.Float64
	return &v
}

// mergeTrackFromMetadata applies metadata onto a copy of row for an update.
func mergeTrackFromMetadata(p *Params, base *trackRow) *trackRow {
	out := *base
	if t := p.MetadataString("title"); t != "" {
		out.Title = t
	}
	mergeStrPtr := func(metaKey string, cur **string) {
		if _, ok := p.Metadata[metaKey]; !ok {
			return
		}
		s := p.MetadataString(metaKey)
		if s == "" {
			*cur = nil
		} else {
			cp := s
			*cur = &cp
		}
	}
	// mergeCidPtr is like mergeStrPtr but never clears an existing value.
	// Content identifiers are set by the upload flow and no edit flow
	// legitimately removes them, but clients send the full track object on
	// update and can carry an explicit "track_cid": null. Letting that null
	// through wipes the CID and makes the track unstreamable even though the
	// audio still exists on content nodes (and every cidstream URL the API
	// then signs has cid="", which no node can serve).
	mergeCidPtr := func(metaKey string, cur **string) {
		if s := p.MetadataString(metaKey); s != "" {
			cp := s
			*cur = &cp
		}
	}
	mergeStrPtr("genre", &out.Genre)
	mergeStrPtr("mood", &out.Mood)
	mergeStrPtr("tags", &out.Tags)
	mergeStrPtr("description", &out.Description)
	mergeStrPtr("cover_art", &out.CoverArt)
	mergeStrPtr("cover_art_sizes", &out.CoverArtSizes)
	mergeCidPtr("track_cid", &out.TrackCID)
	// preview_cid stays clearable: removing a track preview is a legitimate
	// edit (paired with preview_start_seconds = null below).
	mergeStrPtr("preview_cid", &out.PreviewCID)
	mergeCidPtr("orig_file_cid", &out.OrigFileCID)
	// orig_filename gets the same never-clear treatment as the CIDs: only the
	// upload flow sets it, and clients that resend the full track object on
	// edit would otherwise null it out.
	mergeCidPtr("orig_filename", &out.OrigFilename)
	mergeStrPtr("ddex_app", &out.DdexApp)
	// audio_upload_id is a generic passthrough field in apps' indexer (set via
	// the catch-all setattr branch), needed by the audio-analysis repair job.
	// It is also the recovery key for restoring wiped CIDs from content-node
	// upload records, so it gets the same never-clear treatment.
	mergeCidPtr("audio_upload_id", &out.AudioUploadID)
	mergeStrPtr("license", &out.License)
	mergeStrPtr("isrc", &out.ISRC)
	mergeStrPtr("iswc", &out.ISWC)
	mergeStrPtr("cover_original_song_title", &out.CoverOriginalSongTitle)
	mergeStrPtr("cover_original_artist", &out.CoverOriginalArtist)

	if raw, ok := p.Metadata["preview_start_seconds"]; ok {
		if raw == nil {
			out.PreviewStartSeconds = nil
		} else if v, ok2 := p.MetadataFloat64("preview_start_seconds"); ok2 {
			cp := v
			out.PreviewStartSeconds = &cp
		}
	}
	if v, ok := p.MetadataBool("comments_disabled"); ok {
		out.CommentsDisabled = v
	}
	if v, ok := p.MetadataBool("no_ai_use"); ok {
		out.NoAIUse = v
	}

	// bpm semantics mirror apps' track.py: explicit null clears; a nonzero
	// number sets; zero or a non-numeric value is ignored (existing kept).
	if raw, ok := p.Metadata["bpm"]; ok {
		if raw == nil {
			out.Bpm = nil
		} else if v, ok2 := p.MetadataFloat64("bpm"); ok2 && v != 0 {
			cp := v
			out.Bpm = &cp
		}
	}

	// musical_key semantics mirror apps' track.py: explicit null clears; a
	// recognized key sets; an invalid value is ignored (existing kept).
	if raw, ok := p.Metadata["musical_key"]; ok {
		if raw == nil {
			out.MusicalKey = nil
		} else if s, ok2 := raw.(string); ok2 && isValidMusicalKey(s) {
			cp := s
			out.MusicalKey = &cp
		}
	}

	if v, ok := p.MetadataBool("is_custom_bpm"); ok {
		out.IsCustomBpm = v
	}
	if v, ok := p.MetadataBool("is_custom_musical_key"); ok {
		out.IsCustomMusicalKey = v
	}

	if v, ok := p.MetadataBool("is_unlisted"); ok {
		out.IsUnlisted = v
	}
	if v, ok := p.MetadataBool("is_downloadable"); ok {
		out.IsDownloadable = v
	}
	if v, ok := p.MetadataBool("is_download_gated"); ok {
		out.IsDownloadGated = v
	}
	if v, ok := p.MetadataBool("is_stream_gated"); ok {
		out.IsStreamGated = v
	}
	if v, ok := p.MetadataBool("is_scheduled_release"); ok {
		out.IsScheduledRelease = v
	}
	if v, ok := p.MetadataBool("is_playlist_upload"); ok {
		out.IsPlaylistUpload = v
	}
	if v, ok := p.MetadataBool("is_available"); ok {
		out.IsAvailable = v
	}
	if v, ok := p.MetadataInt64("duration"); ok && v > 0 {
		d := int(v)
		out.Duration = d
	}
	if v, ok := p.MetadataInt64("ai_attribution_user_id"); ok {
		out.AiAttributionUserID = &v
	}
	if v, ok := p.MetadataJSON("field_visibility"); ok {
		out.FieldVisibility = marshalJSONOrNil(v)
	}
	if v, ok := p.MetadataJSON("remix_of"); ok {
		out.RemixOf = marshalJSONOrNil(v)
	}
	// stem_of is never cleared by an update: no edit flow legitimately
	// detaches a stem from its parent (removing a stem means deleting the
	// stem track), but clients send the full track object on update and can
	// carry an explicit "stem_of": null — letting that through permanently
	// unlinks the stem (same failure class as the CID wipe fixed in #410).
	if v, ok := p.MetadataJSON("stem_of"); ok && v != nil {
		out.StemOf = marshalJSONOrNil(v)
	}
	if v, ok := p.MetadataJSON("download_conditions"); ok {
		out.DownloadConditions = marshalJSONOrNil(v)
	}
	if v, ok := p.MetadataJSON("stream_conditions"); ok {
		out.StreamConditions = marshalJSONOrNil(v)
	}
	if v, ok := p.MetadataJSON("ddex_release_ids"); ok {
		out.DdexReleaseIDs = marshalJSONOrNil(v)
	}
	if rd, ok := parseReleaseDate(p.MetadataString("release_date")); ok {
		out.ReleaseDate = rd
	}
	return &out
}

func marshalJSONOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func strPtrVal(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtrVal(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func updateTrackRow(ctx context.Context, dbtx db.DBTX, r *trackRow, blockTime time.Time, txHash string, blockNumber int64) error {
	_, err := dbtx.Exec(ctx, `
		UPDATE tracks SET
			title = $2, genre = $3, mood = $4, tags = $5, description = $6,
			cover_art = $7, cover_art_sizes = $8, is_unlisted = $9, field_visibility = $10,
			remix_of = $11, stem_of = $12, track_cid = $13, preview_cid = $14, orig_file_cid = $15,
			orig_filename = $45,
			duration = $16, is_downloadable = $17, is_download_gated = $18, download_conditions = $19,
			is_stream_gated = $20, stream_conditions = $21, release_date = $22, is_scheduled_release = $23,
			ai_attribution_user_id = $24, is_playlist_upload = $25, ddex_app = $26, ddex_release_ids = $27,
			bpm = $28, musical_key = $29, is_custom_bpm = $30, is_custom_musical_key = $31, audio_upload_id = $32,
			is_available = $33,
			license = $34, isrc = $35, iswc = $36, preview_start_seconds = $37, comments_disabled = $38,
			cover_original_song_title = $39, cover_original_artist = $40, no_ai_use = $41,
			updated_at = $42, txhash = $43, blocknumber = $44
		WHERE track_id = $1 AND is_current = true
	`,
		r.TrackID,
		r.Title,
		strPtrVal(r.Genre),
		strPtrVal(r.Mood),
		strPtrVal(r.Tags),
		strPtrVal(r.Description),
		strPtrVal(r.CoverArt),
		strPtrVal(r.CoverArtSizes),
		r.IsUnlisted,
		r.FieldVisibility,
		r.RemixOf,
		r.StemOf,
		strPtrVal(r.TrackCID),
		strPtrVal(r.PreviewCID),
		strPtrVal(r.OrigFileCID),
		r.Duration,
		r.IsDownloadable,
		r.IsDownloadGated,
		r.DownloadConditions,
		r.IsStreamGated,
		r.StreamConditions,
		r.ReleaseDate,
		r.IsScheduledRelease,
		r.AiAttributionUserID,
		r.IsPlaylistUpload,
		strPtrVal(r.DdexApp),
		r.DdexReleaseIDs,
		floatPtrVal(r.Bpm),
		strPtrVal(r.MusicalKey),
		r.IsCustomBpm,
		r.IsCustomMusicalKey,
		strPtrVal(r.AudioUploadID),
		r.IsAvailable,
		strPtrVal(r.License),
		strPtrVal(r.ISRC),
		strPtrVal(r.ISWC),
		floatPtrVal(r.PreviewStartSeconds),
		r.CommentsDisabled,
		strPtrVal(r.CoverOriginalSongTitle),
		strPtrVal(r.CoverOriginalArtist),
		r.NoAIUse,
		blockTime,
		txHash,
		blockNumber,
		strPtrVal(r.OrigFilename),
	)
	return err
}

func insertTrackRow(ctx context.Context, dbtx db.DBTX, r *trackRow, blockTime time.Time, txHash string, blockNumber int64) error {
	_, err := dbtx.Exec(ctx, `
		INSERT INTO tracks (
			track_id, owner_id, is_current, is_delete, title, genre, mood, tags, description,
			cover_art, cover_art_sizes, is_unlisted, field_visibility, remix_of, stem_of,
			track_cid, preview_cid, orig_file_cid, duration,
			is_downloadable, is_download_gated, download_conditions, is_stream_gated, stream_conditions,
			release_date, is_scheduled_release, ai_attribution_user_id, is_playlist_upload, ddex_app, ddex_release_ids,
			bpm, musical_key, is_custom_bpm, is_custom_musical_key, audio_upload_id,
			is_available, license, isrc, iswc, preview_start_seconds, comments_disabled,
			cover_original_song_title, cover_original_artist, no_ai_use,
			track_segments, created_at, updated_at, txhash, blocknumber, orig_filename
		) VALUES (
			$1, $2, true, false, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21, $22,
			$23, $24, $25, $26, $27, $28,
			$29, $30, $31, $32, $33,
			$34, $35, $36, $37, $38, $39,
			$40, $41, $42,
			$43, $44, $45, $46, $47, $48
		)
	`,
		r.TrackID,
		r.OwnerID,
		r.Title,
		strPtrVal(r.Genre),
		strPtrVal(r.Mood),
		strPtrVal(r.Tags),
		strPtrVal(r.Description),
		strPtrVal(r.CoverArt),
		strPtrVal(r.CoverArtSizes),
		r.IsUnlisted,
		r.FieldVisibility,
		r.RemixOf,
		r.StemOf,
		strPtrVal(r.TrackCID),
		strPtrVal(r.PreviewCID),
		strPtrVal(r.OrigFileCID),
		r.Duration,
		r.IsDownloadable,
		r.IsDownloadGated,
		r.DownloadConditions,
		r.IsStreamGated,
		r.StreamConditions,
		r.ReleaseDate,
		r.IsScheduledRelease,
		r.AiAttributionUserID,
		r.IsPlaylistUpload,
		strPtrVal(r.DdexApp),
		r.DdexReleaseIDs,
		floatPtrVal(r.Bpm),
		strPtrVal(r.MusicalKey),
		r.IsCustomBpm,
		r.IsCustomMusicalKey,
		strPtrVal(r.AudioUploadID),
		r.IsAvailable,
		strPtrVal(r.License),
		strPtrVal(r.ISRC),
		strPtrVal(r.ISWC),
		floatPtrVal(r.PreviewStartSeconds),
		r.CommentsDisabled,
		strPtrVal(r.CoverOriginalSongTitle),
		strPtrVal(r.CoverOriginalArtist),
		r.NoAIUse,
		r.TrackSegments,
		r.CreatedAt,
		blockTime,
		txHash,
		blockNumber,
		strPtrVal(r.OrigFilename),
	)
	return err
}
