package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

func TestCommentReact_TxType(t *testing.T) {
	h := CommentReact()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionReact {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionReact)
	}
}

func TestCommentReact_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 40)
	seedUser(t, pool, uid, "0xreacter", "reacter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"Like this","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xReacter", createMeta))

	mustHandle(t, CommentReact(), buildParams(t, pool, EntityTypeComment, ActionReact, uid, commentID, "0xReacter", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM comment_reactions WHERE comment_id = $1 AND user_id = $2", commentID, uid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestCommentReact_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 41)
	seedUser(t, pool, uid, "0xreacter", "reacter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xReacter", createMeta))
	mustHandle(t, CommentReact(), buildParams(t, pool, EntityTypeComment, ActionReact, uid, commentID, "0xReacter", `{}`))

	params := buildParams(t, pool, EntityTypeComment, ActionReact, uid, commentID, "0xReacter", `{}`)
	mustReject(t, CommentReact(), params, "already reacted")
}

func TestCommentReact_RejectsNonexistentComment(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xreacter", "reacter")

	params := buildParams(t, pool, EntityTypeComment, ActionReact, uid, int64(CommentIDOffset+999), "0xReacter", `{}`)
	mustReject(t, CommentReact(), params, "does not exist")
}

func TestCommentUnreact_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 42)
	seedUser(t, pool, uid, "0xreacter", "reacter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xReacter", createMeta))
	mustHandle(t, CommentReact(), buildParams(t, pool, EntityTypeComment, ActionReact, uid, commentID, "0xReacter", `{}`))
	mustHandle(t, CommentUnreact(), buildParams(t, pool, EntityTypeComment, ActionUnreact, uid, commentID, "0xReacter", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM comment_reactions WHERE comment_id = $1 AND user_id = $2", commentID, uid).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestCommentUnreact_RejectsNoReaction(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 43)
	seedUser(t, pool, uid, "0xreacter", "reacter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xReacter", createMeta))

	params := buildParams(t, pool, EntityTypeComment, ActionUnreact, uid, commentID, "0xReacter", `{}`)
	mustReject(t, CommentUnreact(), params, "has not reacted")
}
