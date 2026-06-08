package entity_manager

import (
	"context"
	"fmt"
	"testing"
)

// Regression: track CREATE must persist rights/licensing + preview metadata
// that was previously dropped (license, isrc, iswc, preview_start_seconds,
// comments_disabled, no_ai_use, cover_original_*).
func TestTrackCreate_PersistsRightsAndPreviewFields(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 77)
	seedUser(t, pool, uid, "0xrights", "rightsowner")
	meta := `{"owner_id":3000001,"title":"Rights Song","license":"All rights reserved",` +
		`"isrc":"USX9P1234567","iswc":"T-123.456.789-0","preview_start_seconds":12.5,` +
		`"comments_disabled":true,"no_ai_use":true,` +
		`"cover_original_song_title":"Original","cover_original_artist":"Someone"}`
	mustHandle(t, TrackCreate(), buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xRights", meta))

	var license, isrc, iswc, coverSong, coverArtist string
	var previewStart float64
	var commentsDisabled, noAIUse bool
	err := pool.QueryRow(context.Background(),
		`SELECT license, isrc, iswc, preview_start_seconds, comments_disabled, no_ai_use,
			cover_original_song_title, cover_original_artist
		 FROM tracks WHERE track_id=$1 AND is_current=true`, tid).
		Scan(&license, &isrc, &iswc, &previewStart, &commentsDisabled, &noAIUse, &coverSong, &coverArtist)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if license != "All rights reserved" || isrc != "USX9P1234567" || iswc != "T-123.456.789-0" {
		t.Errorf("rights = license:%q isrc:%q iswc:%q", license, isrc, iswc)
	}
	if previewStart != 12.5 || !commentsDisabled || !noAIUse {
		t.Errorf("preview=%v commentsDisabled=%v noAIUse=%v", previewStart, commentsDisabled, noAIUse)
	}
	if coverSong != "Original" || coverArtist != "Someone" {
		t.Errorf("cover orig = %q / %q", coverSong, coverArtist)
	}
}

// Regression: track UPDATE must persist the same fields (previously dropped).
func TestTrackUpdate_PersistsRightsAndPreviewFields(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 78)
	seedUser(t, pool, uid, "0xrights2", "rightsowner2")
	seedTrackFull(t, pool, tid, uid, "Editable")
	meta := `{"license":"CC BY 4.0","isrc":"USX9P7654321","preview_start_seconds":5,"comments_disabled":true}`
	mustHandle(t, TrackUpdate(), buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xRights2", meta))

	var license, isrc string
	var previewStart float64
	var commentsDisabled bool
	err := pool.QueryRow(context.Background(),
		`SELECT license, isrc, preview_start_seconds, comments_disabled
		 FROM tracks WHERE track_id=$1 AND is_current=true`, tid).
		Scan(&license, &isrc, &previewStart, &commentsDisabled)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if license != "CC BY 4.0" || isrc != "USX9P7654321" || previewStart != 5 || !commentsDisabled {
		t.Errorf("got license:%q isrc:%q preview:%v commentsDisabled:%v", license, isrc, previewStart, commentsDisabled)
	}
}

// Regression: comment UPDATE must keep video_url on a text-only edit
// (previously dropped → video silently removed), and still update it when the
// edit provides a new value.
func TestCommentUpdate_PreservesAndUpdatesVideo(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	trackID := int64(TrackIDOffset + 1)
	commentID := int64(CommentIDOffset + 50)
	seedUser(t, pool, uid, "0xcommenter2", "commenter2")
	seedTrack(t, pool, trackID, uid)

	createMeta := fmt.Sprintf(`{"body":"orig","entity_id":%d,"entity_type":"Track",`+
		`"video_url":"https://v.example/a.mp4"}`, trackID)
	mustHandle(t, CommentCreate(), buildParams(t, pool, EntityTypeComment, ActionCreate, uid, commentID, "0xCommenter2", createMeta))

	// Text-only edit must preserve video_url.
	editMeta := fmt.Sprintf(`{"body":"edited","entity_id":%d,"entity_type":"Track"}`, trackID)
	mustHandle(t, CommentUpdate(), buildParams(t, pool, EntityTypeComment, ActionUpdate, uid, commentID, "0xCommenter2", editMeta))
	var text, videoURL string
	if err := pool.QueryRow(context.Background(),
		"SELECT text, video_url FROM comments WHERE comment_id=$1", commentID).Scan(&text, &videoURL); err != nil {
		t.Fatalf("query: %v", err)
	}
	if text != "edited" {
		t.Errorf("text=%q, want edited", text)
	}
	if videoURL != "https://v.example/a.mp4" {
		t.Errorf("preserve failed: video=%q", videoURL)
	}

	// An edit that provides a new video_url must update it.
	editMeta2 := fmt.Sprintf(`{"body":"edited2","entity_id":%d,"entity_type":"Track","video_url":"https://v.example/b.mp4"}`, trackID)
	mustHandle(t, CommentUpdate(), buildParams(t, pool, EntityTypeComment, ActionUpdate, uid, commentID, "0xCommenter2", editMeta2))
	if err := pool.QueryRow(context.Background(),
		"SELECT video_url FROM comments WHERE comment_id=$1", commentID).Scan(&videoURL); err != nil {
		t.Fatalf("query2: %v", err)
	}
	if videoURL != "https://v.example/b.mp4" {
		t.Errorf("update failed: video=%q", videoURL)
	}
}
