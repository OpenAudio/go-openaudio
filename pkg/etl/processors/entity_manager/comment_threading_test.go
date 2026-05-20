package entity_manager

import (
	"context"
	"testing"
)

func TestCommentCreate_AcceptsParentIDAltKey(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 1100)
	owner := int64(UserIDOffset + 1101)
	tid := int64(TrackIDOffset + 11000)
	parentCID := int64(CommentIDOffset + 1000)
	childCID := int64(CommentIDOffset + 1001)
	seedUser(t, pool, uid, "0xcm1", "cm1u")
	seedUser(t, pool, owner, "0xcm2", "cm2u")
	seedTrackFull(t, pool, tid, owner, "Threaded")

	// Insert parent comment via the handler.
	parentMeta := `{"comment_id":4001000,"body":"top-level","entity_id":2011000,"entity_type":"Track"}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, uid, parentCID, "0xcm1", parentMeta))

	// Reply using parent_id (alt key) instead of parent_comment_id.
	childMeta := `{"comment_id":4001001,"body":"reply","entity_id":2011000,"entity_type":"Track","parent_id":4001000}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, uid, childCID, "0xcm1", childMeta))

	var threadCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM comment_threads WHERE parent_comment_id = $1 AND comment_id = $2",
		parentCID, childCID).Scan(&threadCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if threadCount != 1 {
		t.Errorf("expected 1 comment_threads row, got %d", threadCount)
	}
}

func TestCommentCreate_RejectsParentOnDifferentEntity(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 1110)
	owner := int64(UserIDOffset + 1111)
	tidA := int64(TrackIDOffset + 11100)
	tidB := int64(TrackIDOffset + 11101)
	parentCID := int64(CommentIDOffset + 1100)
	childCID := int64(CommentIDOffset + 1101)

	seedUser(t, pool, uid, "0xcma", "cmau")
	seedUser(t, pool, owner, "0xcmb", "cmbu")
	seedTrackFull(t, pool, tidA, owner, "TrackA")
	seedTrackFull(t, pool, tidB, owner, "TrackB")

	parentMeta := `{"comment_id":4001100,"body":"on TrackA","entity_id":2011100,"entity_type":"Track"}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, uid, parentCID, "0xcma", parentMeta))

	// Reply attempting to reference parent on TrackA but child claims to be on TrackB.
	childMeta := `{"comment_id":4001101,"body":"reply","entity_id":2011101,"entity_type":"Track","parent_comment_id":4001100}`
	mustReject(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, uid, childCID, "0xcma", childMeta),
		"does not belong to Track")
}
