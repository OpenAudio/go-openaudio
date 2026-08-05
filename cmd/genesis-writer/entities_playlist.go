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
	// UPC was already declared here but the SELECT never read the column, so it
	// serialized empty and omitempty dropped it. These four are all read by
	// playlist_create.go.
	UPC                    string      `json:"upc,omitempty"`
	PlaylistImageHash      string      `json:"playlist_image_multihash,omitempty"`
	DDEXApp                string      `json:"ddex_app,omitempty"`
	ParentalWarningType    string      `json:"parental_warning_type,omitempty"`
	// Always serialized: `omitempty` would drop a false value and the indexer
	// cannot tell "absent" from "false".
	IsDelete bool `json:"is_delete"`
}

type sourcePlaylist struct {
	PlaylistID          int64
	PlaylistOwnerID     int64
	OwnerWallet         string
	PlaylistName        *string
	Description         *string
	IsAlbum             bool
	IsPrivate           bool
	MetadataMultihash   *string
	ImageSizesMultihash *string
	ImageMultihash      *string
	PlaylistContents    []byte // JSONB
	UPC                 *string
	DDEXApp             *string
	ParentalWarningType *string
	ReleaseDate         *string
	IsStreamGated       bool
	StreamConditions    []byte // JSONB
	IsDelete            bool
	CreatedAt           time.Time
}

func (w *Writer) writePlaylists(ctx context.Context) error {
	return processBatched(ctx, w, "playlists",
		// Deleted playlists are migrated too, carrying is_delete in the metadata, so
		// a parity check can tell an intentional omission from real data loss.
		`SELECT count(*) FROM playlists WHERE is_current = true`,
		`SELECT
			p.playlist_id, p.playlist_owner_id, COALESCE(LOWER(u.wallet), ''),
			p.playlist_name, p.description,
			p.is_album, p.is_private,
			p.metadata_multihash, p.playlist_image_sizes_multihash, p.playlist_image_multihash, p.playlist_contents,
			p.upc, p.ddex_app, p.parental_warning_type,
			p.release_date::text, p.is_stream_gated, p.stream_conditions,
			p.is_delete,
			p.created_at
		FROM playlists p
		LEFT JOIN users u ON u.user_id = p.playlist_owner_id AND u.is_current = true
		WHERE p.is_current = true
		ORDER BY p.playlist_id`,
		func(rows pgx.Rows) (sourcePlaylist, error) {
			var p sourcePlaylist
			err := rows.Scan(
				&p.PlaylistID, &p.PlaylistOwnerID, &p.OwnerWallet,
				&p.PlaylistName, &p.Description,
				&p.IsAlbum, &p.IsPrivate,
				&p.MetadataMultihash, &p.ImageSizesMultihash, &p.ImageMultihash, &p.PlaylistContents,
				&p.UPC, &p.DDEXApp, &p.ParentalWarningType,
				&p.ReleaseDate, &p.IsStreamGated, &p.StreamConditions,
				&p.IsDelete,
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
				PlaylistImageHash:      deref(p.ImageMultihash),
				UPC:                    deref(p.UPC),
				DDEXApp:                deref(p.DDEXApp),
				ParentalWarningType:    deref(p.ParentalWarningType),
				ReleaseDate:            deref(p.ReleaseDate),
				IsStreamGated:          p.IsStreamGated,
				IsDelete:               p.IsDelete,
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
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     p.PlaylistOwnerID,
				EntityType: "Playlist",
				EntityId:   p.PlaylistID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, p.OwnerWallet)
		},
	)
}
