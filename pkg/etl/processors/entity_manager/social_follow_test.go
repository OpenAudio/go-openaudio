package entity_manager

import (
	"context"
	"testing"
)

func TestFollow_TxType(t *testing.T) {
	h := Follow()
	if h.EntityType() != EntityTypeAny {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeAny)
	}
	if h.Action() != ActionFollow {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionFollow)
	}
}

func TestFollow_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xfollower", "follower")
	seedUser(t, pool, uid2, "0xfollowee", "followee")

	params := buildParams(t, pool, EntityTypeUser, ActionFollow, uid1, uid2, "0xFollower", `{}`)
	mustHandle(t, Follow(), params)

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM follows WHERE follower_user_id = $1 AND followee_user_id = $2 AND is_current = true",
		uid1, uid2).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestFollow_RejectsSelfFollow(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xself", "self")
	params := buildParams(t, pool, EntityTypeUser, ActionFollow, uid, uid, "0xSelf", `{}`)
	mustReject(t, Follow(), params, "cannot follow themselves")
}

func TestFollow_RejectsNonexistentFollowee(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xfollower", "follower")
	params := buildParams(t, pool, EntityTypeUser, ActionFollow, uid, UserIDOffset+999, "0xFollower", `{}`)
	mustReject(t, Follow(), params, "does not exist")
}

func TestFollow_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xfollower", "follower")
	seedUser(t, pool, uid2, "0xfollowee", "followee")

	params := buildParams(t, pool, EntityTypeUser, ActionFollow, uid1, uid2, "0xFollower", `{}`)
	mustHandle(t, Follow(), params)

	params2 := buildParams(t, pool, EntityTypeUser, ActionFollow, uid1, uid2, "0xFollower", `{}`)
	mustReject(t, Follow(), params2, "already exists")
}

func TestUnfollow_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xfollower", "follower")
	seedUser(t, pool, uid2, "0xfollowee", "followee")

	mustHandle(t, Follow(), buildParams(t, pool, EntityTypeUser, ActionFollow, uid1, uid2, "0xFollower", `{}`))
	mustHandle(t, Unfollow(), buildParams(t, pool, EntityTypeUser, ActionUnfollow, uid1, uid2, "0xFollower", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM follows WHERE follower_user_id = $1 AND followee_user_id = $2 AND is_current = true",
		uid1, uid2).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestUnfollow_RejectsNoActiveFollow(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 1)
	uid2 := int64(UserIDOffset + 2)
	seedUser(t, pool, uid1, "0xfollower", "follower")
	seedUser(t, pool, uid2, "0xfollowee", "followee")
	params := buildParams(t, pool, EntityTypeUser, ActionUnfollow, uid1, uid2, "0xFollower", `{}`)
	mustReject(t, Unfollow(), params, "no active follow")
}

func TestFollow_AutoSubscribeIgnoresCollidingEventSubscription(t *testing.T) {
	pool := setupTestDB(t)
	followerID := int64(UserIDOffset + 40)
	// The followee's user id and the event's id collide on purpose: the
	// auto-subscribe upsert must key on entity_type and never land on the
	// Event row — following or unfollowing the user leaves it untouched.
	collidingID := int64(UserIDOffset + 41)
	hostID := int64(UserIDOffset + 42)

	seedUser(t, pool, followerID, "0xafollower", "afollower")
	seedUser(t, pool, collidingID, "0xafollowee", "afollowee")
	seedUser(t, pool, hostID, "0xahost", "ahost")
	seedEvent(t, pool, collidingID, hostID)

	mustHandle(t, Subscribe(), buildParams(t, pool, EntityTypeEvent, ActionSubscribe, followerID, collidingID, "0xafollower", `{}`))
	mustHandle(t, Follow(), buildParams(t, pool, EntityTypeUser, ActionFollow, followerID, collidingID, "0xafollower", `{}`))

	// The follow created its own User subscription alongside the Event one.
	assertSubscriptionDeleteState(t, pool, followerID, collidingID, EntityTypeUser, false)
	assertSubscriptionDeleteState(t, pool, followerID, collidingID, EntityTypeEvent, false)

	// Unfollowing tombstones only the User subscription.
	mustHandle(t, Unfollow(), buildParams(t, pool, EntityTypeUser, ActionUnfollow, followerID, collidingID, "0xafollower", `{}`))

	assertSubscriptionDeleteState(t, pool, followerID, collidingID, EntityTypeUser, true)
	assertSubscriptionDeleteState(t, pool, followerID, collidingID, EntityTypeEvent, false)
}

func TestFollow_AutoSubscribeWritesEntityID(t *testing.T) {
	pool := setupTestDB(t)
	uid1 := int64(UserIDOffset + 45)
	uid2 := int64(UserIDOffset + 46)
	seedUser(t, pool, uid1, "0xeidfollower", "eidfollower")
	seedUser(t, pool, uid2, "0xeidfollowee", "eidfollowee")

	mustHandle(t, Follow(), buildParams(t, pool, EntityTypeUser, ActionFollow, uid1, uid2, "0xeidfollower", `{}`))

	var entityID int64
	err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(entity_id, -1) FROM subscriptions
		WHERE subscriber_id = $1 AND user_id = $2 AND entity_type = $3 AND is_current = true
	`, uid1, uid2, EntityTypeUser).Scan(&entityID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if entityID != uid2 {
		t.Errorf("entity_id = %d, want %d", entityID, uid2)
	}
}
