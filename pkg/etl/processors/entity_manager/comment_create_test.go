package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

func TestCommentCreate_TxType(t *testing.T) {
	h := CommentCreate()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestCommentCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 1)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	meta := fmt.Sprintf(`{"body":"Great track!","entity_id":%d,"entity_type":"Track","track_timestamp_s":30}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", meta))

	var text string
	var entityID int
	err := pool.QueryRow(context.Background(),
		"SELECT text, entity_id FROM comments WHERE comment_id = $1", commentID).Scan(&text, &entityID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if text != "Great track!" {
		t.Errorf("text = %q, want %q", text, "Great track!")
	}
	if int64(entityID) != trackID {
		t.Errorf("entity_id = %d, want %d", entityID, trackID)
	}
}

func TestCommentCreate_WithMentions(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 2)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedUser(t, pool, uid2, "0xmentioned", "mentioned")
	seedTrack(t, pool, trackID, uid)

	meta := fmt.Sprintf(`{"body":"Hey @mentioned!","entity_id":%d,"entity_type":"Track","track_timestamp_s":0,"mentions":[%d]}`, trackID, uid2)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", meta))

	var mentionCount int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM comment_mentions WHERE comment_id = $1", commentID).Scan(&mentionCount)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if mentionCount != 1 {
		t.Errorf("mention count = %d, want 1", mentionCount)
	}
}

func TestCommentCreate_WithThread(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	parentID := int64(CommentIDOffset + 10)
	childID := int64(CommentIDOffset + 11)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	// Create parent comment
	parentMeta := fmt.Sprintf(`{"body":"Parent comment","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, parentID, "0xCommenter", parentMeta))

	// Create reply
	childMeta := fmt.Sprintf(`{"body":"Reply","entity_id":%d,"entity_type":"Track","track_timestamp_s":0,"parent_comment_id":%d}`, trackID, parentID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, childID, "0xCommenter", childMeta))

	var threadCount int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM comment_threads WHERE parent_comment_id = $1 AND comment_id = $2", parentID, childID).Scan(&threadCount)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if threadCount != 1 {
		t.Errorf("thread count = %d, want 1", threadCount)
	}
}

func TestCommentCreate_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 3)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	meta := fmt.Sprintf(`{"body":"First!","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", meta))

	params := buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", meta)
	mustReject(t, CommentCreate(), params, "already exists")
}

func TestCommentCreate_RejectsEmptyBody(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	meta := fmt.Sprintf(`{"body":"","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	params := buildParams(t, pool, EntityTypeComment, ActionCreate, uid, int64(CommentIDOffset+4), "0xCommenter", meta)
	mustReject(t, CommentCreate(), params, "body is empty")
}

func TestCommentCreate_RejectsNonexistentTrack(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xcommenter", "commenter")

	meta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, TrackIDOffset+999)
	params := buildParams(t, pool, EntityTypeComment, ActionCreate, uid, int64(CommentIDOffset+5), "0xCommenter", meta)
	mustReject(t, CommentCreate(), params, "does not exist")
}

func TestCommentCreate_RejectsBodyTooLong(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedTrack(t, pool, trackID, uid)

	longBody := make([]byte, CharacterLimitCommentBody+1)
	for i := range longBody {
		longBody[i] = 'a'
	}
	meta := fmt.Sprintf(`{"body":"%s","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, string(longBody), trackID)
	params := buildParams(t, pool, EntityTypeComment, ActionCreate, uid, int64(CommentIDOffset+6), "0xCommenter", meta)
	mustReject(t, CommentCreate(), params, "character limit")
}
