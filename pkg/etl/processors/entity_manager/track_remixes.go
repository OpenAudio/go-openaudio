package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// updateRemixesTable replaces the set of remix parents for a track:
// delete-all-then-insert.
func updateRemixesTable(ctx context.Context, dbtx db.DBTX, childTrackID int64, metadata map[string]any) error {
	if _, err := dbtx.Exec(ctx, "DELETE FROM remixes WHERE child_track_id = $1", childTrackID); err != nil {
		return err
	}
	parents := getRemixParentTrackIDs(metadata)
	for _, parentID := range parents {
		if _, err := dbtx.Exec(ctx, `
			INSERT INTO remixes (parent_track_id, child_track_id)
			VALUES ($1, $2)
			ON CONFLICT (parent_track_id, child_track_id) DO NOTHING
		`, parentID, childTrackID); err != nil {
			return err
		}
	}
	return nil
}

// getRemixParentTrackIDs extracts parent track IDs from track metadata's
// `remix_of.tracks[].parent_track_id`.
func getRemixParentTrackIDs(metadata map[string]any) []int64 {
	if metadata == nil {
		return nil
	}
	remixOf, ok := metadata["remix_of"].(map[string]any)
	if !ok {
		return nil
	}
	tracksAny, ok := remixOf["tracks"]
	if !ok {
		return nil
	}
	tracks, ok := tracksAny.([]any)
	if !ok {
		return nil
	}
	var ids []int64
	for _, t := range tracks {
		entry, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := metadataMapInt64(entry, "parent_track_id"); ok {
			ids = append(ids, id)
		}
	}
	return ids
}
