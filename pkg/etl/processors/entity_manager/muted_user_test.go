package entity_manager

import (
	"context"
	"testing"
)

func TestMuteUser_TxType(t *testing.T) {
	h := MuteUser()
	if h.EntityType() != EntityTypeUser {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeUser)
	}
	if h.Action() != ActionMute {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionMute)
	}
}

func TestUnmuteUser_TxType(t *testing.T) {
	h := UnmuteUser()
	if h.EntityType() != EntityTypeUser {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeUser)
	}
	if h.Action() != ActionUnmute {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionUnmute)
	}
}

func TestMuteUser_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	targetID := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xmuter", "muter")
	seedUser(t, pool, targetID, "0xtarget", "target")

	mustHandle(t, MuteUser(), buildParams(t, pool, EntityTypeUser, ActionMute, uid, targetID, "0xMuter", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM muted_users WHERE user_id = $1 AND muted_user_id = $2", uid, targetID).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isDelete {
		t.Error("expected is_delete = false")
	}
}

func TestMuteUser_RejectsSelfMute(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xmuter", "muter")

	params := buildParams(t, pool, EntityTypeUser, ActionMute, uid, uid, "0xMuter", `{}`)
	mustReject(t, MuteUser(), params, "cannot mute themselves")
}

func TestMuteUser_RejectsNonexistentTarget(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xmuter", "muter")

	params := buildParams(t, pool, EntityTypeUser, ActionMute, uid, int64(UserIDOffset+999), "0xMuter", `{}`)
	mustReject(t, MuteUser(), params, "does not exist")
}

func TestMuteUser_RejectsDuplicate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	targetID := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xmuter", "muter")
	seedUser(t, pool, targetID, "0xtarget", "target")

	mustHandle(t, MuteUser(), buildParams(t, pool, EntityTypeUser, ActionMute, uid, targetID, "0xMuter", `{}`))

	params := buildParams(t, pool, EntityTypeUser, ActionMute, uid, targetID, "0xMuter", `{}`)
	mustReject(t, MuteUser(), params, "already muted")
}

func TestUnmuteUser_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	targetID := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xmuter", "muter")
	seedUser(t, pool, targetID, "0xtarget", "target")

	mustHandle(t, MuteUser(), buildParams(t, pool, EntityTypeUser, ActionMute, uid, targetID, "0xMuter", `{}`))
	mustHandle(t, UnmuteUser(), buildParams(t, pool, EntityTypeUser, ActionUnmute, uid, targetID, "0xMuter", `{}`))

	var isDelete bool
	err := pool.QueryRow(context.Background(),
		"SELECT is_delete FROM muted_users WHERE user_id = $1 AND muted_user_id = $2", uid, targetID).Scan(&isDelete)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !isDelete {
		t.Error("expected is_delete = true")
	}
}

func TestUnmuteUser_RejectsNoActiveMute(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	targetID := int64(UserIDOffset + 2)
	seedUser(t, pool, uid, "0xmuter", "muter")
	seedUser(t, pool, targetID, "0xtarget", "target")

	params := buildParams(t, pool, EntityTypeUser, ActionUnmute, uid, targetID, "0xMuter", `{}`)
	mustReject(t, UnmuteUser(), params, "no active mute")
}
