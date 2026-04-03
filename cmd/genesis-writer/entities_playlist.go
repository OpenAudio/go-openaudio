package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

type playlistMetadataWrapper struct {
	CID  string                `json:"cid"`
	Data playlistMetadataInner `json:"data"`
}

type playlistMetadataInner struct {
	CreatedAt              string      `json:"created_at,omitempty"`
	PlaylistName           string      `json:"playlist_name"`
	Description            string      `json:"description,omitempty"`
	IsAlbum                bool        `json:"is_album,omitempty"`
	IsPrivate              bool        `json:"is_private,omitempty"`
	PlaylistImageSizesHash string      `json:"playlist_image_sizes_multihash,omitempty"`
	PlaylistContents       interface{} `json:"playlist_contents,omitempty"`
	ReleaseDate            string      `json:"release_date,omitempty"`
	IsStreamGated          bool        `json:"is_stream_gated,omitempty"`
	StreamConditions       interface{} `json:"stream_conditions,omitempty"`
	UPC                    string      `json:"upc,omitempty"`
}

type sourcePlaylist struct {
	PlaylistID          int64
	PlaylistOwnerID     int64
	PlaylistName        *string
	Description         *string
	IsAlbum             bool
	IsPrivate           bool
	MetadataMultihash   *string
	ImageSizesMultihash *string
	PlaylistContents    []byte // JSONB
	ReleaseDate         *string
	IsStreamGated       bool
	StreamConditions    []byte // JSONB
	CreatedAt           time.Time
}

func (w *Writer) writePlaylists(ctx context.Context) error {
	return processBatched(ctx, w, "playlists",
		`SELECT count(*) FROM playlists WHERE is_current = true AND is_delete = false`,
		`SELECT
			playlist_id, playlist_owner_id, playlist_name, description,
			is_album, is_private,
			metadata_multihash, playlist_image_sizes_multihash, playlist_contents,
			release_date::text, is_stream_gated, stream_conditions,
			created_at
		FROM playlists
		WHERE is_current = true AND is_delete = false
		ORDER BY playlist_id`,
		func(rows pgx.Rows) (sourcePlaylist, error) {
			var p sourcePlaylist
			err := rows.Scan(
				&p.PlaylistID, &p.PlaylistOwnerID, &p.PlaylistName, &p.Description,
				&p.IsAlbum, &p.IsPrivate,
				&p.MetadataMultihash, &p.ImageSizesMultihash, &p.PlaylistContents,
				&p.ReleaseDate, &p.IsStreamGated, &p.StreamConditions,
				&p.CreatedAt,
			)
			return p, err
		},
		func(ctx context.Context, p sourcePlaylist) error {
			inner := playlistMetadataInner{
				CreatedAt:              p.CreatedAt.Format(time.RFC3339),
				PlaylistName:           deref(p.PlaylistName),
				Description:            deref(p.Description),
				IsAlbum:                p.IsAlbum,
				IsPrivate:              p.IsPrivate,
				PlaylistImageSizesHash: deref(p.ImageSizesMultihash),
				ReleaseDate:            deref(p.ReleaseDate),
				IsStreamGated:          p.IsStreamGated,
			}

			inner.PlaylistContents = unmarshalJSONB(p.PlaylistContents)
			inner.StreamConditions = unmarshalJSONB(p.StreamConditions)

			cid := "genesis-import"
			if p.MetadataMultihash != nil && *p.MetadataMultihash != "" {
				cid = *p.MetadataMultihash
			}

			metaJSON, err := json.Marshal(playlistMetadataWrapper{
				CID:  cid,
				Data: inner,
			})
			if err != nil {
				return fmt.Errorf("marshal playlist %d metadata: %w", p.PlaylistID, err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     p.PlaylistOwnerID,
				EntityType: "Playlist",
				EntityId:   p.PlaylistID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			})
		},
	)
}
