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
	CreatedAt           string      `json:"created_at,omitempty"`
	Title               string      `json:"title,omitempty"`
	OwnerID             int64       `json:"owner_id"`
	Duration            int         `json:"duration,omitempty"`
	Description         string      `json:"description,omitempty"`
	Genre               string      `json:"genre,omitempty"`
	Mood                string      `json:"mood,omitempty"`
	Tags                string      `json:"tags,omitempty"`
	TrackCID            string      `json:"track_cid,omitempty"`
	PreviewCID          string      `json:"preview_cid,omitempty"`
	CoverArt            string      `json:"cover_art,omitempty"`
	CoverArtSizes       string      `json:"cover_art_sizes,omitempty"`
	IsUnlisted          bool        `json:"is_unlisted,omitempty"`
	IsDownloadable      bool        `json:"is_downloadable,omitempty"`
	IsOriginalAvail     bool        `json:"is_original_available,omitempty"`
	ReleaseDate         string      `json:"release_date,omitempty"`
	License             string      `json:"license,omitempty"`
	ISRC                string      `json:"isrc,omitempty"`
	ISWC                string      `json:"iswc,omitempty"`
	BPM                 *float64    `json:"bpm,omitempty"`
	MusicalKey          string      `json:"musical_key,omitempty"`
	IsCustomBPM         bool        `json:"is_custom_bpm,omitempty"`
	IsCustomMusicalKey  bool        `json:"is_custom_musical_key,omitempty"`
	RemixOf             interface{} `json:"remix_of,omitempty"`
	StemOf              interface{} `json:"stem_of,omitempty"`
	IsStreamGated       bool        `json:"is_stream_gated,omitempty"`
	StreamConditions    interface{} `json:"stream_conditions,omitempty"`
	IsDownloadGated     bool        `json:"is_download_gated,omitempty"`
	DownloadConditions  interface{} `json:"download_conditions,omitempty"`
	IsScheduledRelease  bool        `json:"is_scheduled_release,omitempty"`
	IsPlaylistUpload    bool        `json:"is_playlist_upload,omitempty"`
	Collaborators       []int64     `json:"collaborators,omitempty"`
	OrigFileCID         string      `json:"orig_file_cid,omitempty"`
	OrigFilename        string      `json:"orig_filename,omitempty"`
	AudioUploadID       string      `json:"audio_upload_id,omitempty"`
	FieldVisibility     interface{} `json:"field_visibility,omitempty"`
	DDEXApp             string      `json:"ddex_app,omitempty"`
	DDEXReleaseIDs      interface{} `json:"ddex_release_ids,omitempty"`
	AIAttributionUserID *int64      `json:"ai_attribution_user_id,omitempty"`
	PreviewStartSeconds *float64    `json:"preview_start_seconds,omitempty"`
	CoverOriginalTitle  string      `json:"cover_original_song_title,omitempty"`
	CoverOriginalArtist string      `json:"cover_original_artist,omitempty"`
	Artists             interface{} `json:"artists,omitempty"`
	ResourceContribs    interface{} `json:"resource_contributors,omitempty"`
	IndirectContribs    interface{} `json:"indirect_resource_contributors,omitempty"`
	RightsController    interface{} `json:"rights_controller,omitempty"`
	CopyrightLine       interface{} `json:"copyright_line,omitempty"`
	ProducerCopyright   interface{} `json:"producer_copyright_line,omitempty"`
	ParentalWarning     string      `json:"parental_warning_type,omitempty"`
	CommentsDisabled    bool        `json:"comments_disabled,omitempty"`
	NoAIUse             bool        `json:"no_ai_use,omitempty"`
	RouteSlug           string      `json:"route_slug,omitempty"`
	RouteTitleSlug      string      `json:"route_title_slug,omitempty"`
	RouteCollisionID    int         `json:"route_collision_id,omitempty"`
	// State flags are always serialized: `omitempty` would drop a false value and
	// the indexer cannot tell "absent" from "false" (is_available defaults true).
	IsDelete    bool `json:"is_delete"`
	IsAvailable bool `json:"is_available"`
}

type sourceTrack struct {
	TrackID             int64
	OwnerID             int64
	OwnerWallet         string
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
	ReleaseDate         *time.Time
	License             *string
	ISRC                *string
	ISWC                *string
	BPM                 *float64
	MusicalKey          *string
	IsCustomBPM         bool
	IsCustomMusicalKey  bool
	RemixOf             []byte // JSONB
	StemOf              []byte // JSONB
	IsStreamGated       bool
	StreamConditions    []byte // JSONB
	IsDownloadGated     bool
	DownloadConditions  []byte // JSONB
	IsScheduledRelease  bool
	IsPlaylistUpload    bool
	AccessAuthorities   []string
	OrigFileCID         *string
	OrigFilename        *string
	AudioUploadID       *string
	FieldVisibility     []byte // JSONB
	DDEXApp             *string
	DDEXReleaseIDs      []byte // JSONB
	AIAttributionUserID *int64
	PreviewStartSeconds *float64
	CoverOriginalTitle  *string
	CoverOriginalArtist *string
	Artists             []byte // JSONB
	ResourceContribs    []byte // JSONB
	IndirectContribs    []byte // JSONB
	RightsController    []byte // JSONB
	CopyrightLine       []byte // JSONB
	ProducerCopyright   []byte // JSONB
	ParentalWarning     *string
	CommentsDisabled    bool
	NoAIUse             bool
	RouteSlug           *string
	RouteTitleSlug      *string
	RouteCollisionID    *int
	IsDelete            bool
	IsAvailable         bool
	CreatedAt           time.Time
}

