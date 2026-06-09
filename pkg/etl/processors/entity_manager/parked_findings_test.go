package entity_manager

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Regression: developer app create/update must persist image_url (previously
// dropped), gated by the legacy is_fqdn check. Update overwrites from metadata.
func TestDeveloperApp_ImageURL(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	seedUser(t, pool, uid, "0xappowner", "appowner")

	imageURL := func(addr string) sql.NullString {
		var v sql.NullString
		if err := pool.QueryRow(context.Background(),
			"SELECT image_url FROM developer_apps WHERE address=$1 AND is_current=true", addr).Scan(&v); err != nil {
			t.Fatalf("query image_url(%s): %v", addr, err)
		}
		return v
	}

	// Create with a valid FQDN image_url → stored.
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 1, "0xAppOwner",
		`{"address":"0ximgapp","name":"Img App","image_url":"https://cdn.audius.co/icon.png"}`))
	if got := imageURL("0ximgapp"); !got.Valid || got.String != "https://cdn.audius.co/icon.png" {
		t.Errorf("create valid image_url: got %v", got)
	}

	// Update to a new valid image_url → overwritten.
	mustHandle(t, DeveloperAppUpdate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionUpdate, uid, 1, "0xAppOwner",
		`{"address":"0ximgapp","name":"Img App","image_url":"https://img.example.com/new.png"}`))
	if got := imageURL("0ximgapp"); !got.Valid || got.String != "https://img.example.com/new.png" {
		t.Errorf("update image_url: got %v", got)
	}

	// Create with an invalid (non-FQDN) image_url → NULL (legacy is_fqdn gate).
	mustHandle(t, DeveloperAppCreate(), buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 2, "0xAppOwner",
		`{"address":"0xbadimg","name":"Bad Img","image_url":"javascript:alert(1)"}`))
	if got := imageURL("0xbadimg"); got.Valid {
		t.Errorf("invalid image_url should be NULL, got %q", got.String)
	}
}

// Regression: the comment video_url cap must count runes, not bytes (it claims
// a "character limit" but used len()). A multibyte URL within 2000 characters
// but over 2000 bytes must be accepted, not rejected.
func TestCommentCreate_VideoURLCountsRunes(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 60)
	seedUser(t, pool, uid, "0xvurl", "vurlu")
	seedTrack(t, pool, trackID, uid)

	// 1500 runes, 4500 bytes: under the 2000-character limit, over 2000 bytes.
	videoURL := strings.Repeat("✓", 1500)
	meta := fmt.Sprintf(`{"body":"hi","entity_id":%d,"entity_type":"Track","video_url":%q}`, trackID, videoURL)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xVurl", meta))

	var stored string
	if err := pool.QueryRow(context.Background(),
		"SELECT video_url FROM comments WHERE comment_id=$1", commentID).Scan(&stored); err != nil {
		t.Fatalf("query: %v", err)
	}
	if stored != videoURL {
		t.Errorf("video_url not stored intact (got %d runes)", len([]rune(stored)))
	}
}
