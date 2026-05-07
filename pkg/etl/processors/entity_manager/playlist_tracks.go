package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/OpenAudio/go-openaudio/pkg/hashes"
)

// extractPlaylistTrackIDs reads track_ids out of a playlist_contents metadata
// blob. Mirrors apps' playlist.py behavior: accepts either the new array
// format `[{track:..., time:...}]` or the legacy `{track_ids: [...]}` form.
// Each entry can use `track` or `track_id` as the field name.
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
// the playlist_contents metadata. Mirrors apps' update_playlist_tracks (in
// playlist.py): rows missing from the new contents are marked is_removed=true,
// new rows are inserted, and previously removed rows that reappear are
// recovered (is_removed=false).
//
// Side effects on tracks.playlists_containing_track / playlists_previously_containing_track
// are deferred — they're touched by apps' Python helper but not strictly
// required for parity of the junction table itself.
func updatePlaylistTracks(ctx context.Context, dbtx db.DBTX, playlistID int64, metadata map[string]any) error {
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
	}

	return nil
}
