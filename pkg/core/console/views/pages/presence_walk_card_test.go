package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	storagev1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func renderInProgress(t *testing.T, r *storagev1.RepairRun, w *storagev1.PresenceWalk) string {
	t.Helper()
	var buf bytes.Buffer
	if err := inProgressCard(r, w).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// A fresh cycle writes no tracker row until after its first batch, and the
// presence walk runs before that -- hours of it on a large file:// bucket. Read
// from in_progress_repair alone the card renders an em dash, which is what this
// node showed for 31 hours while it was very much working.
func TestInProgressCardDescribesWalkWithNoTrackerRow(t *testing.T) {
	html := renderInProgress(t, nil, &storagev1.PresenceWalk{
		Bucket:      "archive",
		Dir:         "/archive-blobs",
		Files:       2847113,
		Shards:      2201458,
		StartedAt:   timestamppb.New(time.Now().Add(-19 * time.Hour)),
		FilesPerSec: 40.5,
	})

	if strings.Contains(html, "—") {
		t.Fatalf("a running walk must not render as idle:\n%s", html)
	}
	for _, want := range []string{"building presence index", "archive", "2847113", "19h0m0s"} {
		if !strings.Contains(html, want) {
			t.Fatalf("card missing %q:\n%s", want, html)
		}
	}
}

// A resumed run does have a row, and its checkpoint age climbs for the whole
// walk. Both belong on the one card: the walk lines are what explain the stale
// checkpoint rather than leaving it looking wedged.
func TestInProgressCardShowsWalkAlongsideStaleCheckpoint(t *testing.T) {
	html := renderInProgress(t,
		&storagev1.RepairRun{
			StartedAt: timestamppb.New(time.Now().Add(-77 * 24 * time.Hour)),
			UpdatedAt: timestamppb.New(time.Now().Add(-20 * time.Hour)),
		},
		&storagev1.PresenceWalk{
			Bucket:      "archive",
			Files:       1200000,
			Shards:      900000,
			StartedAt:   timestamppb.New(time.Now().Add(-20 * time.Hour)),
			FilesPerSec: 16.7,
		})

	if !strings.Contains(html, "last checkpoint") {
		t.Fatalf("resumed run must still show its checkpoint age:\n%s", html)
	}
	if !strings.Contains(html, "building presence index") {
		t.Fatalf("walk must appear on the same card:\n%s", html)
	}
}

// Nothing running is still an em dash.
func TestInProgressCardEmptyWhenIdle(t *testing.T) {
	if html := renderInProgress(t, nil, nil); !strings.Contains(html, "—") {
		t.Fatalf("idle card should render an em dash:\n%s", html)
	}
}
