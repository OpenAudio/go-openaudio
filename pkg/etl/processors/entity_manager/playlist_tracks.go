package entity_manager

import (
	"context"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/OpenAudio/go-openaudio/pkg/hashes"
)

// extractPlaylistTrackIDs reads track_ids out of a playlist_contents metadata
// blob. Accepts either the new array form `[{track:..., time:...}]` or the
// legacy `{track_ids: [...]}` dict form. Each entry can use `track` or
// `track_id` as the field name.
func extractPlaylistTrackIDs(metadata map[string]any) []int64 {
	if metadata == nil {
		return nil
	}
	contents, ok := metadata["playlist_contents"]
	if !ok || contents == nil {
		return nil
	}

	var entries []any
	switch v := contents.(type) {
	case map[string]any:
		raw, ok := v["track_ids"]
		if !ok {
			return nil
		}
		arr, ok := raw.([]any)
		if !ok {
			return nil
		}
		entries = arr
	case []any:
		entries = v
	default:
		return nil
	}

	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := pickPlaylistTrackID(obj); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// pickPlaylistTrackID extracts a track_id from a playlist_contents entry.
// Accepts integer values directly; decodes hashid-encoded strings.
func pickPlaylistTrackID(entry map[string]any) (int64, bool) {
	for _, key := range []string{"track_id", "track"} {
		raw, ok := entry[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case float64:
			return int64(v), true
		case int:
			return int64(v), true
		case int64:
			return v, true
		case string:
			if v == "" {
				continue
			}
			if decoded, err := hashes.MaybeDecode(v); err == nil {
				return int64(decoded), true
			}
		}
	}
	return 0, false
}

// updatePlaylistTracks materializes the playlist_tracks junction table from
// the playlist_contents metadata: rows missing from the new contents are
// marked is_removed=true, new rows are inserted, and previously removed
// rows that reappear are recovered (is_removed=false).
//
// It also maintains the reverse index on tracks — playlists_containing_track
// and playlists_previously_containing_track — because that index is what
// grants a track's buyer access when they purchased the album rather than the
// track (see the usdc_purchase branch of the API's access check). The two are
// derived from the same delta and written in the same transaction so they
// cannot disagree.
func updatePlaylistTracks(ctx context.Context, dbtx db.DBTX, playlistID int64, metadata map[string]any, blockTime time.Time) error {
	updatedIDs := extractPlaylistTrackIDs(metadata)
	updatedSet := make(map[int64]struct{}, len(updatedIDs))
	for _, id := range updatedIDs {
		updatedSet[id] = struct{}{}
	}

	rows, err := dbtx.Query(ctx, `
		SELECT track_id, is_removed FROM playlist_tracks WHERE playlist_id = $1
	`, playlistID)
	if err != nil {
		return err
	}
	existing := make(map[int64]bool)
	for rows.Next() {
		var trackID int64
		var isRemoved bool
		if err := rows.Scan(&trackID, &isRemoved); err != nil {
			rows.Close()
			return err
		}
		existing[trackID] = isRemoved
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Tracks whose membership in this playlist actually changed. Only these
	// need the reverse index touched: every tracks row written here fires
	// handle_track, which recounts the owner's catalog, so a no-op update is
	// not free.
	var added, removed []int64

	for trackID, wasRemoved := range existing {
		if _, stillIn := updatedSet[trackID]; stillIn {
			if wasRemoved {
				if _, err := dbtx.Exec(ctx, `
					UPDATE playlist_tracks
					SET is_removed = false, updated_at = now()
					WHERE playlist_id = $1 AND track_id = $2
				`, playlistID, trackID); err != nil {
					return err
				}
				added = append(added, trackID)
			}
			continue
		}
		if !wasRemoved {
			if _, err := dbtx.Exec(ctx, `
				UPDATE playlist_tracks
				SET is_removed = true, updated_at = now()
				WHERE playlist_id = $1 AND track_id = $2
			`, playlistID, trackID); err != nil {
				return err
			}
			removed = append(removed, trackID)
		}
	}

	for _, trackID := range updatedIDs {
		if _, exists := existing[trackID]; exists {
			continue
		}
		if _, err := dbtx.Exec(ctx, `
			INSERT INTO playlist_tracks (playlist_id, track_id, is_removed)
			VALUES ($1, $2, false)
			ON CONFLICT (playlist_id, track_id) DO NOTHING
		`, playlistID, trackID); err != nil {
			return err
		}
		added = append(added, trackID)
	}

	return updateTrackPlaylistIndex(ctx, dbtx, playlistID, added, removed, blockTime)
}

// updateTrackPlaylistIndex maintains the reverse index on tracks for one
// playlist's membership change.
//
// A track gains the playlist in playlists_containing_track and loses any
// removal record for it; a removed track loses the playlist and gains
// `{"<playlist_id>": {"time": <block epoch>}}`. The timestamp is the block's,
// not wall-clock: it is compared against a purchase date to decide whether a
// buyer keeps access to a track that later left the album, so a replayed
// removal has to carry the time it originally happened.
//
// The removal record is only written when absent, and cleared whenever the
// track rejoins, so it always reflects the most recent departure.
func updateTrackPlaylistIndex(ctx context.Context, dbtx db.DBTX, playlistID int64, added, removed []int64, blockTime time.Time) error {
	if len(added) > 0 {
		if _, err := dbtx.Exec(ctx, `
			UPDATE tracks SET
				playlists_containing_track = CASE
					WHEN playlists_containing_track @> ARRAY[$1::integer] THEN playlists_containing_track
					ELSE array_append(playlists_containing_track, $1::integer) END,
				playlists_previously_containing_track = playlists_previously_containing_track - $1::text,
				updated_at = $3
			WHERE track_id = ANY($2::integer[]) AND is_current = true
			  AND (NOT (playlists_containing_track @> ARRAY[$1::integer])
			       OR jsonb_exists(playlists_previously_containing_track, $1::text))
		`, playlistID, added, blockTime); err != nil {
			return err
		}
	}

	if len(removed) > 0 {
		if _, err := dbtx.Exec(ctx, `
			UPDATE tracks SET
				playlists_containing_track = array_remove(playlists_containing_track, $1::integer),
				playlists_previously_containing_track = CASE
					WHEN jsonb_exists(playlists_previously_containing_track, $1::text)
						THEN playlists_previously_containing_track
					ELSE jsonb_set(playlists_previously_containing_track, ARRAY[$1::text],
					               jsonb_build_object('time', $4::bigint), true) END,
				updated_at = $3
			WHERE track_id = ANY($2::integer[]) AND is_current = true
			  AND (playlists_containing_track @> ARRAY[$1::integer]
			       OR NOT jsonb_exists(playlists_previously_containing_track, $1::text))
		`, playlistID, removed, blockTime, blockTime.Unix()); err != nil {
			return err
		}
	}

	return nil
}
