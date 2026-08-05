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

func render(t *testing.T, r *storagev1.RepairRun) string {
	t.Helper()
	var buf bytes.Buffer
	if err := repairRunCard("In Progress", r).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// An in-progress run's started_at can be months old on a resumed tracker, so it
// says nothing about whether the run is alive. The checkpoint age is the field
// that distinguishes working from wedged, and it must be on the card.
func TestRepairRunCardShowsCheckpointAgeWhileRunning(t *testing.T) {
	html := render(t, &storagev1.RepairRun{
		StartedAt: timestamppb.New(time.Now().Add(-77 * 24 * time.Hour)),
		UpdatedAt: timestamppb.New(time.Now().Add(-4 * time.Minute)),
	})

	if !strings.Contains(html, "last checkpoint") {
		t.Fatalf("in-progress card must show checkpoint age:\n%s", html)
	}
	if strings.Contains(html, "text-red-500") {
		t.Fatalf("a recent checkpoint must not be flagged stale:\n%s", html)
	}
}

// The exact case this node sat in for eleven weeks: a run reporting
// "in progress" whose last checkpoint was weeks old. That has to be visually
// distinct from a healthy long-running job.
func TestRepairRunCardFlagsStaleCheckpoint(t *testing.T) {
	html := render(t, &storagev1.RepairRun{
		StartedAt: timestamppb.New(time.Now().Add(-77 * 24 * time.Hour)),
		UpdatedAt: timestamppb.New(time.Now().Add(-25 * 24 * time.Hour)),
	})

	if !strings.Contains(html, "last checkpoint") {
		t.Fatalf("stale card must still show checkpoint age:\n%s", html)
	}
	if !strings.Contains(html, "text-red-500") {
		t.Fatalf("checkpoint older than %v must be flagged:\n%s", staleCheckpointAfter, html)
	}
}

// A finished run has a duration and a final state, so the checkpoint age is
// noise — it would just restate finished_at.
func TestRepairRunCardOmitsCheckpointWhenFinished(t *testing.T) {
	html := render(t, &storagev1.RepairRun{
		StartedAt:  timestamppb.New(time.Now().Add(-2 * time.Hour)),
		UpdatedAt:  timestamppb.New(time.Now().Add(-1 * time.Hour)),
		FinishedAt: timestamppb.New(time.Now().Add(-1 * time.Hour)),
	})

	if strings.Contains(html, "last checkpoint") {
		t.Fatalf("finished run should not show checkpoint age:\n%s", html)
	}
}

// Older nodes serve a RepairRun without updated_at; the card must not claim a
// checkpoint at the zero time.
func TestRepairRunCardHandlesMissingUpdatedAt(t *testing.T) {
	html := render(t, &storagev1.RepairRun{
		StartedAt: timestamppb.New(time.Now().Add(-time.Hour)),
	})

	if strings.Contains(html, "last checkpoint") {
		t.Fatalf("must not render a checkpoint line without updated_at:\n%s", html)
	}
}

func TestRepairCheckpointClass(t *testing.T) {
	if got := repairCheckpointClass(nil); got != "text-secondary" {
		t.Fatalf("nil run: got %q", got)
	}
	if got := repairCheckpointClass(&storagev1.RepairRun{}); got != "text-secondary" {
		t.Fatalf("no updated_at: got %q", got)
	}
	fresh := &storagev1.RepairRun{UpdatedAt: timestamppb.New(time.Now().Add(-time.Minute))}
	if got := repairCheckpointClass(fresh); got != "text-secondary" {
		t.Fatalf("fresh checkpoint: got %q", got)
	}
	stale := &storagev1.RepairRun{UpdatedAt: timestamppb.New(time.Now().Add(-2 * staleCheckpointAfter))}
	if got := repairCheckpointClass(stale); !strings.Contains(got, "text-red-500") {
		t.Fatalf("stale checkpoint: got %q", got)
	}
}
