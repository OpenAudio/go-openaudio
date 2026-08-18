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

// Off is the default on nearly every node, and an empty section on all of them
// is noise. Matches Archive Storage, which only appears once configured.
func TestWaveformSectionHiddenWhenDisabled(t *testing.T) {
	for _, w := range []*storagev1.WaveformStatus{nil, {Enabled: false}} {
		html := renderWaveforms(t, w)
		if strings.Contains(html, "Waveforms") {
			t.Fatalf("disabled node must render nothing:\n%s", html)
		}
	}
}

// The settings describe the output, so they belong in a tile rather than
// floating under the heading as prose.
func TestWaveformAnalysisCardCarriesSettings(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, Version: 39204091, AlgorithmVersion: 1, Buckets: 750, SampleRate: 44100,
	})
	for _, want := range []string{"Analysis", "live uploads only", "750 buckets", "44100 Hz", "algorithm 1", "v39204091"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in the analysis tile:\n%s", want, html)
		}
	}
}

// Enabled-with-backfill-off is the state operators mistake for working, so the
// backfill tile has to say so on its own.
func TestWaveformBackfillCardDistinguishesOffFromIdle(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{Enabled: true, Buckets: 750})
	if !strings.Contains(html, "history not walked") {
		t.Fatalf("backfill-off must be explicit:\n%s", html)
	}
	if !strings.Contains(html, "new uploads are still analyzed") {
		t.Fatalf("must say the live path still runs:\n%s", html)
	}
}

func TestWaveformBackfillCardShowsRunProgress(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true, Version: 7,
		Run: &storagev1.WaveformRun{
			StartedAt:       timestamppb.New(time.Now().Add(-2 * time.Hour)),
			UpdatedAt:       timestamppb.New(time.Now().Add(-30 * time.Second)),
			CursorCreatedAt: timestamppb.New(time.Now().Add(-90 * 24 * time.Hour)),
			Queued:          1500, Fraction: 0.25, RemainingNs: int64(6 * time.Hour), Version: 7,
		},
	})
	for _, want := range []string{"walking history", "25.0% walked", "left (rough)", "last checkpoint", "queued this pass: 1500"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in the backfill tile:\n%s", want, html)
		}
	}
}

// Between a version bump and the sweep noticing, nothing else changes visibly.
func TestWaveformBackfillCardFlagsPendingReBackfill(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true, Version: 99,
		Run: &storagev1.WaveformRun{Version: 42, Exhausted: true},
	})
	if !strings.Contains(html, "re-backfill pending") {
		t.Fatalf("a cursor behind the running version must be called out:\n%s", html)
	}
}

func TestWaveformStatTilesCoverEveryStatus(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true, StaleVersion: 5000,
		ByStatus: map[string]int64{
			"done": 1200, "not_local": 300, "archive_skipped": 2293395,
			"unavailable": 914, "error": 37,
		},
	})
	for _, want := range []string{"1200", "300", "2293395", "914", "37", "5000"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected count %q in the stat tiles:\n%s", want, html)
		}
	}
	// unavailable is a storage problem, not unanalyzable audio.
	if !strings.Contains(html, "storage erroring, not missing") {
		t.Fatalf("unavailable must be distinguished from missing blobs:\n%s", html)
	}
}

func TestWaveformStatTilesReadCleanWithNoStorageErrors(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, ByStatus: map[string]int64{"done": 10},
	})
	if !strings.Contains(html, "no storage errors") {
		t.Fatalf("a healthy node should say so:\n%s", html)
	}
}

// Nothing else reports whether the waveforms are being consumed, and the
// redirect count is the only read on how evenly they are spread network-wide.
func TestWaveformRequestTilesRender(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true,
		Requests: &storagev1.WaveformRequestStats{
			Served: 8123, Misses: 44, Redirected: 271,
		},
	})
	for _, want := range []string{"Requests Served", "8123", "Request Misses", "44", "Requests Redirected", "271"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in the request tiles:\n%s", want, html)
		}
	}
}

// A node that has served nothing yet still renders, rather than nil-panicking.
func TestWaveformRequestTilesSurviveMissingStats(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{Enabled: true})
	if !strings.Contains(html, "Requests Served") {
		t.Fatalf("request tiles must render without stats:\n%s", html)
	}
}

// The one figure describing rows absent from the table, so it is sampled on a
// sweep. The tile must say how stale that sample is rather than imply it is live.
func TestWaveformUnanalyzedTileReportsSampleAge(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true,
		Outstanding: 151600, OutstandingAgeNs: int64(90 * time.Second),
	})
	if !strings.Contains(html, "Unanalyzed") || !strings.Contains(html, "151600") {
		t.Fatalf("outstanding work must be shown:\n%s", html)
	}
	if !strings.Contains(html, "as of") {
		t.Fatalf("must disclose the sample age:\n%s", html)
	}
}

func TestWaveformUnanalyzedTileBeforeFirstSample(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{Enabled: true})
	if !strings.Contains(html, "not yet reached") {
		t.Fatalf("must render before any sample exists:\n%s", html)
	}
	if strings.Contains(html, "as of") {
		t.Fatalf("must not claim an age it does not have:\n%s", html)
	}
}
