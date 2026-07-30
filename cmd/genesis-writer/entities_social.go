package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/jackc/pgx/v5"
)

// createdAtMeta is a minimal metadata payload carrying only created_at.
type createdAtMeta struct {
	CreatedAt string `json:"created_at"`
}

// socialMeta carries created_at plus the row's delete state. Soft-deleted social
// rows are migrated too, so a parity check against the source can tell an
// intentional omission from real data loss. is_delete is always serialized:
// `omitempty` would drop a false value and the indexer cannot tell "absent" from
// "false".
type socialMeta struct {
	CreatedAt string `json:"created_at"`
	IsDelete  bool   `json:"is_delete"`
}

func fmtCreatedAt(t time.Time) string {
	return t.Format(time.RFC3339)
}

// --- Follows ---

func (w *Writer) writeFollows(ctx context.Context) error {
	type follow struct {
		follower, followee int64
		wallet             string
		createdAt          time.Time
		isDelete           bool
	}
	return processBatched(ctx, w, "follows",
		`SELECT count(*) FROM follows WHERE is_current = true`,
		`SELECT f.follower_user_id, f.followee_user_id, COALESCE(LOWER(u.wallet), ''), f.created_at, f.is_delete
		FROM follows f
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = f.follower_user_id
		WHERE f.is_current = true
		ORDER BY f.follower_user_id, f.followee_user_id`,
		func(rows pgx.Rows) (follow, error) {
			var f follow
			err := rows.Scan(&f.follower, &f.followee, &f.wallet, &f.createdAt, &f.isDelete)
			return f, err
		},
		func(ctx context.Context, f follow) error {
			metaJSON, err := json.Marshal(socialMeta{CreatedAt: fmtCreatedAt(f.createdAt), IsDelete: f.isDelete})
			if err != nil {
				return fmt.Errorf("marshal follow metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     f.follower,
				EntityType: "User",
				EntityId:   f.followee,
				Action:     "Follow",
				Metadata:   string(metaJSON),
			}, f.wallet)
		},
	)
}

// --- Saves ---

func (w *Writer) writeSaves(ctx context.Context) error {
	type save struct {
		userID, itemID int64
		wallet         string
		saveType       string
		createdAt      time.Time
		isDelete       bool
	}
	return processBatched(ctx, w, "saves",
		`SELECT count(*) FROM saves WHERE is_current = true`,
		`SELECT s.user_id, s.save_item_id, COALESCE(LOWER(u.wallet), ''), s.save_type, s.created_at, s.is_delete
		FROM saves s
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = s.user_id
		WHERE s.is_current = true
		ORDER BY s.user_id, s.save_item_id`,
		func(rows pgx.Rows) (save, error) {
			var s save
			err := rows.Scan(&s.userID, &s.itemID, &s.wallet, &s.saveType, &s.createdAt, &s.isDelete)
			return s, err
		},
		func(ctx context.Context, s save) error {
			metaJSON, err := json.Marshal(socialMeta{CreatedAt: fmtCreatedAt(s.createdAt), IsDelete: s.isDelete})
			if err != nil {
				return fmt.Errorf("marshal save metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.userID,
				EntityType: saveRepostEntityType(s.saveType),
				EntityId:   s.itemID,
				Action:     "Save",
				Metadata:   string(metaJSON),
			}, s.wallet)
		},
	)
}

// --- Reposts ---

func (w *Writer) writeReposts(ctx context.Context) error {
	type repost struct {
		userID, itemID int64
		wallet         string
		repostType     string
		createdAt      time.Time
		isDelete       bool
	}
	return processBatched(ctx, w, "reposts",
		`SELECT count(*) FROM reposts WHERE is_current = true`,
		`SELECT r.user_id, r.repost_item_id, COALESCE(LOWER(u.wallet), ''), r.repost_type, r.created_at, r.is_delete
		FROM reposts r
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = r.user_id
		WHERE r.is_current = true
		ORDER BY r.user_id, r.repost_item_id`,
		func(rows pgx.Rows) (repost, error) {
			var r repost
			err := rows.Scan(&r.userID, &r.itemID, &r.wallet, &r.repostType, &r.createdAt, &r.isDelete)
			return r, err
		},
		func(ctx context.Context, r repost) error {
			metaJSON, err := json.Marshal(socialMeta{CreatedAt: fmtCreatedAt(r.createdAt), IsDelete: r.isDelete})
			if err != nil {
				return fmt.Errorf("marshal repost metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     r.userID,
				EntityType: saveRepostEntityType(r.repostType),
				EntityId:   r.itemID,
				Action:     "Repost",
				Metadata:   string(metaJSON),
			}, r.wallet)
		},
	)
}

