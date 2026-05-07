package entity_manager

import (
	"context"
	"encoding/json"
	"time"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// updateTrackPriceHistory writes a track_price_history row when track metadata
// includes USDC gating with a price + splits. Mirrors apps' Python helper in
// track.py (update_track_price_history).
//
// access: 'stream' if is_stream_gated, otherwise 'download' if is_download_gated.
// Only writes when there's no existing row at the same block_timestamp with the
// same total_price_cents + splits (apps' equality check via .equals()).
func updateTrackPriceHistory(ctx context.Context, dbtx db.DBTX, trackID int64, blocknumber int64, blockTime time.Time, metadata map[string]any) error {
	access, conditions := pickPriceConditions(metadata)
	if conditions == nil {
		return nil
	}

	usdc, ok := conditions["usdc_purchase"].(map[string]any)
	if !ok {
		return nil
	}
	priceCents, ok := metadataMapInt64(usdc, "price")
	if !ok {
		return NewValidationError("track %d usdc_purchase missing or non-int price", trackID)
	}
	splitsRaw, ok := usdc["splits"]
	if !ok {
		return NewValidationError("track %d usdc_purchase missing splits", trackID)
	}
	splitsJSON, err := json.Marshal(splitsRaw)
	if err != nil {
		return err
	}

	if same, err := lastTrackPriceMatches(ctx, dbtx, trackID, priceCents, splitsJSON, access); err != nil {
		return err
	} else if same {
		return nil
	}

	_, err = dbtx.Exec(ctx, `
		INSERT INTO track_price_history (track_id, splits, total_price_cents, blocknumber, block_timestamp, access)
		VALUES ($1, $2::jsonb, $3, $4, $5, $6::usdc_purchase_access_type)
		ON CONFLICT (track_id, block_timestamp) DO NOTHING
	`, trackID, string(splitsJSON), priceCents, blocknumber, blockTime, access)
	return err
}

// updateAlbumPriceHistory writes an album_price_history row when playlist
// metadata declares USDC stream-gating. Mirrors apps' update_album_price_history.
func updateAlbumPriceHistory(ctx context.Context, dbtx db.DBTX, playlistID int64, blocknumber int64, blockTime time.Time, metadata map[string]any) error {
	if metadata == nil {
		return nil
	}
	if !metadataBoolOr(metadata, "is_stream_gated", false) {
		return nil
	}
	conditions, ok := metadata["stream_conditions"].(map[string]any)
	if !ok {
		return nil
	}
	usdc, ok := conditions["usdc_purchase"].(map[string]any)
	if !ok {
		return nil
	}
	priceCents, ok := metadataMapInt64(usdc, "price")
	if !ok {
		return NewValidationError("playlist %d usdc_purchase missing or non-int price", playlistID)
	}
	splitsRaw, ok := usdc["splits"]
	if !ok {
		return NewValidationError("playlist %d usdc_purchase missing splits", playlistID)
	}
	splitsJSON, err := json.Marshal(splitsRaw)
	if err != nil {
		return err
	}

	if same, err := lastAlbumPriceMatches(ctx, dbtx, playlistID, priceCents, splitsJSON); err != nil {
		return err
	} else if same {
		return nil
	}

	_, err = dbtx.Exec(ctx, `
		INSERT INTO album_price_history (playlist_id, splits, total_price_cents, blocknumber, block_timestamp)
		VALUES ($1, $2::jsonb, $3, $4, $5)
		ON CONFLICT (playlist_id, block_timestamp) DO NOTHING
	`, playlistID, string(splitsJSON), priceCents, blocknumber, blockTime)
	return err
}

// pickPriceConditions returns ("stream", stream_conditions) when stream-gated,
// ("download", download_conditions) when download-only-gated, or ("", nil)
// when not USDC-gated. Mirrors apps' is_stream_gated / is_download_gated logic.
func pickPriceConditions(metadata map[string]any) (string, map[string]any) {
	if metadata == nil {
		return "", nil
	}
	streamGated := metadataBoolOr(metadata, "is_stream_gated", false)
	downloadGated := metadataBoolOr(metadata, "is_download_gated", false)
	if streamGated {
		if c, ok := metadata["stream_conditions"].(map[string]any); ok {
			return "stream", c
		}
	}
	if downloadGated {
		if c, ok := metadata["download_conditions"].(map[string]any); ok {
			return "download", c
		}
	}
	return "", nil
}

func metadataBoolOr(m map[string]any, key string, def bool) bool {
	v, ok := m[key].(bool)
	if !ok {
		return def
	}
	return v
}

// lastTrackPriceMatches reports whether the most recent track_price_history
// row already records this price/splits/access combination — used to skip
// duplicate inserts when nothing has changed.
func lastTrackPriceMatches(ctx context.Context, dbtx db.DBTX, trackID, priceCents int64, splits []byte, access string) (bool, error) {
	var lastPrice int64
	var lastSplits []byte
	var lastAccess string
	err := dbtx.QueryRow(ctx, `
		SELECT total_price_cents, splits, access::text
		FROM track_price_history
		WHERE track_id = $1
		ORDER BY block_timestamp DESC
		LIMIT 1
	`, trackID).Scan(&lastPrice, &lastSplits, &lastAccess)
	if err != nil {
		// no rows yet → not a match, caller should insert
		return false, nil
	}
	return lastPrice == priceCents && string(lastSplits) == string(splits) && lastAccess == access, nil
}

func lastAlbumPriceMatches(ctx context.Context, dbtx db.DBTX, playlistID, priceCents int64, splits []byte) (bool, error) {
	var lastPrice int64
	var lastSplits []byte
	err := dbtx.QueryRow(ctx, `
		SELECT total_price_cents, splits
		FROM album_price_history
		WHERE playlist_id = $1
		ORDER BY block_timestamp DESC
		LIMIT 1
	`, playlistID).Scan(&lastPrice, &lastSplits)
	if err != nil {
		return false, nil
	}
	return lastPrice == priceCents && string(lastSplits) == string(splits), nil
}
