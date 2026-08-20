package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// --- Comments ---

type commentMetadata struct {
	Body            string  `json:"body"`
	EntityID        int64   `json:"entity_id"`
	EntityType      string  `json:"entity_type"`
	ParentCommentID *int64  `json:"parent_comment_id,omitempty"`
	TrackTimestampS *int    `json:"track_timestamp_s,omitempty"`
	Mentions        []int64 `json:"mentions,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
	// Fan-club text post fields. The indexer reads both with a default that
	// matches an unset column (false / NULL), so `omitempty` cannot flip a value
	// on read and only the 39 members-only and 15 video comments carry them.
	// is_members_only is emitted verbatim from the source: every row that sets
	// it is entity_type='FanClub', which is what validateCommentWrite requires.
	IsMembersOnly bool   `json:"is_members_only,omitempty"`
	VideoURL      string `json:"video_url,omitempty"`
	// Always serialized: `omitempty` would drop a false value and the indexer
	// cannot tell "absent" from "not deleted".
	IsDelete bool `json:"is_delete"`
	// The source row is the final state of the comment, so an edited comment
	// has to replay as edited: the indexer only sets is_edited on an Update,
	// which a one-Create-per-comment migration never emits.
	IsEdited bool `json:"is_edited,omitempty"`
}

type sourceComment struct {
	CommentID       int64
	Text            *string
	UserID          int64
	UserWallet      string
	EntityID        int64
	EntityType      string
	TrackTimestampS *int
	CreatedAt       time.Time
	IsDelete        bool
	IsMembersOnly   bool
	VideoURL        *string
	IsEdited        bool
}

// writeComments emits root comments and replies in two passes.
//
// Ordering the query is not enough on its own: processBatched signs and emits
// each batch concurrently, so rows inside one batch land in a non-deterministic
// order. A reply that shares a batch with its parent can be written first, and
// the indexer then rejects it -- 1,237 replies on a production clone, all of
// them with the parent present, active, created earlier and in the same block.
//
// Splitting by depth removes the dependency instead of trying to order around
// it: every root is emitted before any reply, so both passes stay fully
// concurrent and no serialization is needed.
func (w *Writer) writeComments(ctx context.Context) error {
	// Two passes assume replies are never nested under other replies. Nothing
	// in the protocol enforces that -- validateCommentWrite only checks the
	// parent exists and shares the entity -- so verify it rather than trust it.
	// A second level would need a pass of its own, and silently mis-ordering it
	// is exactly the failure this is fixing.
	var nested int64
	if err := w.srcDB.QueryRow(ctx, `
		SELECT count(*) FROM comment_threads t
		WHERE EXISTS (SELECT 1 FROM comment_threads t2 WHERE t2.comment_id = t.parent_comment_id)
	`).Scan(&nested); err != nil {
		return fmt.Errorf("check comment nesting depth: %w", err)
	}
	if nested > 0 {
		return fmt.Errorf("%d replies are nested under other replies; the two-pass "+
			"comment emission only handles one level and would emit them out of order", nested)
	}

	// Pre-load comment threads (parent_comment_id for each comment).
	threads, err := preloadMap[int64, int64](ctx, w.srcDB,
		`SELECT comment_id, parent_comment_id FROM comment_threads`)
	if err != nil {
		return fmt.Errorf("preload comment threads: %w", err)
	}

	// Pre-load comment mentions (user_ids mentioned in each comment).
	mentions, err := preloadMap[int64, int64](ctx, w.srcDB,
		`SELECT comment_id, user_id FROM comment_mentions WHERE is_delete = false`)
	if err != nil {
		return fmt.Errorf("preload comment mentions: %w", err)
	}

	if err := w.writeCommentPass(ctx, "comments (roots)", false, threads, mentions); err != nil {
		return err
	}
	return w.writeCommentPass(ctx, "comments (replies)", true, threads, mentions)
}

// writeCommentPass emits either the roots or the replies. isReply selects which
// side of comment_threads the pass covers; the two are disjoint and together
// cover every comment.
func (w *Writer) writeCommentPass(
	ctx context.Context,
	name string,
	isReply bool,
	threads map[int64][]int64,
	mentions map[int64][]int64,
) error {
	depthFilter := "NOT EXISTS"
	if isReply {
		depthFilter = "EXISTS"
	}
	where := "AND " + depthFilter + " (SELECT 1 FROM comment_threads t WHERE t.comment_id = c.comment_id)"

	return processBatched(ctx, w, name,
		`SELECT count(*) FROM comments c
		JOIN users u ON u.user_id = c.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		`+where,
		`SELECT c.comment_id, c.text, c.user_id, COALESCE(LOWER(u.wallet), ''), c.entity_id, c.entity_type, c.track_timestamp_s, c.created_at, c.is_delete, c.is_members_only, c.video_url, c.is_edited
		FROM comments c
		JOIN users u ON u.user_id = c.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		`+where+`
		-- Chronological within a pass. Depth already guarantees a reply cannot
		-- precede its parent, so this only keeps emission stable and readable.
		ORDER BY c.created_at, c.comment_id`,
		func(rows pgx.Rows) (sourceComment, error) {
			var c sourceComment
			err := rows.Scan(&c.CommentID, &c.Text, &c.UserID, &c.UserWallet, &c.EntityID, &c.EntityType, &c.TrackTimestampS, &c.CreatedAt, &c.IsDelete, &c.IsMembersOnly, &c.VideoURL, &c.IsEdited)
			return c, err
		},
		func(ctx context.Context, c sourceComment) error {
			meta := commentMetadata{
				IsDelete:        c.IsDelete,
				Body:            deref(c.Text),
				EntityID:        c.EntityID,
				EntityType:      c.EntityType,
				TrackTimestampS: c.TrackTimestampS,
				CreatedAt:       c.CreatedAt.UTC().Format(time.RFC3339),
				IsMembersOnly:   c.IsMembersOnly,
				VideoURL:        deref(c.VideoURL),
				IsEdited:        c.IsEdited,
			}

			// Attach parent comment if this is a reply.
			if parents := threads[c.CommentID]; len(parents) > 0 {
				meta.ParentCommentID = &parents[0]
			}

			// Attach mentioned user IDs.
			if m := mentions[c.CommentID]; len(m) > 0 {
				meta.Mentions = m
			}

			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return fmt.Errorf("marshal comment %d metadata: %w", c.CommentID, err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     c.UserID,
				EntityType: "Comment",
				EntityId:   c.CommentID,
				Action:     "Create",
				Metadata:   string(metaJSON),
			}, c.UserWallet)
		},
	)
}

// --- Comment Reactions ---

func (w *Writer) writeCommentReactions(ctx context.Context) error {
	type commentReaction struct {
		commentID int64
		userID    int64
		wallet    string
		createdAt time.Time
	}
	return processBatched(ctx, w, "comment_reactions",
		`SELECT count(*) FROM comment_reactions cr
		JOIN users u ON u.user_id = cr.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE cr.is_delete = false`,
		`SELECT cr.comment_id, cr.user_id, COALESCE(LOWER(u.wallet), ''), cr.created_at
		FROM comment_reactions cr
		JOIN users u ON u.user_id = cr.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
		WHERE cr.is_delete = false
		ORDER BY cr.comment_id, cr.user_id`,
		func(rows pgx.Rows) (commentReaction, error) {
			var cr commentReaction
			err := rows.Scan(&cr.commentID, &cr.userID, &cr.wallet, &cr.createdAt)
			return cr, err
		},
		func(ctx context.Context, cr commentReaction) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: cr.createdAt.UTC().Format(time.RFC3339)})
			if err != nil {
				return fmt.Errorf("marshal comment reaction metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     cr.userID,
				EntityType: "Comment",
				EntityId:   cr.commentID,
				Action:     "React",
				Metadata:   string(metaJSON),
			}, cr.wallet)
		},
	)
}
