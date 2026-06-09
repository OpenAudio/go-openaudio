package entity_manager

import (
	"context"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// updateTrackCollaboratorsTable reconciles a track's collaborator invites
// against the `collaborators` list in track metadata. Newly listed users are
// inserted as 'pending'; existing rows keep their status (an already-'accepted'
// collaborator is NOT reset to 'pending'); users dropped from the list are
// removed.
//
// Callers must only invoke this when the metadata explicitly carries the
// `collaborators` key (always on Create; on Update only when present), so that
// unrelated track edits never clear collaborators. The owner is never recorded
// as a collaborator of their own track.
func updateTrackCollaboratorsTable(ctx context.Context, dbtx db.DBTX, trackID, ownerID int64, metadata map[string]any, blockTime time.Time, txHash string, blockNumber int64) error {
	// Non-nil so an empty list encodes as '{}' (not NULL) below, which clears
	// all collaborators rather than matching nothing.
	desired := getCollaboratorUserIDs(metadata, ownerID)
	if desired == nil {
		desired = []int64{}
	}

	// Drop collaborators the owner no longer lists.
	if _, err := dbtx.Exec(ctx,
		"DELETE FROM track_collaborators WHERE track_id = $1 AND collaborator_user_id <> ALL($2::int[])",
		trackID, desired); err != nil {
		return err
	}

	// Add newly listed collaborators as pending; ON CONFLICT DO NOTHING leaves
	// an already accepted/rejected/pending row untouched.
	_, err := dbtx.Exec(ctx, `
		INSERT INTO track_collaborators (
			track_id, collaborator_user_id, invited_by, status,
			created_at, updated_at, txhash, blocknumber
		)
		SELECT $1, uid, $3, 'pending', $4, $4, $5, $6
		FROM unnest($2::int[]) AS uid
		ON CONFLICT (track_id, collaborator_user_id) DO NOTHING
	`, trackID, desired, ownerID, blockTime, txHash, blockNumber)
	return err
}

// getCollaboratorUserIDs extracts collaborator user IDs from track metadata's
// `collaborators` array, excluding the owner and de-duplicating. Array entries
// may be bare numeric IDs or objects carrying a `user_id` field.
func getCollaboratorUserIDs(metadata map[string]any, ownerID int64) []int64 {
	if metadata == nil {
		return nil
	}
	raw, ok := metadata["collaborators"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	seen := make(map[int64]bool)
	var ids []int64
	for _, item := range list {
		var id int64
		switch v := item.(type) {
		case float64:
			id = int64(v)
		case int:
			id = int64(v)
		case int64:
			id = v
		case map[string]any:
			if n, ok := metadataMapInt64(v, "user_id"); ok {
				id = n
			}
		}
		if id == 0 || id == ownerID || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= MaxTrackCollaborators {
			break
		}
	}
	return ids
}
