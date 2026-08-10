package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// --- Pinned comments ---

// commentPinMetadata is the payload of a Comment/Pin transaction. entity_id is
// the *track* being pinned to; the comment is the transaction's entity id.
type commentPinMetadata struct {
	EntityID  int64  `json:"entity_id"`
	CreatedAt string `json:"created_at,omitempty"`
}

type sourceCommentPin struct {
	TrackID     int64
	CommentID   int64
	OwnerID     int64
	OwnerWallet string
	// PinnedAt is the track row's updated_at. See writeCommentPins.
	PinnedAt time.Time
}

// writeCommentPins replays tracks.pinned_comment_id as Comment/Pin transactions.
//
// A pin is not stored as its own row: the source keeps it as a single column on
// the track, with no record of who pinned it or when. Nothing in the comment
// tables carries it, so a migration that replays only comments leaves the column
// NULL on every track — 1,286 of them on a production clone.
//
// The transaction shape is the production one (commentPinHandler is not
// overridden for migration, so its validation applies verbatim):
//   - entity id is the comment, and metadata entity_id is the track
//   - the signer must be the track owner, since only a track owner may pin
//   - the comment must already exist, which is why this step runs after
//     `comments` rather than beside it
//
// Both joins to users mirror the filters the tracks and comments steps use, so a
// pin is emitted only when the entities it depends on were themselves migrated.
// Nothing within a batch depends on another row in the same batch: the source
// holds one pinned comment per track, so two pins can never target the same
// track and the concurrent emission in processBatched is safe here.
func (w *Writer) writeCommentPins(ctx context.Context) error {
	// The track row's updated_at is the closest thing the source has to a pin
	// time, and passing it as created_at keeps tracks.updated_at at the source's
	// own value: migrationBlockTime uses it as the block time, and the pin
	// handler writes the block time to updated_at. Left absent it would be the
	// migration date instead. It is never earlier than the pinned comment's
	// created_at anywhere in a production clone, so the replayed pin does not
	// claim to predate the comment it pins.
	const from = `
		FROM tracks t
		JOIN users u ON u.user_id = t.owner_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		JOIN comments c ON c.comment_id = t.pinned_comment_id
		JOIN users cu ON cu.user_id = c.user_id AND cu.is_current = true AND cu.wallet IS NOT NULL AND cu.wallet <> ''
		WHERE t.is_current = true AND t.pinned_comment_id IS NOT NULL`

	return processBatched(ctx, w, "comment pins",
		`SELECT count(*)`+from,
		`SELECT t.track_id, t.pinned_comment_id, t.owner_id, COALESCE(LOWER(u.wallet), ''), t.updated_at`+from+`
		ORDER BY t.track_id`,
		func(rows pgx.Rows) (sourceCommentPin, error) {
			var p sourceCommentPin
			err := rows.Scan(&p.TrackID, &p.CommentID, &p.OwnerID, &p.OwnerWallet, &p.PinnedAt)
			return p, err
		},
		func(ctx context.Context, p sourceCommentPin) error {
			metaJSON, err := json.Marshal(commentPinMetadata{
				EntityID:  p.TrackID,
				CreatedAt: p.PinnedAt.Format(time.RFC3339),
			})
			if err != nil {
				return fmt.Errorf("marshal comment pin metadata for track %d: %w", p.TrackID, err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     p.OwnerID,
				EntityType: "Comment",
				EntityId:   p.CommentID,
				Action:     "Pin",
				Metadata:   string(metaJSON),
			}, p.OwnerWallet)
		},
	)
}
