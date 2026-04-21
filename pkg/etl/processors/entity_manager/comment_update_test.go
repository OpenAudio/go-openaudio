package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

func TestCommentUpdate_TxType(t *testing.T) {
	h := CommentUpdate()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionUpdate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionUpdate)
	}
}

func TestCommentUpdate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 20)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"Original","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", createMeta))

	updateMeta := fmt.Sprintf(`{"body":"Updated text","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentUpdate(), buildParams(t, pool, EntityTypeComment, ActionUpdate, uid, commentID, "0xCommenter", updateMeta))

	var text string
	var isEdited bool
	err := pool.QueryRow(context.Background(),
		"SELECT text, is_edited FROM comments WHERE comment_id = $1", commentID).Scan(&text, &isEdited)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if text != "Updated text" {
		t.Errorf("text = %q, want %q", text, "Updated text")
	}
	if !isEdited {
		t.Error("expected is_edited = true")
	}
}

func TestCommentUpdate_RejectsNonexistent(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	meta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	params := buildParams(t, pool, EntityTypeComment, ActionUpdate, uid, int64(CommentIDOffset+999), "0xCommenter", meta)
	mustReject(t, CommentUpdate(), params, "does not exist")
}