// buildTrackMetadata maps a source row onto the metadata the ETL indexes. It is
// kept separate from the batch loop so the mapping can be asserted directly:
// a column the writer forgets to carry is invisible in the output otherwise.
func buildTrackMetadata(t sourceTrack, collaborators []int64) trackMetadataInner {
	inner := trackMetadataInner{
		CreatedAt:       t.CreatedAt.UTC().Format(time.RFC3339),
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
		ReleaseDate:     fmtReleaseDate(t.ReleaseDate),
		License:         deref(t.License),
		ISRC:            deref(t.ISRC),
		ISWC:            deref(t.ISWC),
		BPM:             t.BPM,
		MusicalKey:      deref(t.MusicalKey),
		IsStreamGated:   t.IsStreamGated,
		IsDownloadGated: t.IsDownloadGated,
		IsDelete:        t.IsDelete,
		IsAvailable:     t.IsAvailable,

		IsScheduledRelease: t.IsScheduledRelease,
		IsPlaylistUpload:   t.IsPlaylistUpload,
		IsCustomBPM:        t.IsCustomBPM,
		IsCustomMusicalKey: t.IsCustomMusicalKey,
		CommentsDisabled:   t.CommentsDisabled,
		NoAIUse:            t.NoAIUse,

		OrigFileCID:         deref(t.OrigFileCID),
		OrigFilename:        deref(t.OrigFilename),
		AudioUploadID:       deref(t.AudioUploadID),
		DDEXApp:             deref(t.DDEXApp),
		AIAttributionUserID: t.AIAttributionUserID,
		PreviewStartSeconds: t.PreviewStartSeconds,
		CoverOriginalTitle:  deref(t.CoverOriginalTitle),
		CoverOriginalArtist: deref(t.CoverOriginalArtist),
		ParentalWarning:     deref(t.ParentalWarning),
		RouteSlug:           deref(t.RouteSlug),
		RouteTitleSlug:      deref(t.RouteTitleSlug),
		RouteCollisionID:    derefInt(t.RouteCollisionID),
	}

	inner.RemixOf = unmarshalJSONB(t.RemixOf)
	inner.StemOf = unmarshalJSONB(t.StemOf)
	inner.FieldVisibility = unmarshalJSONB(t.FieldVisibility)
	inner.DDEXReleaseIDs = unmarshalJSONB(t.DDEXReleaseIDs)
	inner.StreamConditions = unmarshalJSONB(t.StreamConditions)
	inner.DownloadConditions = unmarshalJSONB(t.DownloadConditions)
	inner.Artists = unmarshalJSONB(t.Artists)
	inner.ResourceContribs = unmarshalJSONB(t.ResourceContribs)
	inner.IndirectContribs = unmarshalJSONB(t.IndirectContribs)
	inner.RightsController = unmarshalJSONB(t.RightsController)
	inner.CopyrightLine = unmarshalJSONB(t.CopyrightLine)
	inner.ProducerCopyright = unmarshalJSONB(t.ProducerCopyright)

	inner.TrackCID = deref(t.TrackCID)

	if len(collaborators) > 0 {
		inner.Collaborators = collaborators
	}

	return inner
}

