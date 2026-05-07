package entity_manager

import (
	"context"
	"testing"
)

// Tests for handle_comment, handle_event, handle_share triggers ported in
// migration 0020.

func TestTrigger_HandleComment_TicksCommentCount(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	commenter := int64(UserIDOffset + 300)
	owner := int64(UserIDOffset + 301)
	tid := int64(TrackIDOffset + 5000)
	cid := int64(CommentIDOffset + 100)
	seedUser(t, pool, commenter, "0xcommenter", "cmtu")
	seedUser(t, pool, owner, "0xowner", "ownr")
	seedTrackFull(t, pool, tid, owner, "Commented Song")

	meta := `{"comment_id":4000100,"body":"nice","entity_id":2005000,"entity_type":"Track"}`
	mustHandle(t, CommentCreate(),
		buildParams(t, pool, EntityTypeComment, ActionCreate, commenter, cid, "0xcommenter", meta))

	var commentCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT comment_count FROM aggregate_track WHERE track_id = $1", tid).Scan(&commentCount); err != nil {
		t.Fatalf("aggregate_track: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("aggregate_track.comment_count = %d, want 1", commentCount)
	}
}

func TestTrigger_HandleShare_TicksTrackShareCount(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	sharer := int64(UserIDOffset + 310)
	owner := int64(UserIDOffset + 311)
	tid := int64(TrackIDOffset + 5100)
	seedUser(t, pool, sharer, "0xsharer", "shu")
	seedUser(t, pool, owner, "0xshareown", "showu")
	seedTrackFull(t, pool, tid, owner, "Shared Song")

	mustHandle(t, Share(),
		buildParams(t, pool, EntityTypeTrack, ActionShare, sharer, tid, "0xsharer", `{}`))

	var trackShareCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT share_count FROM aggregate_track WHERE track_id = $1", tid).Scan(&trackShareCount); err != nil {
		t.Fatalf("aggregate_track: %v", err)
	}
	if trackShareCount != 1 {
		t.Errorf("aggregate_track.share_count = %d, want 1", trackShareCount)
	}

	var userShareCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT track_share_count FROM aggregate_user WHERE user_id = $1", sharer).Scan(&userShareCount); err != nil {
		t.Fatalf("aggregate_user: %v", err)
	}
	if userShareCount != 1 {
		t.Errorf("aggregate_user.track_share_count = %d, want 1", userShareCount)
	}
}

func TestTrigger_HandleEvent_RemixContestNotifiesFollowers(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	creator := int64(UserIDOffset + 320)
	follower := int64(UserIDOffset + 321)
	tid := int64(TrackIDOffset + 5200)
	eventID := int64(99000)

	seedUser(t, pool, creator, "0xcreator", "creau")
	seedUser(t, pool, follower, "0xfollA", "follA")
	seedTrackFull(t, pool, tid, creator, "Remix Parent")

	// follower follows creator
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO follows (follower_user_id, followee_user_id, is_current, is_delete, created_at, txhash, blocknumber)
		VALUES ($1, $2, true, false, now(), 'tx-fol', 100)
	`, follower, creator); err != nil {
		t.Fatalf("seed follow: %v", err)
	}

	// creator inserts a remix_contest event
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO events (event_id, event_type, user_id, entity_type, entity_id, end_date, is_deleted, created_at, updated_at, blocknumber, txhash, blockhash)
		VALUES ($1, 'remix_contest', $2, 'Track', $3, now() + interval '7 days', false, now(), now(), 100, 'tx-evt', 'bh-evt')
	`, eventID, creator, tid); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM notification WHERE type = 'fan_remix_contest_started' AND user_ids @> ARRAY[$1::int]",
		int(follower)).Scan(&count); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 fan_remix_contest_started notification for follower, got %d", count)
	}
}
