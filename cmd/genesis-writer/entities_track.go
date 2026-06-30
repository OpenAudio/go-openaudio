package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

type trackMetadataWrapper struct {
	CID               string             `json:"cid"`
	AccessAuthorities []string           `json:"access_authorities,omitempty"`
	Data              trackMetadataInner `json:"data"`
}

type trackMetadataInner struct {
	CreatedAt          string      `json:"created_at,omitempty"`
	Title              string      `json:"title,omitempty"`
	OwnerID            int64       `json:"owner_id"`
	Duration           int         `json:"duration,omitempty"`
	Description        string      `json:"description,omitempty"`
	Genre              string      `json:"genre,omitempty"`
	Mood               string      `json:"mood,omitempty"`
	Tags               string      `json:"tags,omitempty"`
	TrackCID           string      `json:"track_cid,omitempty"`
	PreviewCID         string      `json:"preview_cid,omitempty"`
	CoverArt           string      `json:"cover_art,omitempty"`
	CoverArtSizes      string      `json:"cover_art_sizes,omitempty"`
	IsUnlisted         bool        `json:"is_unlisted,omitempty"`
	IsDownloadable     bool        `json:"is_downloadable,omitempty"`
	IsOriginalAvail    bool        `json:"is_original_available,omitempty"`
	ReleaseDate        string      `json:"release_date,omitempty"`
	License            string      `json:"license,omitempty"`
	ISRC               string      `json:"isrc,omitempty"`
	ISWC               string      `json:"iswc,omitempty"`
	BPM                *float64    `json:"bpm,omitempty"`
	MusicalKey         string      `json:"musical_key,omitempty"`
	RemixOf            interface{} `json:"remix_of,omitempty"`
	StemOf             interface{} `json:"stem_of,omitempty"`
	IsStreamGated      bool        `json:"is_stream_gated,omitempty"`
	StreamConditions   interface{} `json:"stream_conditions,omitempty"`
	IsDownloadGated    bool        `json:"is_download_gated,omitempty"`
	DownloadConditions interface{} `json:"download_conditions,omitempty"`
	Collaborators      []int64     `json:"collaborators,omitempty"`
}

type sourceTrack struct {
	TrackID             int64
	OwnerID             int64
	Title               *string
	Description         *string
	Duration            *int
	Genre               *string
	Mood                *string
	Tags                *string
	TrackCID            *string
	CoverArt            *string
	CoverArtSizes       *string
	PreviewCID          *string
	IsUnlisted          bool
	IsDownloadable      bool
	IsOriginalAvailable bool
	ReleaseDate         *string
	License             *string
	ISRC                *string
	ISWC                *string
	BPM                 *float64
	MusicalKey          *string
	RemixOf             []byte // JSONB
	StemOf              []byte // JSONB
	IsStreamGated       bool
	StreamConditions    []byte // JSONB
	IsDownloadGated     bool
	DownloadConditions  []byte // JSONB
	AccessAuthorities   []string
	CreatedAt           time.Time
}

