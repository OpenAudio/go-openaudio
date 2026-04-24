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

func fmtCreatedAt(t time.Time) string {
	return t.Format(time.RFC3339)
}

// --- Follows ---

func (w *Writer) writeFollows(ctx context.Context) error {
	type follow struct {
		follower, followee int64
		createdAt          time.Time
	}
	return processBatched(ctx, w, "follows",
		`SELECT count(*) FROM follows WHERE is_current = true AND is_delete = false`,
		`SELECT follower_user_id, followee_user_id, created_at
		FROM follows
		WHERE is_current = true AND is_delete = false
		ORDER BY follower_user_id, followee_user_id`,
		func(rows pgx.Rows) (follow, error) {
			var f follow
			err := rows.Scan(&f.follower, &f.followee, &f.createdAt)
			return f, err
		},
		func(ctx context.Context, f follow) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(f.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal follow metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     f.follower,
				EntityType: "User",
				EntityId:   f.followee,
				Action:     "Follow",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Saves ---

func (w *Writer) writeSaves(ctx context.Context) error {
	type save struct {
		userID, itemID int64
		saveType       string
		createdAt      time.Time
	}
	return processBatched(ctx, w, "saves",
		`SELECT count(*) FROM saves WHERE is_current = true AND is_delete = false`,
		`SELECT user_id, save_item_id, save_type, created_at
		FROM saves
		WHERE is_current = true AND is_delete = false
		ORDER BY user_id, save_item_id`,
		func(rows pgx.Rows) (save, error) {
			var s save
			err := rows.Scan(&s.userID, &s.itemID, &s.saveType, &s.createdAt)
			return s, err
		},
		func(ctx context.Context, s save) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(s.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal save metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.userID,
				EntityType: saveRepostEntityType(s.saveType),
				EntityId:   s.itemID,
				Action:     "Save",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Reposts ---

func (w *Writer) writeReposts(ctx context.Context) error {
	type repost struct {
		userID, itemID int64
		repostType     string
		createdAt      time.Time
	}
	return processBatched(ctx, w, "reposts",
		`SELECT count(*) FROM reposts WHERE is_current = true AND is_delete = false`,
		`SELECT user_id, repost_item_id, repost_type, created_at
		FROM reposts
		WHERE is_current = true AND is_delete = false
		ORDER BY user_id, repost_item_id`,
		func(rows pgx.Rows) (repost, error) {
			var r repost
			err := rows.Scan(&r.userID, &r.itemID, &r.repostType, &r.createdAt)
			return r, err
		},
		func(ctx context.Context, r repost) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(r.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal repost metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     r.userID,
				EntityType: saveRepostEntityType(r.repostType),
				EntityId:   r.itemID,
				Action:     "Repost",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Shares ---

func (w *Writer) writeShares(ctx context.Context) error {
	type share struct {
		userID    int64
		itemID    int64
		shareType string
		createdAt time.Time
	}
	return processBatched(ctx, w, "shares",
		`SELECT count(*) FROM shares`,
		`SELECT user_id, share_item_id, share_type, created_at
		FROM shares
		ORDER BY user_id, share_item_id`,
		func(rows pgx.Rows) (share, error) {
			var s share
			err := rows.Scan(&s.userID, &s.itemID, &s.shareType, &s.createdAt)
			return s, err
		},
		func(ctx context.Context, s share) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(s.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal share metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.userID,
				EntityType: saveRepostEntityType(s.shareType),
				EntityId:   s.itemID,
				Action:     "Share",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Subscriptions ---

func (w *Writer) writeSubscriptions(ctx context.Context) error {
	type subscription struct {
		subscriberID, userID int64
		createdAt            time.Time
	}
	return processBatched(ctx, w, "subscriptions",
		`SELECT count(*) FROM subscriptions WHERE is_current = true AND is_delete = false`,
		`SELECT subscriber_id, user_id, created_at
		FROM subscriptions
		WHERE is_current = true AND is_delete = false
		ORDER BY subscriber_id, user_id`,
		func(rows pgx.Rows) (subscription, error) {
			var s subscription
			err := rows.Scan(&s.subscriberID, &s.userID, &s.createdAt)
			return s, err
		},
		func(ctx context.Context, s subscription) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(s.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal subscription metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     s.subscriberID,
				EntityType: "User",
				EntityId:   s.userID,
				Action:     "Subscribe",
				Metadata:   string(metaJSON),
			})
		},
	)
}

// --- Muted Users ---

func (w *Writer) writeMutedUsers(ctx context.Context) error {
	type mutedUser struct {
		userID, mutedUserID int64
		createdAt           time.Time
	}
	return processBatched(ctx, w, "muted_users",
		`SELECT count(*) FROM muted_users WHERE is_delete = false`,
		`SELECT user_id, muted_user_id, created_at
		FROM muted_users
		WHERE is_delete = false
		ORDER BY user_id, muted_user_id`,
		func(rows pgx.Rows) (mutedUser, error) {
			var m mutedUser
			err := rows.Scan(&m.userID, &m.mutedUserID, &m.createdAt)
			return m, err
		},
		func(ctx context.Context, m mutedUser) error {
			metaJSON, err := json.Marshal(createdAtMeta{CreatedAt: fmtCreatedAt(m.createdAt)})
			if err != nil {
				return fmt.Errorf("marshal muted user metadata: %w", err)
			}
			return w.addManageEntity(ctx, &corev1.ManageEntityLegacy{
				UserId:     m.userID,
				EntityType: "MutedUser",
				EntityId:   m.mutedUserID,
				Action:     "Mute",
				Metadata:   string(metaJSON),
			})
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
