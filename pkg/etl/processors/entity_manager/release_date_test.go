package entity_manager

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestParseReleaseDate(t *testing.T) {
	want := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		in        string
		wantOK    bool
		wantValue time.Time
	}{
		{name: "rfc3339 utc", in: "2026-05-01T00:00:00Z", wantOK: true, wantValue: want},
		{name: "rfc3339 nano", in: "2026-05-01T00:00:00.000Z", wantOK: true, wantValue: want},
		{name: "timezoneless T separator", in: "2026-05-01T00:00:00", wantOK: true, wantValue: want},
		{name: "space separated", in: "2026-05-01 00:00:00", wantOK: true, wantValue: want},
		{name: "date only", in: "2026-05-01", wantOK: true, wantValue: want},
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

// Regression test: a timezone-less release_date (the format that surfaced as
// "Invalid Date" in clients) must now be indexed instead of dropped to NULL.
func TestTrackCreate_TimezonelessReleaseDate(t *testing.T) {
	pool := setupTestDB(t)
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 750)
	seedUser(t, pool, uid, "0xtzowner", "tzowner")

	meta := `{"owner_id":3000001,"title":"Backdated Live Set","release_date":"2026-05-01T00:00:00"}`
	params := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xTzOwner", meta)
	mustHandle(t, TrackCreate(), params)

	var releaseDate sql.NullTime
	err := pool.QueryRow(context.Background(),
		"SELECT release_date FROM tracks WHERE track_id = $1 AND is_current = true", tid).Scan(&releaseDate)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !releaseDate.Valid {
		t.Fatal("expected release_date to be set for timezone-less input")
	}
	if got := releaseDate.Time.UTC(); !got.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("release_date = %v, want 2026-05-01T00:00:00Z", got)
	}
}
