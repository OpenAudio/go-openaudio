package entity_manager

import (
	"context"
	"testing"
	"time"
)

func TestNotificationCreate_TxType(t *testing.T) {
	h := NotificationCreate()
	if h.EntityType() != EntityTypeNotification {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeNotification)
	}
	if h.Action() != ActionCreate {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionCreate)
	}
}

func TestNotificationCreate_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xnotifier", "notifier")

	meta := `{"title":"Test Announcement","body":"Hello world"}`
	mustHandle(t, NotificationCreate(), buildParams(t, pool, EntityTypeNotification, ActionCreate, uid, 0, "0xNotifier", meta))

	var groupID, typ string
	err := pool.QueryRow(context.Background(),
		"SELECT group_id, type FROM notification WHERE type = 'announcement' ORDER BY id DESC LIMIT 1").Scan(&groupID, &typ)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if typ != "announcement" {
		t.Errorf("type = %q, want %q", typ, "announcement")
	}
}

func TestNotificationCreate_RejectsEmptyMetadata(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xnotifier", "notifier")

	params := buildParams(t, pool, EntityTypeNotification, ActionCreate, uid, 0, "0xNotifier", "")
	mustReject(t, NotificationCreate(), params, "metadata is empty")
}

func TestNotificationCreate_RejectsInvalidJSON(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xnotifier", "notifier")

	params := buildParams(t, pool, EntityTypeNotification, ActionCreate, uid, 0, "0xNotifier", "not json")
	mustReject(t, NotificationCreate(), params, "invalid notification metadata")
}

func TestNotificationView_TxType(t *testing.T) {
	h := NotificationView()
	if h.EntityType() != EntityTypeNotification {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeNotification)
	}
	if h.Action() != ActionView {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionView)
	}
}

func TestNotificationView_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xviewer", "viewer")

	mustHandle(t, NotificationView(), buildParams(t, pool, EntityTypeNotification, ActionView, uid, 0, "0xViewer", `{}`))

	var seenAt time.Time
	err := pool.QueryRow(context.Background(),
		"SELECT seen_at FROM notification_seen WHERE user_id = $1", uid).Scan(&seenAt)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if seenAt.IsZero() {
		t.Error("expected non-zero seen_at")
	}
}

func TestNotificationView_RejectsNonexistentUser(t *testing.T) {
	pool := setupTestDB(t)
	params := buildParams(t, pool, EntityTypeNotification, ActionView, int64(UserIDOffset+999), 0, "0xNobody", `{}`)
	mustReject(t, NotificationView(), params, "does not exist")
}

func TestPlaylistSeenView_TxType(t *testing.T) {
	h := PlaylistSeenView()
	if h.EntityType() != EntityTypeNotification {
		t.Errorf("EntityType() = %q, want %q", h.EntityType(), EntityTypeNotification)
	}
	if h.Action() != ActionViewPlaylist {
		t.Errorf("Action() = %q, want %q", h.Action(), ActionViewPlaylist)
	}
}

func TestPlaylistSeenView_Success(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	pid := int64(PlaylistIDOffset + 90)
	seedUser(t, pool, uid, "0xviewer", "viewer")
	seedPlaylist(t, pool, pid, uid)

	mustHandle(t, PlaylistSeenView(), buildParams(t, pool, EntityTypeNotification, ActionViewPlaylist, uid, pid, "0xViewer", `{}`))

	var playlistID int
	err := pool.QueryRow(context.Background(),
		"SELECT playlist_id FROM playlist_seen WHERE user_id = $1 AND playlist_id = $2", uid, pid).Scan(&playlistID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if int64(playlistID) != pid {
		t.Errorf("playlist_id = %d, want %d", playlistID, pid)
	}
}

func TestPlaylistSeenView_RejectsNonexistentPlaylist(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xviewer", "viewer")

	params := buildParams(t, pool, EntityTypeNotification, ActionViewPlaylist, uid, int64(PlaylistIDOffset+999), "0xViewer", `{}`)
	mustReject(t, PlaylistSeenView(), params, "does not exist")
}
