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
}

func (w *Writer) writeComments(ctx context.Context) error {
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

	return processBatched(ctx, w, "comments",
		`SELECT count(*) FROM comments WHERE is_delete = false`,
		`SELECT c.comment_id, c.text, c.user_id, COALESCE(LOWER(u.wallet), ''), c.entity_id, c.entity_type, c.track_timestamp_s, c.created_at
		FROM comments c
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = c.user_id
		WHERE c.is_delete = false
		ORDER BY c.comment_id`,
		func(rows pgx.Rows) (sourceComment, error) {
			var c sourceComment
			err := rows.Scan(&c.CommentID, &c.Text, &c.UserID, &c.UserWallet, &c.EntityID, &c.EntityType, &c.TrackTimestampS, &c.CreatedAt)
			return c, err
		},
		func(ctx context.Context, c sourceComment) error {
			meta := commentMetadata{
				Body:            deref(c.Text),
				EntityID:        c.EntityID,
				EntityType:      c.EntityType,
				TrackTimestampS: c.TrackTimestampS,
				CreatedAt:       c.CreatedAt.Format(time.RFC3339),
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
		`SELECT count(*) FROM comment_reactions WHERE is_delete = false`,
		`SELECT cr.comment_id, cr.user_id, COALESCE(LOWER(u.wallet), ''), cr.created_at
		FROM comment_reactions cr
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = cr.user_id
		WHERE cr.is_delete = false
		ORDER BY cr.comment_id, cr.user_id`,
		func(rows pgx.Rows) (commentReaction, error) {
			var cr commentReaction
			err := rows.Scan(&cr.commentID, &cr.userID, &cr.wallet, &cr.createdAt)
			return cr, err
		},
		func(ctx context.Context, cr commentReaction) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: cr.createdAt.Format(time.RFC3339)})
			if err != nil {
				return fmt.Errorf("marshal comment reaction metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     cr.userID,
				EntityType: "CommentReaction",
				EntityId:   cr.commentID,
				Action:     "React",
				Metadata:   string(metaJSON),
			}, cr.wallet)
		},
	)
}
