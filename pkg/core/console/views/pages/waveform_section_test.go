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

func renderWaveforms(t *testing.T, w *storagev1.WaveformStatus) string {
	t.Helper()
	var buf bytes.Buffer
	d := &storagev1.GetStorageDiagnosticsResponse{Waveforms: w}
	if err := waveformSection(d).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// A node that has never enabled the feature still renders the storage page.
func TestWaveformSectionAbsentWhenNotReported(t *testing.T) {
	var buf bytes.Buffer
	d := &storagev1.GetStorageDiagnosticsResponse{}
	if err := waveformSection(d).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Waveforms") {
		t.Fatalf("nothing to report should render nothing:\n%s", buf.String())
	}
}

// Disabled is worth stating rather than hiding -- it is how an operator learns
// the capability exists -- but it must not imply a stalled backfill by showing
// a run with zeroes.
func TestWaveformSectionDisabledStaysMinimal(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{Enabled: false})
	if !strings.Contains(html, "disabled") {
		t.Fatalf("disabled state must be stated:\n%s", html)
	}
	if strings.Contains(html, "Backfill Run") {
		t.Fatalf("disabled node must not render run cards:\n%s", html)
	}
}

// The state operators actually get stuck in: the master switch is on, so it
// looks enabled, but history is never walked because backfill is off.
func TestWaveformSectionDistinguishesLiveOnlyFromBackfill(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, Version: 42, AlgorithmVersion: 1, Buckets: 750, SampleRate: 44100,
	})
	if !strings.Contains(html, "live uploads only") {
		t.Fatalf("must distinguish live-only from backfilling:\n%s", html)
	}
	if !strings.Contains(html, "backfill not enabled") {
		t.Fatalf("run card must explain the absent run:\n%s", html)
	}
	// The fingerprint is meaningless without the inputs beside it.
	if !strings.Contains(html, "750 buckets") || !strings.Contains(html, "44100 Hz") {
		t.Fatalf("version must be shown with its parameters:\n%s", html)
	}
}

func TestWaveformSectionShowsRunProgressAndHeldBackArchive(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true, Version: 42, AlgorithmVersion: 1,
		Buckets: 750, SampleRate: 44100,
		ByStatus: map[string]int64{
			"done": 1200, "not_local": 300, "archive_skipped": 2000000, "error": 4,
		},
		Run: &storagev1.WaveformRun{
			StartedAt:       timestamppb.New(time.Now().Add(-2 * time.Hour)),
			UpdatedAt:       timestamppb.New(time.Now().Add(-30 * time.Second)),
			CursorCreatedAt: timestamppb.New(time.Now().Add(-90 * 24 * time.Hour)),
			Queued:          1500,
			Fraction:        0.25,
			RemainingNs:     int64(6 * time.Hour),
			Version:         42,
		},
	})

	for _, want := range []string{
		"walking history",
		"progress",
		"remaining",
		"last checkpoint",
		"2000000", // the cost of enabling the archive tier, before committing
		"1200",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in run section:\n%s", want, html)
		}
	}
}

// A version bump is only visible as a pending re-backfill until the sweep picks
// it up, and that gap is exactly when an operator wonders why nothing changed.
func TestWaveformSectionFlagsPendingReBackfill(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true, Version: 99,
		StaleVersion: 5000,
		Run:          &storagev1.WaveformRun{Version: 42, Exhausted: true},
	})
	if !strings.Contains(html, "re-backfill pending") {
		t.Fatalf("a cursor behind the running version must be called out:\n%s", html)
	}
	if !strings.Contains(html, "5000") {
		t.Fatalf("must show how much needs recomputing:\n%s", html)
	}
}

// An erroring bucket is a storage problem surfacing through this feature, not a
// waveform problem, and the card should say so rather than bury it with decode
// failures.
func TestWaveformSectionSeparatesStorageErrorsFromDecodeFailures(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true,
		ByStatus: map[string]int64{"error": 7, "unavailable": 900},
		Run:      &storagev1.WaveformRun{},
	})
	if !strings.Contains(html, "storage unavailable") {
		t.Fatalf("bucket errors must be surfaced distinctly:\n%s", html)
	}
	if !strings.Contains(html, "not just missing blobs") {
		t.Fatalf("must explain what unavailable means:\n%s", html)
	}
}