// fmtReleaseDate emits RFC3339, the format the indexer's parseReleaseDate
// accepts. Selecting release_date::text instead yields Postgres's own
// "2026-09-06 22:06:00", which matches none of the accepted layouts, so the
// indexer silently fell back to block time. That is not a cosmetic date
// difference: a track whose release_date lands in the past is picked up by the
// scheduled-release publisher, which sets is_unlisted = false. On the
// 2026-08-07 snapshot 372 unlisted tracks with a future release date had the
// date rewritten and 368 of them were published early.
func fmtReleaseDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (w *Writer) writeTracks(ctx context.Context) error {
	// Pre-load collaborator lists so Track:Create metadata includes them,
	// which causes the ETL to create pending invites automatically.
	collabs, err := w.loadTrackCollaborators(ctx)
	if err != nil {
		return fmt.Errorf("load track collaborators: %w", err)
	}

	return processBatched(ctx, w, "tracks",
		// Deleted and unavailable tracks are migrated too, carrying their state in
		// the metadata: the source row is the truth, and omitting them would make
		// a parity check unable to tell an intentional omission from data loss.
		`SELECT count(*) FROM tracks t
		JOIN users u ON u.user_id = t.owner_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE t.is_current = true`,
		`SELECT
			t.track_id, t.owner_id, COALESCE(LOWER(u.wallet), ''), t.title, t.description, t.duration, t.genre, t.mood, t.tags,
			t.track_cid,
			t.cover_art, t.cover_art_sizes, t.preview_cid,
			t.is_unlisted, t.is_downloadable, t.is_original_available,
			t.release_date, t.license, t.isrc, t.iswc, t.bpm, t.musical_key,
			t.is_custom_bpm, t.is_custom_musical_key,
			t.remix_of, t.stem_of,
			t.is_stream_gated, t.stream_conditions,
			t.is_download_gated, t.download_conditions,
			t.is_scheduled_release, t.is_playlist_upload,
			t.access_authorities,
			t.orig_file_cid, t.orig_filename, t.audio_upload_id,
			t.field_visibility, t.ddex_app, t.ddex_release_ids,
			t.ai_attribution_user_id, t.preview_start_seconds,
			t.cover_original_song_title, t.cover_original_artist,
			t.artists, t.resource_contributors, t.indirect_resource_contributors,
			t.rights_controller, t.copyright_line, t.producer_copyright_line,
			t.parental_warning_type,
			t.comments_disabled, t.no_ai_use,
			r.slug, r.title_slug, r.collision_id,
			t.is_delete, t.is_available,
			t.created_at
		FROM tracks t
		JOIN users u ON u.user_id = t.owner_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		LEFT JOIN LATERAL (
			SELECT slug, title_slug, collision_id
			FROM track_routes tr
			WHERE tr.track_id = t.track_id AND tr.is_current = true
			ORDER BY tr.collision_id DESC, tr.slug
			LIMIT 1
		) r ON true
		WHERE t.is_current = true
		ORDER BY t.track_id`,
		func(rows pgx.Rows) (sourceTrack, error) {
			var t sourceTrack
			err := rows.Scan(
				&t.TrackID, &t.OwnerID, &t.OwnerWallet, &t.Title, &t.Description, &t.Duration, &t.Genre, &t.Mood, &t.Tags,
				&t.TrackCID,
				&t.CoverArt, &t.CoverArtSizes, &t.PreviewCID,
				&t.IsUnlisted, &t.IsDownloadable, &t.IsOriginalAvailable,
				&t.ReleaseDate, &t.License, &t.ISRC, &t.ISWC, &t.BPM, &t.MusicalKey,
				&t.IsCustomBPM, &t.IsCustomMusicalKey,
				&t.RemixOf, &t.StemOf,
				&t.IsStreamGated, &t.StreamConditions,
				&t.IsDownloadGated, &t.DownloadConditions,
				&t.IsScheduledRelease, &t.IsPlaylistUpload,
				&t.AccessAuthorities,
				&t.OrigFileCID, &t.OrigFilename, &t.AudioUploadID,
				&t.FieldVisibility, &t.DDEXApp, &t.DDEXReleaseIDs,
				&t.AIAttributionUserID, &t.PreviewStartSeconds,
				&t.CoverOriginalTitle, &t.CoverOriginalArtist,
				&t.Artists, &t.ResourceContribs, &t.IndirectContribs,
				&t.RightsController, &t.CopyrightLine, &t.ProducerCopyright,
				&t.ParentalWarning,
				&t.CommentsDisabled, &t.NoAIUse,
				&t.RouteSlug, &t.RouteTitleSlug, &t.RouteCollisionID,
				&t.IsDelete, &t.IsAvailable,
				&t.CreatedAt,
			)
			return t, err
		},
		func(ctx context.Context, t sourceTrack) error {
			inner := buildTrackMetadata(t, collabs[t.TrackID])

			metaJSON, err := json.Marshal(trackMetadataWrapper{
				CID:               deref(t.TrackCID),
				AccessAuthorities: t.AccessAuthorities,
				Data:              inner,
			})
			if err != nil {
				return fmt.Errorf("marshal track %d metadata: %w", t.TrackID, err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     t.OwnerID,
				EntityType: "Track",
				EntityId:   t.TrackID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, t.OwnerWallet)
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
	UserWallet    string
	City          *string
	Region        *string
	Country       *string
	CreatedAt     time.Time
}

func (w *Writer) writeTrackDownloads(ctx context.Context) error {
	return processBatched(ctx, w, "track_downloads",
		`SELECT count(*) FROM track_downloads td
		JOIN users u ON u.user_id = td.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''`,
		`SELECT td.parent_track_id, td.track_id, td.user_id, COALESCE(LOWER(u.wallet), ''), td.city, td.region, td.country, td.created_at
		FROM track_downloads td
		JOIN users u ON u.user_id = td.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		ORDER BY td.parent_track_id, td.track_id`,
		func(rows pgx.Rows) (sourceTrackDownload, error) {
			var d sourceTrackDownload
			err := rows.Scan(&d.ParentTrackID, &d.TrackID, &d.UserID, &d.UserWallet, &d.City, &d.Region, &d.Country, &d.CreatedAt)
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
				CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal track download metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     userID,
				EntityType: "Track",
				EntityId:   d.TrackID,
				Action:     "Download",
				Metadata:   string(metaJSON),
			}, d.UserWallet)
		},
	)
}
