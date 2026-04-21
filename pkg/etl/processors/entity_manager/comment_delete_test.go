package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

func TestCommentDelete_TxType(t *testing.T) {
	h := CommentDelete()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionDelete {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionDelete)
	}
}

func TestCommentDelete_ByOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 30)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"To delete","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", createMeta))

	mustHandle(t, CommentDelete(), buildParams(t, pool, EntityTypeComment, ActionDelete, uid, commentID, "0xCommenter", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM comments WHERE comment_id = $1", commentID).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestCommentDelete_ByTrackOwner(t *testing.T) {
	pool := setupTestDB(t)
	trackOwner := int64(UserIDOffset + 1)
	commenter := int64(UserIDOffset + 2)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 31)
	seedUser(t, pool, trackOwner, "0xowner", "owner")
	seedUser(t, pool, commenter, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, trackOwner)

	createMeta := fmt.Sprintf(`{"body":"Someone else's comment","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, commenter, commentID, "0xCommenter", createMeta))

	// Track owner can delete
	mustHandle(t, CommentDelete(), buildParams(t, pool, EntityTypeComment, ActionDelete, trackOwner, commentID, "0xOwner", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM comments WHERE comment_id = $1", commentID).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestCommentDelete_RejectsNonOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	uid3 := int64(UserIDOffset + 3)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 32)
	seedUser(t, pool, uid, "0xowner", "owner")
	seedUser(t, pool, uid2, "0xcommenter", "commenter")
	seedUser(t, pool, uid3, "0xrandom", "random")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"My comment","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid2, commentID, "0xCommenter", createMeta))

	// Random user cannot delete
	params := buildParams(t, pool, EntityTypeComment, ActionDelete, uid3, commentID, "0xRandom", `{}`)
	mustReject(t, CommentDelete(), params, "only comment owner or track owner")
}

func TestCommentDelete_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xcommenter", "commenter")

	params := buildParams(t, pool, EntityTypeComment, ActionDelete, uid, int64(CommentIDOffset+999), "0xCommenter", `{}`)
	mustReject(t, CommentDelete(), params, "does not exist")
}
