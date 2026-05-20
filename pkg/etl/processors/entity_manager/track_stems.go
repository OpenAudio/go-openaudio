package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// upsertStemRow links a stem child to its parent track. Idempotent on the
// (parent, child) primary key.
func upsertStemRow(ctx context.Context, dbtx db.DBTX, parentTrackID, childTrackID int64) error {
	_, err := dbtx.Exec(ctx, `
		INSERT INTO stems (parent_track_id, child_track_id)
		VALUES ($1, $2)
		ON CONFLICT (parent_track_id, child_track_id) DO NOTHING
	`, parentTrackID, childTrackID)
	return err
}

// updateStemsTable adds a stems row when track metadata declares a parent_track_id.
func updateStemsTable(ctx context.Context, dbtx db.DBTX, childTrackID int64, metadata map[string]any) error {
	if metadata == nil {
		return nil
	}
	stemOf, ok := metadata["stem_of"].(map[string]any)
	if !ok {
		return nil
	}
	parentID, ok := metadataMapInt64(stemOf, "parent_track_id")
	if !ok {
		return nil
	}
	return upsertStemRow(ctx, dbtx, parentID, childTrackID)
}

// deleteStemsForChild removes any stems rows where this track is the child.
func deleteStemsForChild(ctx context.Context, dbtx db.DBTX, childTrackID int64) error {
	_, err := dbtx.Exec(ctx, "DELETE FROM stems WHERE child_track_id = $1", childTrackID)
	return err
}

// metadataMapInt64 reads an int64 field from an arbitrary metadata map,
// supporting JSON number, int, and int64 forms. Lives here because stems
// + remixes both need it for `parent_track_id` extraction.
func metadataMapInt64(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return int64(val), true
	case int:
		return int64(val), true
	case int64:
		return val, true
	}
	return 0, false
}
