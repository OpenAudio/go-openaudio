package entity_manager

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestParseReleaseDate(t *testing.T) {
	want := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		in        string
		wantOK    bool
		wantValue time.Time
	}{
		{name: "js date toString", in: "Fri May 01 2026 00:00:00 GMT-0700", wantOK: true, wantValue: want},
		{name: "iso millis utc", in: "2026-05-01T07:00:00.000Z", wantOK: true, wantValue: want},
		{name: "iso no millis utc", in: "2026-05-01T07:00:00Z", wantOK: true, wantValue: want},
		{name: "iso with offset", in: "2026-05-01T00:00:00-07:00", wantOK: true, wantValue: want},
		{name: "unix seconds", in: "1777618800", wantOK: true, wantValue: want},
		// Formats the legacy indexer rejected (-> falls back to block time).
		{name: "date only", in: "2026-05-01", wantOK: false},
		{name: "timezoneless", in: "2026-05-01T00:00:00", wantOK: false},
		{name: "empty", in: "", wantOK: false},
		{name: "garbage", in: "not a date", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseReleaseDate(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("parseReleaseDate(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if !tt.wantOK {
				if got.Valid {
					t.Errorf("expected invalid timestamp for %q", tt.in)
				}
				return
			}
			if !got.Valid {
				t.Fatalf("expected valid timestamp for %q", tt.in)
			}
			if !got.Time.UTC().Equal(tt.wantValue) {
				t.Errorf("parseReleaseDate(%q) = %v, want %v", tt.in, got.Time.UTC(), tt.wantValue)
			}
		})
	}
}

func TestReleaseDateOrDefault(t *testing.T) {
	blockTime := time.Date(2026, 6, 2, 3, 53, 4, 0, time.UTC)

	// Parseable metadata wins.
	got := releaseDateOrDefault("2026-05-01T07:00:00.000Z", blockTime)
	if !got.Valid || !got.Time.UTC().Equal(time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected parsed metadata date, got %+v", got)
	}

	// Missing or unparseable metadata falls back to the block time (created_at),
	// never NULL.
	for _, raw := range []string{"", "2026-05-01", "garbage"} {
		got := releaseDateOrDefault(raw, blockTime)
		if !got.Valid {
			t.Fatalf("expected default release_date for %q, got NULL", raw)
		}
		if !got.Time.UTC().Equal(blockTime) {
			t.Errorf("releaseDateOrDefault(%q) = %v, want block time %v", raw, got.Time.UTC(), blockTime)
		}
	}
}

// Regression test: a track created with no release_date in its metadata must
// default to its created_at (block time) instead of being left NULL, which is
// what surfaced as "Invalid Date" in clients.
func TestTrackCreate_DefaultsReleaseDateToCreatedAt(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 760)
	seedUser(t, pool, uid, "0xnoreldate", "noreldate")

	meta := `{"owner_id":3000001,"title":"No Release Date Provided"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xNoRelDate", meta)
	mustHandle(t, TrackCreate(), params)

	var releaseDate, createdAt sql.NullTime
	err := pool.QueryRow(context.Background(),
		"SELECT release_date, created_at FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&releaseDate, &createdAt)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !releaseDate.Valid {
		t.Fatal("expected release_date to default to created_at, got NULL")
	}
	if !releaseDate.Time.Equal(createdAt.Time) {
		t.Errorf("release_date = %v, want created_at %v", releaseDate.Time, createdAt.Time)
	}
}
