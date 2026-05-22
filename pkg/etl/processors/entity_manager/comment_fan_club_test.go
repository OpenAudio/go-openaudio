package entity_manager

import (
	"context"
	"strings"
	"testing"
)

// TestCommentCreate_FanClubMembersOnly verifies that an is_members_only=true
// comment on a FanClub entity persists with both the flag and an attached
// video_url. Mirrors apps' fan-club text-post flow (PRs #14029, #14080).
func TestCommentCreate_FanClubMembersOnly(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	author := int64(UserIDOffset + 7000)
	target := int64(UserIDOffset + 7001)
	cid := int64(CommentIDOffset + 7000)
	seedUser(t, pool, author, "0xfc1", "fc1u")
	seedUser(t, pool, target, "0xfc2", "fc2u")

	// FanClub entity_id is the target user id; the trackExists check is
	// skipped because entity_type != "Track".
	meta := `{
		"comment_id":4007000,
		"body":"members only",
		"entity_id":3007001,
		"entity_type":"FanClub",
		"is_members_only":true,
		"video_url":"https://example.com/post.mp4"
	}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, author, cid, "0xfc1", meta))

	var isMembersOnly bool
	var videoURL *string
	if err := pool.QueryRow(context.Background(),
		"SELECT is_members_only, video_url FROM comments WHERE comment_id = $1",
		cid).Scan(&isMembersOnly, &videoURL); err != nil {
		t.Fatalf("query comment: %v", err)
	}
	if !isMembersOnly {
		t.Error("is_members_only should be true")
	}
	if videoURL == nil || *videoURL != "https://example.com/post.mp4" {
		t.Errorf("video_url = %v, want https://example.com/post.mp4", videoURL)
	}
}

// TestCommentCreate_RejectsMembersOnlyOnTrack — is_members_only is only
// meaningful for FanClub comments; setting it on a Track comment must error.
func TestCommentCreate_RejectsMembersOnlyOnTrack(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	author := int64(UserIDOffset + 7010)
	owner := int64(UserIDOffset + 7011)
	tid := int64(TrackIDOffset + 7010)
	cid := int64(CommentIDOffset + 7010)
	seedUser(t, pool, author, "0xfc3", "fc3u")
	seedUser(t, pool, owner, "0xfc4", "fc4u")
	seedTrackFull(t, pool, tid, owner, "Tk")

	meta := `{
		"comment_id":4007010,
		"body":"nope",
		"entity_id":2007010,
		"entity_type":"Track",
		"is_members_only":true
	}`
	mustReject(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, author, cid, "0xfc3", meta),
		"FanClub")
}

// TestCommentCreate_RejectsOverlongVideoURL caps video_url at 2000 chars.
func TestCommentCreate_RejectsOverlongVideoURL(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	author := int64(UserIDOffset + 7020)
	target := int64(UserIDOffset + 7021)
	cid := int64(CommentIDOffset + 7020)
	seedUser(t, pool, author, "0xfc5", "fc5u")
	seedUser(t, pool, target, "0xfc6", "fc6u")

	longURL := "https://example.com/" + strings.Repeat("a", 2000)
	meta := `{
		"comment_id":4007020,
		"body":"x",
		"entity_id":3007021,
		"entity_type":"FanClub",
		"video_url":"` + longURL + `"
	}`
	mustReject(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, author, cid, "0xfc5", meta),
		"video_url")
}

// TestCommentCreate_FanClubVideoURLOnly — video_url without is_members_only is
// allowed (and lands) for FanClub entities. Default for is_members_only
// stays false.
func TestCommentCreate_FanClubVideoURLOnly(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	author := int64(UserIDOffset + 7030)
	target := int64(UserIDOffset + 7031)
	cid := int64(CommentIDOffset + 7030)
	seedUser(t, pool, author, "0xfc7", "fc7u")
	seedUser(t, pool, target, "0xfc8", "fc8u")

	meta := `{
		"comment_id":4007030,
		"body":"public text post",
		"entity_id":3007031,
		"entity_type":"FanClub",
		"video_url":"https://cdn.example.com/v.mp4"
	}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, author, cid, "0xfc7", meta))

	var isMembersOnly bool
	var videoURL *string
	if err := pool.QueryRow(context.Background(),
		"SELECT is_members_only, video_url FROM comments WHERE comment_id = $1",
		cid).Scan(&isMembersOnly, &videoURL); err != nil {
		t.Fatalf("query: %v", err)
	}
	if isMembersOnly {
		t.Error("is_members_only should default to false when omitted")
	}
	if videoURL == nil || *videoURL != "https://cdn.example.com/v.mp4" {
		t.Errorf("video_url = %v, want https://cdn.example.com/v.mp4", videoURL)
	}
}

// TestCommentCreate_NonFanClubLeavesMembersOnlyFalse — even without an
// is_members_only key in metadata, a Track comment should persist
// is_members_only=false (column default).
func TestCommentCreate_NonFanClubDefaultsFalse(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	author := int64(UserIDOffset + 7040)
	owner := int64(UserIDOffset + 7041)
	tid := int64(TrackIDOffset + 7040)
	cid := int64(CommentIDOffset + 7040)
	seedUser(t, pool, author, "0xfc9", "fc9u")
	seedUser(t, pool, owner, "0xfca", "fcau")
	seedTrackFull(t, pool, tid, owner, "Tk")

	meta := `{
		"comment_id":4007040,
		"body":"plain track comment",
		"entity_id":2007040,
		"entity_type":"Track"
	}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, author, cid, "0xfc9", meta))

	var isMembersOnly bool
	var videoURL *string
	if err := pool.QueryRow(context.Background(),
		"SELECT is_members_only, video_url FROM comments WHERE comment_id = $1",
		cid).Scan(&isMembersOnly, &videoURL); err != nil {
		t.Fatalf("query: %v", err)
	}
	if isMembersOnly {
		t.Error("is_members_only should default to false on Track comments")
	}
	if videoURL != nil {
		t.Errorf("video_url should be NULL, got %v", *videoURL)
	}
}
