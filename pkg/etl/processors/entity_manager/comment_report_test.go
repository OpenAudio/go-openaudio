package entity_manager

import (
	"fmt"
	"testing"
)

func TestCommentReport_TxType(t *testing.T) {
	h := CommentReport()
	if h.EntityType() != EntityTypeComment {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeComment)
	}
	if h.Action() != ActionReport {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionReport)
	}
}

func TestCommentReport_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 60)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedUser(t, pool, uid2, "0xreporter", "reporter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"Reportable","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", createMeta))

	mustHandle(t, CommentReport(), buildParams(t, pool, EntityTypeComment, ActionReport, uid2, commentID, "0xReporter", `{}`))
}

func TestCommentReport_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 61)
	seedUser(t, pool, uid, "0xcommenter", "commenter")
	seedUser(t, pool, uid2, "0xreporter", "reporter")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"Reportable","entity_id":%d,"entity_type":"Track","track_timestamp_s":0}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter", createMeta))

	mustHandle(t, CommentReport(), buildParams(t, pool, EntityTypeComment, ActionReport, uid2, commentID, "0xReporter", `{}`))

	params := buildParams(t, pool, EntityTypeComment, ActionReport, uid2, commentID, "0xReporter", `{}`)
	mustReject(t, CommentReport(), params, "already reported")
}