// --- Shares ---

func (w *Writer) writeShares(ctx context.Context) error {
	type share struct {
		userID    int64
		itemID    int64
		wallet    string
		shareType string
		createdAt time.Time
	}
	return processBatched(ctx, w, "shares",
		`SELECT count(*) FROM shares`,
		`SELECT s.user_id, s.share_item_id, COALESCE(LOWER(u.wallet), ''), s.share_type, s.created_at
		FROM shares s
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = s.user_id
		ORDER BY s.user_id, s.share_item_id`,
		func(rows pgx.Rows) (share, error) {
			var s share
			err := rows.Scan(&s.userID, &s.itemID, &s.wallet, &s.shareType, &s.createdAt)
			return s, err
		},
		func(ctx context.Context, s share) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(s.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal share metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.userID,
				EntityType: saveRepostEntityType(s.shareType),
				EntityId:   s.itemID,
				Action:     "Share",
				Metadata:   string(metaJSON),
			}, s.wallet)
		},
	)
}

// --- Subscriptions ---

func (w *Writer) writeSubscriptions(ctx context.Context) error {
	type subscription struct {
		subscriberID, userID int64
		wallet               string
		createdAt            time.Time
		isDelete             bool
	}
	return processBatched(ctx, w, "subscriptions",
		`SELECT count(*) FROM subscriptions WHERE is_current = true`,
		`SELECT s.subscriber_id, s.user_id, COALESCE(LOWER(u.wallet), ''), s.created_at, s.is_delete
		FROM subscriptions s
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = s.subscriber_id
		WHERE s.is_current = true
		ORDER BY s.subscriber_id, s.user_id`,
		func(rows pgx.Rows) (subscription, error) {
			var s subscription
			err := rows.Scan(&s.subscriberID, &s.userID, &s.wallet, &s.createdAt, &s.isDelete)
			return s, err
		},
		func(ctx context.Context, s subscription) error {
			metaJSON, err := json.Marshal(socialMeta{CreatedAt: fmtCreatedAt(s.createdAt), IsDelete: s.isDelete})
			if err != nil {
				return fmt.Errorf("marshal subscription metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.subscriberID,
				EntityType: "User",
				EntityId:   s.userID,
				Action:     "Subscribe",
				Metadata:   string(metaJSON),
			}, s.wallet)
		},
	)
}

// --- Muted Users ---

func (w *Writer) writeMutedUsers(ctx context.Context) error {
	type mutedUser struct {
		userID, mutedUserID int64
		wallet              string
		createdAt           time.Time
	}
	return processBatched(ctx, w, "muted_users",
		`SELECT count(*) FROM muted_users WHERE is_delete = false`,
		`SELECT m.user_id, m.muted_user_id, COALESCE(LOWER(u.wallet), ''), m.created_at
		FROM muted_users m
		LEFT JOIN (
			-- At most one row per user: the source contains a few user_ids with
			-- more than one is_current row, and joining users directly would
			-- duplicate every entity those users own.
			SELECT DISTINCT ON (user_id) user_id, wallet FROM users
			WHERE is_current = true ORDER BY user_id, blocknumber DESC NULLS LAST
		) u ON u.user_id = m.user_id
		WHERE m.is_delete = false
		ORDER BY m.user_id, m.muted_user_id`,
		func(rows pgx.Rows) (mutedUser, error) {
			var m mutedUser
			err := rows.Scan(&m.userID, &m.mutedUserID, &m.wallet, &m.createdAt)
			return m, err
		},
		func(ctx context.Context, m mutedUser) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(m.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal muted user metadata: %w", err)
			}
			return w.addManageEntityWithSigner(ctx, &corev1.ManageEntityLegacy{
				UserId:     m.userID,
				EntityType: "MutedUser",
				EntityId:   m.mutedUserID,
				Action:     "Mute",
				Metadata:   string(metaJSON),
			}, m.wallet)
		},
	)
}

// saveRepostEntityType maps save_type / repost_type DB strings to DP entity type names.
func saveRepostEntityType(t string) string {
	switch t {
	case "track":
		return "Track"
	case "playlist", "album":
		return "Playlist"
	default:
		return "Track"
	}
}
