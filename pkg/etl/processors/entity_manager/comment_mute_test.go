package entity_manager

import (
	"context"
	"testing"
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
