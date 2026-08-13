package main

import (
	"testing"
	"time"
)

// The indexer's parseReleaseDate accepts RFC3339, RFC3339Nano and
// "Mon Jan 02 2006 15:04:05 GMT-0700" -- and nothing else. Selecting
// release_date::text yields Postgres's "2026-09-06 22:06:00", which matches
// none of them, so the indexer silently fell back to block time. A future
// release date rewritten into the past is then picked up by the
// scheduled-release publisher, which sets is_unlisted = false: on the
// 2026-08-07 snapshot that rewrote 372 dates and published 368 unlisted
// tracks early.
func TestReleaseDateIsEmittedInAnAcceptedLayout(t *testing.T) {
	rd := time.Date(2026, 9, 6, 22, 6, 0, 0, time.UTC)
	got := fmtReleaseDate(&rd)

	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("fmtReleaseDate produced %q, which the indexer cannot parse: %v", got, err)
	}
	if got != "2026-09-06T22:06:00Z" {
		t.Errorf("fmtReleaseDate = %q, want %q", got, "2026-09-06T22:06:00Z")
	}
	if got == "2026-09-06 22:06:00" {
		t.Error("emitted Postgres text format, which no accepted layout matches")
	}
	if fmtReleaseDate(nil) != "" {
		t.Errorf("nil release_date = %q, want empty so omitempty drops it", fmtReleaseDate(nil))
	}
}
