package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

func TestCommentPin_TxType(t *testing.T) {
	h := CommentPin()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionPin {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionPin)
	}
}

func TestCommentPin_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 50)
	seedUser(t, pool, uid, "0xowner", "owner")
	seedTrackFull(t, pool, trackID, uid, "My Track")

	createMeta := fmt.Sprintf(`{"body":"Pin me","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xOwner", createMeta))

	pinMeta := fmt.Sprintf(`{"entity_id":%d}`, trackID)
	mustHandle(t, CommentPin(), buildParams(t, pool, EntityTypeComment, ActionPin, uid, commentID, "0xOwner", pinMeta))

	var pinnedID *int64
	err := pool.QueryRow(context.Background(),
		"SELECT pinned_comment_id FROM tracks WHERE track_id = $1 AND is_current = true", trackID).Scan(&pinnedID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if pinnedID == nil || *pinnedID != commentID {
		t.Errorf("pinned_comment_id = %v, want %d", pinnedID, commentID)
	}
}

func TestCommentPin_RejectsNonOwner(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 51)
	seedUser(t, pool, uid, "0xowner", "owner")
	seedUser(t, pool, uid2, "0xother", "other")
	seedTrackFull(t, pool, trackID, uid, "My Track")

	createMeta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid2, commentID, "0xOther", createMeta))

	pinMeta := fmt.Sprintf(`{"entity_id":%d}`, trackID)
	params := buildParams(t, pool, EntityTypeComment, ActionPin, uid2, commentID, "0xOther", pinMeta)
	mustReject(t, CommentPin(), params, "only track owner")
}

func TestCommentUnpin_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 52)
	seedUser(t, pool, uid, "0xowner", "owner")
	seedTrackFull(t, pool, trackID, uid, "My Track")

	createMeta := fmt.Sprintf(`{"body":"Pin then unpin","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xOwner", createMeta))

	pinMeta := fmt.Sprintf(`{"entity_id":%d}`, trackID)
	mustHandle(t, CommentPin(), buildParams(t, pool, EntityTypeComment, ActionPin, uid, commentID, "0xOwner", pinMeta))
	mustHandle(t, CommentUnpin(), buildParams(t, pool, EntityTypeComment, ActionUnpin, uid, commentID, "0xOwner", pinMeta))

	var pinnedID *int64
	err := pool.QueryRow(context.Background(),
		"SELECT pinned_comment_id FROM tracks WHERE track_id = $1 AND is_current = true", trackID).Scan(&pinnedID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if pinnedID != nil {
		t.Errorf("expected pinned_comment_id = nil, got %d", *pinnedID)
	}
}

func TestCommentPin_RejectsAlreadyPinned(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 53)
	seedUser(t, pool, uid, "0xowner", "owner")
	seedTrackFull(t, pool, trackID, uid, "My Track")

	createMeta := fmt.Sprintf(`{"body":"test","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xOwner", createMeta))

	pinMeta := fmt.Sprintf(`{"entity_id":%d}`, trackID)
	mustHandle(t, CommentPin(), buildParams(t, pool, EntityTypeComment, ActionPin, uid, commentID, "0xOwner", pinMeta))

	params := buildParams(t, pool, EntityTypeComment, ActionPin, uid, commentID, "0xOwner", pinMeta)
	mustReject(t, CommentPin(), params, "already pinned")
}
