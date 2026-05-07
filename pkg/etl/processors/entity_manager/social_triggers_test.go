package entity_manager

import (
	"context"
	"strconv"
	"testing"
)

// These tests verify the social-action triggers ported in migration 0018:
// handle_follow, handle_save, handle_repost. The Go handlers insert rows into
// follows/saves/reposts; the triggers should then maintain aggregate_user,
// aggregate_track, aggregate_playlist, milestones, and notification rows.

func TestTrigger_HandleFollow_TicksAggregateCounts(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	follower := int64(UserIDOffset + 10)
	followee := int64(UserIDOffset + 11)
	seedUser(t, pool, follower, "0xfollower", "f1")
	seedUser(t, pool, followee, "0xfollowee", "f2")

	mustHandle(t, Follow(), buildParams(t, pool, EntityTypeUser, ActionFollow, follower, followee, "0xfollower", `{}`))

	var follFollowerCount, follFollowingCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT follower_count, following_count FROM aggregate_user WHERE user_id = $1", follower).Scan(&follFollowerCount, &follFollowingCount); err != nil {
		t.Fatalf("agg follower: %v", err)
	}
	if follFollowingCount != 1 {
		t.Errorf("follower's following_count = %d, want 1", follFollowingCount)
	}

	var followeeFollowerCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT follower_count FROM aggregate_user WHERE user_id = $1", followee).Scan(&followeeFollowerCount); err != nil {
		t.Fatalf("agg followee: %v", err)
	}
	if followeeFollowerCount != 1 {
		t.Errorf("followee's follower_count = %d, want 1", followeeFollowerCount)
	}
}

func TestTrigger_HandleFollow_CreatesNotification(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	follower := int64(UserIDOffset + 20)
	followee := int64(UserIDOffset + 21)
	seedUser(t, pool, follower, "0xfA", "fA")
	seedUser(t, pool, followee, "0xfB", "fB")

	mustHandle(t, Follow(), buildParams(t, pool, EntityTypeUser, ActionFollow, follower, followee, "0xfA", `{}`))

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM notification WHERE type = 'follow' AND specifier = $1",
		strconv.FormatInt(follower, 10)).Scan(&count); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 follow notification, got %d", count)
	}
}

func TestTrigger_HandleSave_TicksTrackSaveCount(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	saver := int64(UserIDOffset + 30)
	owner := int64(UserIDOffset + 31)
	tid := int64(TrackIDOffset + 1000)
	seedUser(t, pool, saver, "0xsaver", "saver")
	seedUser(t, pool, owner, "0xowner", "owner")
	seedTrackFull(t, pool, tid, owner, "Saved Song")

	mustHandle(t, Save(), buildParams(t, pool, EntityTypeTrack, ActionSave, saver, tid, "0xsaver", `{}`))

	var saveCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT save_count FROM aggregate_track WHERE track_id = $1", tid).Scan(&saveCount); err != nil {
		t.Fatalf("agg track: %v", err)
	}
	if saveCount != 1 {
		t.Errorf("aggregate_track.save_count = %d, want 1", saveCount)
	}

	var trackSaveCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT track_save_count FROM aggregate_user WHERE user_id = $1", saver).Scan(&trackSaveCount); err != nil {
		t.Fatalf("agg user: %v", err)
	}
	if trackSaveCount != 1 {
		t.Errorf("aggregate_user.track_save_count = %d, want 1", trackSaveCount)
	}
}

func TestTrigger_HandleSave_CreatesNotification(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	saver := int64(UserIDOffset + 40)
	owner := int64(UserIDOffset + 41)
	tid := int64(TrackIDOffset + 1100)
	seedUser(t, pool, saver, "0xsaverB", "sB")
	seedUser(t, pool, owner, "0xownerB", "oB")
	seedTrackFull(t, pool, tid, owner, "Notif Song")

	mustHandle(t, Save(), buildParams(t, pool, EntityTypeTrack, ActionSave, saver, tid, "0xsaverB", `{}`))

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM notification WHERE type = 'save' AND specifier = $1",
		strconv.FormatInt(saver, 10)).Scan(&count); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 save notification, got %d", count)
	}
}

func TestTrigger_HandleRepost_TicksRepostCounts(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	reposter := int64(UserIDOffset + 50)
	owner := int64(UserIDOffset + 51)
	tid := int64(TrackIDOffset + 1200)
	seedUser(t, pool, reposter, "0xreposter", "rp")
	seedUser(t, pool, owner, "0xtrackowner", "to")
	seedTrackFull(t, pool, tid, owner, "Reposted Song")

	mustHandle(t, Repost(), buildParams(t, pool, EntityTypeTrack, ActionRepost, reposter, tid, "0xreposter", `{}`))

	var trackRepostCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT repost_count FROM aggregate_track WHERE track_id = $1", tid).Scan(&trackRepostCount); err != nil {
		t.Fatalf("agg track: %v", err)
	}
	if trackRepostCount != 1 {
		t.Errorf("aggregate_track.repost_count = %d, want 1", trackRepostCount)
	}

	var userRepostCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT repost_count FROM aggregate_user WHERE user_id = $1", reposter).Scan(&userRepostCount); err != nil {
		t.Fatalf("agg user: %v", err)
	}
	if userRepostCount != 1 {
		t.Errorf("aggregate_user.repost_count = %d, want 1", userRepostCount)
	}
}

func TestTrigger_HandleUnfollow_DecrementsCounts(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	follower := int64(UserIDOffset + 60)
	followee := int64(UserIDOffset + 61)
	seedUser(t, pool, follower, "0xfollX", "fX")
	seedUser(t, pool, followee, "0xfollY", "fY")

	mustHandle(t, Follow(), buildParams(t, pool, EntityTypeUser, ActionFollow, follower, followee, "0xfollX", `{}`))
	mustHandle(t, Unfollow(), buildParams(t, pool, EntityTypeUser, ActionUnfollow, follower, followee, "0xfollX", `{}`))

	var followingCount int
	if err := pool.QueryRow(context.Background(),
		"SELECT following_count FROM aggregate_user WHERE user_id = $1", follower).Scan(&followingCount); err != nil {
		t.Fatalf("agg follower: %v", err)
	}
	if followingCount != 0 {
		t.Errorf("after unfollow, following_count = %d, want 0", followingCount)
	}
}
