package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

// A migration emits one Create per comment and no updates, so the only place
// an edited comment can be marked edited is the Create itself. The live
// indexer sets is_edited on Update, which meant every edited comment replayed
// as unedited -- 1,249 of them on the 2026-08-16 snapshot.
func TestMigratedCommentCreate_ReplaysIsEdited(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 981)
	trackID := int64(TrackIDOffset + 981)
	seedUser(t, pool, uid, "0xeditor", "editor")
	seedTrack(t, pool, trackID, uid)

	cases := []struct {
		name      string
		commentID int64
		metaFrag  string
		want      bool
	}{
		{"edited comment replays as edited", int64(CommentIDOffset + 981), `,"is_edited":true`, true},
		{"unedited comment stays unedited", int64(CommentIDOffset + 982), `,"is_edited":false`, false},
		{"absent flag defaults to unedited", int64(CommentIDOffset + 983), "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			meta := fmt.Sprintf(`{"body":"hi","entity_id":%d,"entity_type":"Track"%s}`, trackID, c.metaFrag)
			mustHandle(t, migratedCommentCreate(),
				buildParams(t, pool, EntityTypeComment, ActionCreate, uid, c.commentID, "0xEditor", meta))

			var got bool
			if err := pool.QueryRow(context.Background(),
				"SELECT is_edited FROM comments WHERE comment_id = $1", c.commentID).Scan(&got); err != nil {
				t.Fatalf("query: %v", err)
			}
			if got != c.want {
				t.Errorf("is_edited = %v, want %v", got, c.want)
			}
		})
	}
}

// The live path must be unaffected: a comment created now has not been edited,
// whatever a client puts in the metadata.
func TestLiveCommentCreate_IgnoresIsEdited(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 984)
	trackID := int64(TrackIDOffset + 984)
	commentID := int64(CommentIDOffset + 984)
	seedUser(t, pool, uid, "0xliveeditor", "liveeditor")
	seedTrack(t, pool, trackID, uid)

	meta := fmt.Sprintf(`{"body":"hi","entity_id":%d,"entity_type":"Track","is_edited":true}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xLiveEditor", meta))

	var got bool
	if err := pool.QueryRow(context.Background(),
		"SELECT is_edited FROM comments WHERE comment_id = $1", commentID).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got {
		t.Error("live create honoured is_edited from metadata; only Update may set it")
	}
}
