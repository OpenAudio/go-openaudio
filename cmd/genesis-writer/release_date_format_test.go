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

// RFC3339 has no fractional-second component, so formatting with it rounded
// every sub-second release_date down to the whole second. The indexer accepts
// RFC3339Nano, and a value with no fraction still formats identically, so
// nothing about the layout above changes.
func TestReleaseDateKeepsSubSecondPrecision(t *testing.T) {
	rd := time.Date(2026, 2, 2, 15, 53, 12, 50585000, time.UTC)
	got := fmtReleaseDate(&rd)

	if got != "2026-02-02T15:53:12.050585Z" {
		t.Errorf("fmtReleaseDate = %q, want %q -- microseconds were dropped", got, "2026-02-02T15:53:12.050585Z")
	}

	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("indexer cannot parse %q: %v", got, err)
	}
	if !parsed.Equal(rd) {
		t.Errorf("round-tripped to %v, want %v", parsed, rd)
	}

	// A whole-second value must still come out in the plain layout, so this
	// change is invisible to the 99% of rows that carry no fraction.
	whole := time.Date(2026, 9, 6, 22, 6, 0, 0, time.UTC)
	if got := fmtReleaseDate(&whole); got != "2026-09-06T22:06:00Z" {
		t.Errorf("whole-second release_date = %q, want %q", got, "2026-09-06T22:06:00Z")
	}
}

// created_at went out through the same non-fractional layout, and it is not a
// cosmetic field: the indexer replays each migrated row as of its created_at
// (migrationBlockTime), and parity keys track_downloads on it. A full-snapshot
// run truncated every one of the 78,032 track_downloads rows, plus ~2.9M
// social rows, to the whole second.
//
// parseMigrationTimestamp tries RFC3339Nano before RFC3339, and Go's parser
// accepts a fractional second against a layout that lacks one, so emitting the
// longer form is safe for every reader.
func TestEmittedCreatedAtKeepsSubSecondPrecision(t *testing.T) {
	src := sourceTrack{
		TrackID:   1,
		OwnerID:   2,
		CreatedAt: time.Date(2024, 12, 1, 12, 30, 13, 244308000, time.UTC),
	}

	got := buildTrackMetadata(src, nil).CreatedAt
	if got != "2024-12-01T12:30:13.244308Z" {
		t.Errorf("created_at = %q, want %q -- microseconds were dropped", got, "2024-12-01T12:30:13.244308Z")
	}

	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("indexer cannot parse %q: %v", got, err)
	}
	if !parsed.Equal(src.CreatedAt) {
		t.Errorf("round-tripped to %v, want %v", parsed, src.CreatedAt)
	}

	// The stricter RFC3339 layout used by event_create.go must still accept it.
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("plain RFC3339 parse rejected %q: %v", got, err)
	}
}
