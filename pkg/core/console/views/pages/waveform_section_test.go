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
	return renderWaveformsArchive(t, w, false)
}

func renderWaveformsArchive(t *testing.T, w *storagev1.WaveformStatus, archiveConfigured bool) string {
	t.Helper()
	var buf bytes.Buffer
	d := &storagev1.GetStorageDiagnosticsResponse{Waveforms: w, ArchiveConfigured: archiveConfigured}
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

func TestWaveformStatTilesCoverEveryState(t *testing.T) {
	html := renderWaveformsArchive(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true,
		ByUploadState: map[string]int64{
			"analyzed": 1200, "not_local": 300, "archive_skipped": 2293395,
			"unavailable": 914, "failed": 37, "to_recompute": 5000,
			"partial": 12, "never_analyzed": 8800,
		},
	}, true)
	for _, want := range []string{"1200", "300", "2293395", "914", "37", "5000", "12", "8800"} {
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
		Enabled: true, ByUploadState: map[string]int64{"analyzed": 10},
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

// The counts come from one periodic pass, so the tiles an operator watches for
// movement must say how stale that pass is rather than imply they are live.
func TestWaveformTilesReportSampleAge(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, BackfillEnabled: true,
		ByUploadState: map[string]int64{"never_analyzed": 151600},
		SampledAgeNs:  int64(90 * time.Second),
	})
	if !strings.Contains(html, "Never Analyzed") || !strings.Contains(html, "151600") {
		t.Fatalf("outstanding work must be shown:\n%s", html)
	}
	if !strings.Contains(html, "as of") {
		t.Fatalf("must disclose the sample age:\n%s", html)
	}
}

func TestWaveformTilesBeforeFirstSample(t *testing.T) {
	html := renderWaveforms(t, &storagev1.WaveformStatus{Enabled: true})
	if !strings.Contains(html, "not yet reached") {
		t.Fatalf("must render before any sample exists:\n%s", html)
	}
	if strings.Contains(html, "as of") {
		t.Fatalf("must not claim an age it does not have:\n%s", html)
	}
}

// Without an archive bucket nothing can ever be routed to archive, so the tile
// would report a permanent zero and only add noise.
func TestSkippedArchiveTileHiddenWithoutArchiveStorage(t *testing.T) {
	w := &storagev1.WaveformStatus{
		Enabled:       true,
		ByUploadState: map[string]int64{"archive_skipped": 2293395},
	}
	if html := renderWaveformsArchive(t, w, false); strings.Contains(html, "Skipped Archive") {
		t.Fatalf("tile must be hidden without archive storage:\n%s", html)
	}
	html := renderWaveformsArchive(t, w, true)
	if !strings.Contains(html, "Skipped Archive") || !strings.Contains(html, "2293395") {
		t.Fatalf("tile must show once archive storage exists:\n%s", html)
	}
	if !strings.Contains(html, "blobs skipped due to being in archive storage") {
		t.Fatalf("sublabel must say why they were skipped:\n%s", html)
	}
}

// Unlinked rows are not upload-keyed, so no tile above can account for them.
// They are the signal that the rest of the section is describing a subset.
func TestUnlinkedRowsTileAppearsOnlyWhenNonZero(t *testing.T) {
	clean := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, ByUploadState: map[string]int64{"analyzed": 10},
	})
	if strings.Contains(clean, "Unlinked Rows") {
		t.Fatalf("a healthy node must not carry a permanent zero tile:\n%s", clean)
	}

	broken := renderWaveforms(t, &storagev1.WaveformStatus{
		Enabled: true, OrphanRows: 417,
	})
	if !strings.Contains(broken, "Unlinked Rows") || !strings.Contains(broken, "417") {
		t.Fatalf("unlinked rows must surface when they exist:\n%s", broken)
	}
	if !strings.Contains(broken, "waveforms matching no upload") {
		t.Fatalf("the tile must say what it counts:\n%s", broken)
	}
}
