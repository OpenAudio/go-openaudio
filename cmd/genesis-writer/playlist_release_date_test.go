package main

import (
	"testing"
	"time"
)

// Playlists selected p.release_date::text and passed it through untouched.
// That is Postgres text format -- "2024-12-20 13:59:25.634624" -- which
// parseReleaseDate does not accept, so playlist_create fell back to block
// time. On a migration block time is the source row's created_at, so every
// migrated playlist ended up with release_date == created_at: 315,166 of
// 315,166 on the 2026-08-16 snapshot, none carrying its real release date.
//
// This is the same failure #519 fixed for tracks. Only the track path was
// changed then; playlists kept emitting ::text.
func TestPlaylistReleaseDateIsEmittedInAnAcceptedLayout(t *testing.T) {
	rd := time.Date(2024, 12, 20, 13, 59, 25, 634624000, time.UTC)
	got := fmtReleaseDate(&rd)

	if got != "2024-12-20T13:59:25.634624Z" {
		t.Errorf("fmtReleaseDate = %q, want %q", got, "2024-12-20T13:59:25.634624Z")
	}
	if _, err := time.Parse(time.RFC3339Nano, got); err != nil {
		t.Fatalf("indexer cannot parse %q: %v", got, err)
	}
	// The shape that caused the fallback.
	if _, err := time.Parse(time.RFC3339Nano, "2024-12-20 13:59:25.634624"); err == nil {
		t.Error("Postgres text format parsed unexpectedly; the fallback this guards would not trigger")
	}
	if fmtReleaseDate(nil) != "" {
		t.Errorf("nil release_date = %q, want empty so omitempty drops it", fmtReleaseDate(nil))
	}
}