func (w *Writer) writeTracks(ctx context.Context) error {
	// Pre-load collaborator lists so Track:Create metadata includes them,
	// which causes the ETL to create pending invites automatically.
	collabs, err := w.loadTrackCollaborators(ctx)
	if err != nil {
		return fmt.Errorf("load track collaborators: %w", err)
	}

	return processBatched(ctx, w, "tracks",
		`SELECT count(*) FROM tracks WHERE is_current = true AND is_delete = false AND is_available = true`,
		`SELECT
			track_id, owner_id, title, description, duration, genre, mood, tags,
			track_cid,
			cover_art, cover_art_sizes, preview_cid,
			is_unlisted, is_downloadable, is_original_available,
			release_date::text, license, isrc, iswc, bpm, musical_key,
			remix_of, stem_of,
			is_stream_gated, stream_conditions,
			is_download_gated, download_conditions,
			access_authorities,
			created_at
		FROM tracks
		WHERE is_current = true AND is_delete = false AND is_available = true
		ORDER BY track_id`,
		func(rows pgx.Rows) (sourceTrack, error) {
			var t sourceTrack
			err := rows.Scan(
				&t.TrackID, &t.OwnerID, &t.Title, &t.Description, &t.Duration, &t.Genre, &t.Mood, &t.Tags,
				&t.TrackCID,
				&t.CoverArt, &t.CoverArtSizes, &t.PreviewCID,
				&t.IsUnlisted, &t.IsDownloadable, &t.IsOriginalAvailable,
				&t.ReleaseDate, &t.License, &t.ISRC, &t.ISWC, &t.BPM, &t.MusicalKey,
				&t.RemixOf, &t.StemOf,
				&t.IsStreamGated, &t.StreamConditions,
				&t.IsDownloadGated, &t.DownloadConditions,
				&t.AccessAuthorities,
				&t.CreatedAt,
			)
			return t, err
		},
		func(ctx context.Context, t sourceTrack) error {
			inner := trackMetadataInner{
				CreatedAt:       t.CreatedAt.Format(time.RFC3339),
				OwnerID:         t.OwnerID,
				Title:           deref(t.Title),
				Description:     deref(t.Description),
				Duration:        derefInt(t.Duration),
				Genre:           deref(t.Genre),
				Mood:            deref(t.Mood),
				Tags:            deref(t.Tags),
				CoverArt:        deref(t.CoverArt),
				CoverArtSizes:   deref(t.CoverArtSizes),
				PreviewCID:      deref(t.PreviewCID),
				IsUnlisted:      t.IsUnlisted,
				IsDownloadable:  t.IsDownloadable,
				IsOriginalAvail: t.IsOriginalAvailable,
				ReleaseDate:     deref(t.ReleaseDate),
				License:         deref(t.License),
				ISRC:            deref(t.ISRC),
				ISWC:            deref(t.ISWC),
				BPM:             t.BPM,
				MusicalKey:      deref(t.MusicalKey),
				IsStreamGated:   t.IsStreamGated,
				IsDownloadGated: t.IsDownloadGated,
			}

			inner.RemixOf = unmarshalJSONB(t.RemixOf)
			inner.StemOf = unmarshalJSONB(t.StemOf)
			inner.StreamConditions = unmarshalJSONB(t.StreamConditions)
			inner.DownloadConditions = unmarshalJSONB(t.DownloadConditions)

			inner.TrackCID = deref(t.TrackCID)

			if ids, ok := collabs[t.TrackID]; ok {
				inner.Collaborators = ids
			}

			metaJSON, err := json.Marshal(trackMetadataWrapper{
				CID:               deref(t.TrackCID),
				AccessAuthorities: t.AccessAuthorities,
				Data:              inner,
			})
			if err != nil {
				return fmt.Errorf("marshal track %d metadata: %w", t.TrackID, err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     t.OwnerID,
				EntityType: "Track",
				EntityId:   t.TrackID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Track Downloads ---

type trackDownloadMetadata struct {
	City      string `json:"city,omitempty"`
	Region    string `json:"region,omitempty"`
	Country   string `json:"country,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type sourceTrackDownload struct {
	ParentTrackID int64
	TrackID       int64
	UserID        *int64
	City          *string
	Region        *string
	Country       *string
	CreatedAt     time.Time
}

func (w *Writer) writeTrackDownloads(ctx context.Context) error {
	return processBatched(ctx, w, "track_downloads",
		`SELECT count(*) FROM track_downloads`,
		`SELECT parent_track_id, track_id, user_id, city, region, country, created_at
		FROM track_downloads
		ORDER BY parent_track_id, track_id`,
		func(rows pgx.Rows) (sourceTrackDownload, error) {
			var d sourceTrackDownload
			err := rows.Scan(&d.ParentTrackID, &d.TrackID, &d.UserID, &d.City, &d.Region, &d.Country, &d.CreatedAt)
			return d, err
		},
		func(ctx context.Context, d sourceTrackDownload) error {
			var userID int64
			if d.UserID != nil {
				userID = *d.UserID
			}
			meta := trackDownloadMetadata{
				City:      deref(d.City),
				Region:    deref(d.Region),
				Country:   deref(d.Country),
				CreatedAt: d.CreatedAt.Format(time.RFC3339),
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal track download metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     userID,
				EntityType: "Track",
				EntityId:   d.TrackID,
				Action:     "Download",
				Metadata:   string(metaJSON),
			})
		},
	)
}
