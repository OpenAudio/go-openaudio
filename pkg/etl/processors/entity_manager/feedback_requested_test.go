package entity_manager

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestTrackFeedbackRequested(t *testing.T) {
	pool := setupTestDB(t)
	ctx := context.Background()
	uid := int64(UserIDOffset + 1)
	tid := int64(TrackIDOffset + 91)
	seedUser(t, pool, uid, "0xfeedback", "feedbackowner")

	read := func() pgtype.Timestamp {
		t.Helper()
		var ts pgtype.Timestamp
		if err := pool.QueryRow(ctx, `SELECT feedback_requested_at FROM tracks WHERE track_id=$1 AND is_current=true`, tid).Scan(&ts); err != nil {
			t.Fatalf("query: %v", err)
		}
		return ts
	}
	update := func(at time.Time, meta string) {
		t.Helper()
		p := buildParams(t, pool, EntityTypeTrack, ActionUpdate, uid, tid, "0xFeedback", meta)
		p.BlockTime = at
		mustHandle(t, TrackUpdate(), p)
	}

	created := buildParams(t, pool, EntityTypeTrack, ActionCreate, uid, tid, "0xFeedback",
		`{"owner_id":3000001,"title":"Rough Mix","feedback_requested":true}`)
	mustHandle(t, TrackCreate(), created)
	if ts := read(); !ts.Valid {
		t.Fatalf("create: feedback_requested_at is NULL")
	}
	first := read()

	// Re-sending true (clients send the whole track on edit) keeps the original time.
	update(first.Time.Add(time.Hour), `{"title":"Rough Mix v2","feedback_requested":true}`)
	if ts := read(); !ts.Valid || !ts.Time.Equal(first.Time) {
		t.Fatalf("resend true: got %+v, want %v", ts, first.Time)
	}

	// Omitting the key leaves it alone.
	update(first.Time.Add(2*time.Hour), `{"title":"Rough Mix v3"}`)
	if ts := read(); !ts.Valid || !ts.Time.Equal(first.Time) {
		t.Fatalf("omitted: got %+v, want %v", ts, first.Time)
	}

	update(first.Time.Add(3*time.Hour), `{"feedback_requested":false}`)
	if ts := read(); ts.Valid {
		t.Fatalf("off: got %+v, want NULL", ts)
	}

	// Turning it back on stamps the new time.
	again := first.Time.Add(4 * time.Hour)
	update(again, `{"feedback_requested":true}`)
	if ts := read(); !ts.Valid || !ts.Time.Equal(again) {
		t.Fatalf("on again: got %+v, want %v", ts, again)
	}
}
