package entity_manager

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCommentMute_TxType(t *testing.T) {
	h := CommentMute()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionMute {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionMute)
	}
}

func TestCommentMute_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	commentID := int64(CommentIDOffset + 70)
	seedUser(t, pool, uid, "0xmuter", "muter")

	mustHandle(t, CommentMute(), buildParams(t, pool, EntityTypeComment, ActionMute, uid, commentID, "0xMuter", `{}`))

	var isMuted bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_muted FROM comment_notification_settings WHERE user_id = $1 AND entity_id = $2", uid, commentID).Scan(&isMuted)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isMuted {
		t.Error("expected is_muted = true")
	}
}

func TestCommentUnmute_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	commentID := int64(CommentIDOffset + 71)
	seedUser(t, pool, uid, "0xmuter", "muter")

	mustHandle(t, CommentMute(), buildParams(t, pool, EntityTypeComment, ActionMute, uid, commentID, "0xMuter", `{}`))
	mustHandle(t, CommentUnmute(), buildParams(t, pool, EntityTypeComment, ActionUnmute, uid, commentID, "0xMuter", `{}`))

	var isMuted bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_muted FROM comment_notification_settings WHERE user_id = $1 AND entity_id = $2", uid, commentID).Scan(&isMuted)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isMuted {
		t.Error("expected is_muted = false")
	}
}

func TestCommentMute_RejectsWrongSigner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	commentID := int64(CommentIDOffset + 72)
	seedUser(t, pool, uid, "0xmuter", "muter")

	mustReject(t, CommentMute(), buildParams(t, pool, EntityTypeComment, ActionMute, uid, commentID, "0xStranger", `{}`), "not authorized")
	assertNoNotificationSetting(t, pool, uid, commentID)
}

func TestTrackMute_RejectsWrongSigner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xmuter", "muter")
	seedTrack(t, pool, trackID, uid)

	mustReject(t, TrackMute(), buildParams(t, pool, EntityTypeTrack, ActionMute, uid, trackID, "0xStranger", `{}`), "not authorized")
	mustReject(t, TrackUnmute(), buildParams(t, pool, EntityTypeTrack, ActionUnmute, uid, trackID, "0xStranger", `{}`), "not authorized")
	assertNoNotificationSetting(t, pool, uid, trackID)
}

func TestTrackMute_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xmuter", "muter")
	seedTrack(t, pool, trackID, uid)

	mustHandle(t, TrackMute(), buildParams(t, pool, EntityTypeTrack, ActionMute, uid, trackID, "0xMuter", `{}`))

	var isMuted bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_muted FROM comment_notification_settings WHERE user_id = $1 AND entity_id = $2", uid, trackID).Scan(&isMuted)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isMuted {
		t.Error("expected is_muted = true")
	}
}

func assertNoNotificationSetting(t *testing.T, pool *pgxpool.Pool, userID, entityID int64) {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM comment_notification_settings WHERE user_id = $1 AND entity_id = $2", userID, entityID).Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Errorf("found %d comment_notification_settings rows, want 0", n)
	}
}
