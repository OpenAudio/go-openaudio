package entity_manager

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// trackRow holds columns we read/write for track create/update parity.
type trackRow struct {
	TrackID              int64
	OwnerID              int64
	Title                string
	Genre                *string
	Mood                 *string
	Tags                 *string
	Description          *string
	CoverArt             *string
	CoverArtSizes        *string
	IsUnlisted           bool
	FieldVisibility      []byte
	RemixOf              []byte
	StemOf               []byte
	TrackCID             *string
	PreviewCID           *string
	OrigFileCID          *string
	Duration             int
	IsDownloadable       bool
	IsDownloadGated      bool
	DownloadConditions   []byte
	IsStreamGated        bool
	StreamConditions     []byte
	ReleaseDate          pgtype.Timestamp
	IsScheduledRelease   bool
	AiAttributionUserID  *int64
	IsPlaylistUpload     bool
	DdexApp              *string
	DdexReleaseIDs       []byte
	IsAvailable          bool
	TrackSegments        []byte
	CreatedAt            time.Time
}

func loadCurrentTrackRow(ctx context.Context, dbtx db.DBTX, trackID int64) (*trackRow, error) {
	var r trackRow
	var title, genre, mood, tags, desc, cover, coverSz, tcid, pcid, ofcid, ddex sql.NullString
	var fieldVis, remix, stem, dlCond, streamCond, ddexRel, segments []byte
	var aiAttr sql.NullInt64
	var releaseDate pgtype.Timestamp

	err := dbtx.QueryRow(ctx, `
		SELECT track_id, owner_id, title, genre, mood, tags, description,
			cover_art, cover_art_sizes, is_unlisted, field_visibility, remix_of, stem_of,
			track_cid, preview_cid, orig_file_cid, duration,
			is_downloadable, is_download_gated, download_conditions, is_stream_gated, stream_conditions,
			release_date, is_scheduled_release, ai_attribution_user_id, is_playlist_upload, ddex_app, ddex_release_ids,
			is_available, track_segments, created_at
		FROM tracks WHERE track_id = $1 AND is_current = true AND is_delete = false LIMIT 1
	`, trackID).Scan(
		&r.TrackID, &r.OwnerID, &title, &genre, &mood, &tags, &desc,
		&cover, &coverSz, &r.IsUnlisted, &fieldVis, &remix, &stem,
		&tcid, &pcid, &ofcid, &r.Duration,
		&r.IsDownloadable, &r.IsDownloadGated, &dlCond, &r.IsStreamGated, &streamCond,
		&releaseDate, &r.IsScheduledRelease, &aiAttr, &r.IsPlaylistUpload, &ddex, &ddexRel,
		&r.IsAvailable, &segments, &r.CreatedAt,
	)
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
	r.DownloadConditions = dlCond
	r.StreamConditions = streamCond
	r.ReleaseDate = releaseDate
	r.AiAttributionUserID = nullInt64Ptr(aiAttr)
	r.DdexApp = nullStrPtr(ddex)
	r.DdexReleaseIDs = ddexRel
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
	mergeStrPtr("genre", &out.Genre)
	mergeStrPtr("mood", &out.Mood)
	mergeStrPtr("tags", &out.Tags)
	mergeStrPtr("description", &out.Description)
	mergeStrPtr("cover_art", &out.CoverArt)
	mergeStrPtr("cover_art_sizes", &out.CoverArtSizes)
	mergeStrPtr("track_cid", &out.TrackCID)
	mergeStrPtr("preview_cid", &out.PreviewCID)
	mergeStrPtr("orig_file_cid", &out.OrigFileCID)
	mergeStrPtr("ddex_app", &out.DdexApp)

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
	if v, ok := p.MetadataJSON("stem_of"); ok {
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
	if rd := p.MetadataString("release_date"); rd != "" {
		if t, err := time.Parse(time.RFC3339, rd); err == nil {
			out.ReleaseDate = pgtype.Timestamp{Time: t, Valid: true}
		}
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

func insertTrackRow(ctx context.Context, dbtx db.DBTX, r *trackRow, blockTime time.Time, txHash string, blockNumber int64) error {
	_, err := dbtx.Exec(ctx, `
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
			$29, $30, $31, $32, $33, $34
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
		r.IsAvailable,
		r.TrackSegments,
		r.CreatedAt,
		blockTime,
		txHash,
		blockNumber,
	)
	return err
}
