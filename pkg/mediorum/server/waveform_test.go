package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/persistence"
	"github.com/stretchr/testify/require"
)

// -- pure algorithm tests -------------------------------------------------
//
// These exercise the accumulator directly. All the subtle failure modes live
// here -- NaN from empty buckets, RMS-of-RMS after a merge, silence dividing
// by zero -- and none of them need ffmpeg or a database to reproduce.

func pushAll(a *waveformAccumulator, samples []int16) {
	for _, s := range samples {
		a.push(s)
	}
}

func TestWaveformConstantAmplitudeIsFlat(t *testing.T) {
	a := newWaveformAccumulator()

	// 5 seconds of a constant-amplitude square wave. Every bucket sees the
	// same energy, so normalization must drive all of them to full scale.
	n := waveformSampleRate * 5
	samples := make([]int16, n)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 16000
		} else {
			samples[i] = -16000
		}
	}
	pushAll(a, samples)

	peaks, sampleCount, err := a.finalize()
	require.NoError(t, err)
	require.Len(t, peaks, waveformBuckets)
	require.Equal(t, int64(n), sampleCount)

	for i, v := range peaks {
		require.Equal(t, uint8(255), v, "bucket %d should be full scale", i)
	}
}

func TestWaveformSilenceIsAllZeroAndNotNaN(t *testing.T) {
	a := newWaveformAccumulator()
	pushAll(a, make([]int16, waveformSampleRate*3))

	peaks, sampleCount, err := a.finalize()
	require.NoError(t, err)
	require.Len(t, peaks, waveformBuckets)
	require.Equal(t, int64(waveformSampleRate*3), sampleCount)

	// A global max of zero is the divide-by-zero case; every byte must be a
	// clean 0 rather than a NaN cast to uint8.
	require.Equal(t, bytes.Repeat([]byte{0}, waveformBuckets), peaks)
}

func TestWaveformShortInputStillFillsAllBuckets(t *testing.T) {
	// Well under a second, so there are far fewer frames than buckets. The
	// naive range calculation yields empty buckets here, which produced 0/0
	// and garbage bytes.
	a := newWaveformAccumulator()
	samples := make([]int16, 500)
	for i := range samples {
		samples[i] = 12000
	}
	pushAll(a, samples)

	peaks, _, err := a.finalize()
	require.NoError(t, err)
	require.Len(t, peaks, waveformBuckets)

	// Nearest-neighbour upsampling: constant input means every bucket is full
	// scale, and critically none are zero from an empty range.
	for i, v := range peaks {
		require.Equal(t, uint8(255), v, "bucket %d should be upsampled, not empty", i)
	}
}

func TestWaveformEmptyInputIsAnError(t *testing.T) {
	a := newWaveformAccumulator()
	_, _, err := a.finalize()
	// A zero-length or non-audio blob must not present as a silent track.
	require.ErrorIs(t, err, errWaveformNoAudio)
}

// referenceWaveform is a deliberately naive implementation: contiguous
// sample-exact buckets, RMS per bucket, normalized to the max. It exists to
// hold the frame/merge/reduce machinery honest.
func referenceWaveform(samples []int16) []float64 {
	out := make([]float64, waveformBuckets)
	maxV := 0.0
	n := len(samples)
	for b := 0; b < waveformBuckets; b++ {
		start := b * n / waveformBuckets
		end := (b + 1) * n / waveformBuckets
		if end <= start {
			end = start + 1
		}
		if end > n {
			end = n
		}
		var sum float64
		for i := start; i < end; i++ {
			f := float64(samples[i]) / 32768.0
			sum += f * f
		}
		out[b] = math.Sqrt(sum / float64(end-start))
		if out[b] > maxV {
			maxV = out[b]
		}
	}
	if maxV > 0 {
		for b := range out {
			out[b] /= maxV
		}
	}
	return out
}

func TestWaveformMatchesReferenceAfterHalving(t *testing.T) {
	// Long enough to overflow the frame buffer several times, so the merge
	// path is what produces the answer. At 32 samples/frame the first halving
	// lands around 35s; five minutes forces three or four. Kept short because
	// the package runs under a 60s timeout.
	seconds := 5 * 60
	n := waveformSampleRate * seconds
	samples := make([]int16, n)

	// A slow amplitude sweep under a tone. The envelope varies across buckets,
	// so a merge bug shows up as drift rather than being masked by a constant.
	for i := 0; i < n; i++ {
		envelope := 0.15 + 0.85*float64(i)/float64(n)
		tone := math.Sin(2 * math.Pi * 440 * float64(i) / float64(waveformSampleRate))
		samples[i] = int16(envelope * tone * 30000)
	}

	a := newWaveformAccumulator()
	pushAll(a, samples)
	peaks, sampleCount, err := a.finalize()
	require.NoError(t, err)
	require.Equal(t, int64(n), sampleCount)

	// Halving must actually have happened, or this test proves nothing.
	require.Greater(t, a.frameSize, waveformInitialFrameSize,
		"expected the accumulator to have merged frames")

	want := referenceWaveform(samples)
	for b := 0; b < waveformBuckets; b++ {
		expected := math.Round(want[b] * 255)
		got := float64(peaks[b])
		// Buckets are frame-aligned rather than sample-exact, so allow a
		// couple of quantization steps. A broken merge drifts far more.
		require.InDelta(t, expected, got, 2,
			"bucket %d diverged from reference", b)
	}
}

func TestWaveformMergePreservesEnergy(t *testing.T) {
	a := newWaveformAccumulator()

	n := waveformMaxFrames*waveformInitialFrameSize + 1234
	samples := make([]int16, n)
	for i := range samples {
		samples[i] = int16(1000 + i%5000)
	}
	pushAll(a, samples)
	a.flushFrame()

	// Merging sums and counts is exact; only time resolution is lost.
	var totalCount int64
	var totalSum float64
	for i := 0; i < a.n; i++ {
		totalCount += a.counts[i]
		totalSum += a.sums[i]
	}
	require.Equal(t, int64(n), totalCount)

	var wantSum float64
	for _, s := range samples {
		f := float64(s) / 32768.0
		wantSum += f * f
	}
	require.InEpsilon(t, wantSum, totalSum, 1e-9)
}

func TestSummarizeFFmpegErrorIsBounded(t *testing.T) {
	require.Equal(t, "no ffmpeg diagnostics", summarizeFFmpegError("   \n  "))

	long := ""
	for i := 0; i < 500; i++ {
		long += fmt.Sprintf("line %d of noise\n", i)
	}
	got := summarizeFFmpegError(long)
	require.LessOrEqual(t, len(got), 403)
	// The tail is kept: ffmpeg reports the real failure last.
	require.Contains(t, got, "499")
}

func TestBoundedBufferStopsAtLimitButNeverFails(t *testing.T) {
	b := &boundedBuffer{limit: 10}
	n, err := b.Write([]byte("0123456789abcdef"))
	require.NoError(t, err)
	// Reports a full write so truncating diagnostics cannot fail ffmpeg.
	require.Equal(t, 16, n)
	require.Equal(t, "0123456789", b.String())
}

// -- ffmpeg-backed tests --------------------------------------------------

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
}

// synthAudioFile renders a test tone to a real audio file via ffmpeg's lavfi
// source, so computeWaveform gets a genuine decode rather than a stub.
func synthAudioFile(t *testing.T, spec string, seconds int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tone.mp3")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("%s:duration=%d", spec, seconds),
		"-b:a", "320k", "-ar", "48000", path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg synth failed: %s", string(out))
	return path
}

func TestComputeWaveformOnRealAudio(t *testing.T) {
	requireFFmpeg(t)

	// Deliberately past the 120s truncation the BPM/key path applies -- a
	// full-track waveform must not inherit that limit. Only just past it,
	// because the package runs under a 60s timeout.
	const seconds = 130
	path := synthAudioFile(t, "sine=frequency=1000", seconds)

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	info, err := f.Stat()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := computeWaveform(ctx, f, info.Size())
	require.NoError(t, err)

	require.Len(t, result.Peaks, waveformBuckets)
	require.Equal(t, waveformSampleRate, result.SampleRate)

	// The whole track was decoded, not just the first two minutes.
	require.InDelta(t, seconds*1000, result.DurationMs, 1500)

	// A steady sine normalizes to a flat, full-scale envelope. The bound is
	// loose because mp3 encoder delay makes the first and last buckets ramp;
	// the point is that the envelope is flat everywhere, not that it is exact.
	var maxPeak uint8
	loud := 0
	for i, v := range result.Peaks {
		if v > maxPeak {
			maxPeak = v
		}
		require.Greater(t, int(v), 200, "bucket %d unexpectedly quiet", i)
		if v > 245 {
			loud++
		}
	}
	require.Equal(t, uint8(255), maxPeak)
	require.Greater(t, loud, 700, "expected a flat envelope across the track")
}

func TestComputeWaveformDetectsShortSource(t *testing.T) {
	requireFFmpeg(t)

	path := synthAudioFile(t, "sine=frequency=440", 5)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	info, err := f.Stat()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Claim the source is larger than it is, standing in for a bucket read
	// that ends early without reporting an error. Silently analyzing a
	// fraction of the track and marking it done is the failure to avoid.
	_, err = computeWaveform(ctx, f, info.Size()+4096)
	require.Error(t, err)
	require.Contains(t, err.Error(), "short read")
}

func TestComputeWaveformRejectsNonAudio(t *testing.T) {
	requireFFmpeg(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := computeWaveform(ctx, bytes.NewReader([]byte("this is not audio at all")), 0)
	require.Error(t, err)
}

// -- serving tests --------------------------------------------------------
//
// These run against the real test network, so they exercise the middleware
// chain and the cross-node probe rather than the handler in isolation.

// uploadID may be empty for rows that stand in for legacy Qm content, which has
// no upload row. Discovery correlates on upload_id, so a row seeded without one
// does not count against its upload.
func insertTestWaveform(t *testing.T, ss *MediorumServer, cid, uploadID string) {
	t.Helper()
	peaks := make([]byte, waveformBuckets)
	for i := range peaks {
		peaks[i] = uint8(i % 256)
	}
	_, err := ss.pgPool.Exec(context.Background(), `
		insert into waveforms (cid, peaks, buckets, version, sample_rate, sample_count, duration_ms, status, upload_id, analyzed_at)
		values ($1, $2, $3, $4, $5, $6, $7, 'done', $8, now())
		on conflict (cid) do update set peaks = excluded.peaks, status = 'done', upload_id = excluded.upload_id
	`, cid, peaks, waveformBuckets, waveformVersion, waveformSampleRate, int64(waveformSampleRate*10), int64(10000),
		nullableUploadID(uploadID))
	require.NoError(t, err)
}

func deleteTestWaveform(t *testing.T, ss *MediorumServer, cid string) {
	t.Helper()
	_, _ = ss.pgPool.Exec(context.Background(), `delete from waveforms where cid = $1`, cid)
}

// noRedirectClient reports the redirect instead of following it.
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestServeWaveformReturnsStoredPeaks(t *testing.T) {
	ss := testNetwork[0]
	cid := fmt.Sprintf("waveform-serve-%d", time.Now().UnixNano())
	insertTestWaveform(t, ss, cid, "")
	t.Cleanup(func() { deleteTestWaveform(t, ss, cid) })

	resp, err := noRedirectClient().Get(ss.Config.Self.Host + "/waveform/" + cid)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got waveformResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, cid, got.CID)
	require.Len(t, got.Peaks, waveformBuckets)
	require.Equal(t, waveformSampleRate, got.SampleRate)
}

func TestServeWaveformHeadOmitsBody(t *testing.T) {
	ss := testNetwork[0]
	cid := fmt.Sprintf("waveform-head-%d", time.Now().UnixNano())
	insertTestWaveform(t, ss, cid, "")
	t.Cleanup(func() { deleteTestWaveform(t, ss, cid) })

	// This is the shape peers probe with, so it must answer without paying to
	// serialize 750 bytes of peaks.
	req, err := http.NewRequest(http.MethodHead, ss.Config.Self.Host+"/waveform/"+cid+"?localOnly=true", nil)
	require.NoError(t, err)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Empty(t, body)
}

func TestServeWaveformLocalOnlyDoesNotRedirect(t *testing.T) {
	// A node that lacks the waveform must answer 404 under localOnly rather
	// than forwarding. Without this a probe would recurse across the network.
	holder := testNetwork[0]
	asked := testNetwork[1]
	cid := fmt.Sprintf("waveform-localonly-%d", time.Now().UnixNano())
	insertTestWaveform(t, holder, cid, "")
	t.Cleanup(func() { deleteTestWaveform(t, holder, cid) })

	resp, err := noRedirectClient().Get(asked.Config.Self.Host + "/waveform/" + cid + "?localOnly=true")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestServeWaveformRedirectsToPeerThatHasIt(t *testing.T) {
	asked := testNetwork[1]
	cid := fmt.Sprintf("waveform-redirect-%d", time.Now().UnixNano())

	// Seed every other node so the answer does not depend on where this cid
	// happens to land in the rendezvous ranking.
	for i, ss := range testNetwork {
		if i == 1 {
			continue
		}
		insertTestWaveform(t, ss, cid, "")
		t.Cleanup(func() { deleteTestWaveform(t, ss, cid) })
	}

	resp, err := noRedirectClient().Get(asked.Config.Self.Host + "/waveform/" + cid)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.Contains(t, loc, "/waveform/"+cid)
	require.NotContains(t, loc, asked.Config.Self.Host, "must not redirect to itself")
}

func TestServeWaveformMissEverywhereDoesNotQueueWork(t *testing.T) {
	// On-demand analysis was removed: an unauthenticated GET must not be able
	// to schedule a decode, which on a StoreAll node could mean a cold-storage
	// retrieval the backfill sweep deliberately refuses.
	ss := testNetwork[0]
	cid := fmt.Sprintf("waveform-absent-%d", time.Now().UnixNano())

	before := len(ss.waveformWork)
	resp, err := noRedirectClient().Get(ss.Config.Self.Host + "/waveform/" + cid)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, before, len(ss.waveformWork), "request must not enqueue analysis")
}

// -- versioning and cursor tests -----------------------------------------

func TestWaveformVersionTracksParameters(t *testing.T) {
	// The version has to be a pure function of the things that change the
	// output, or a parameter change leaves stale waveforms in place silently.
	require.Equal(t, waveformVersion, computeWaveformVersion())
	require.Positive(t, waveformVersion, "must be positive; it lands in an int column")

	// Any input differing must produce a different fingerprint.
	base := fingerprint(waveformAlgorithmVersion, waveformBuckets, waveformSampleRate, waveformInitialFrameSize, waveformMaxFrames)
	require.Equal(t, waveformVersion, base)

	for _, c := range []struct {
		name                                string
		alg, buckets, rate, frame, maxFrame int
	}{
		{"algorithm", waveformAlgorithmVersion + 1, waveformBuckets, waveformSampleRate, waveformInitialFrameSize, waveformMaxFrames},
		{"buckets", waveformAlgorithmVersion, waveformBuckets + 1, waveformSampleRate, waveformInitialFrameSize, waveformMaxFrames},
		{"sample rate", waveformAlgorithmVersion, waveformBuckets, waveformSampleRate * 2, waveformInitialFrameSize, waveformMaxFrames},
		{"frame size", waveformAlgorithmVersion, waveformBuckets, waveformSampleRate, waveformInitialFrameSize * 2, waveformMaxFrames},
		{"max frames", waveformAlgorithmVersion, waveformBuckets, waveformSampleRate, waveformInitialFrameSize, waveformMaxFrames * 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.NotEqual(t, base, fingerprint(c.alg, c.buckets, c.rate, c.frame, c.maxFrame),
				"changing %s must invalidate stored waveforms", c.name)
		})
	}
}

// fingerprint mirrors computeWaveformVersion with the inputs made explicit, so
// a test can vary one at a time.
func fingerprint(alg, buckets, rate, frame, maxFrames int) int {
	h := fnv.New32a()
	fmt.Fprintf(h, "alg=%d;buckets=%d;rate=%d;frame=%d;maxframes=%d", alg, buckets, rate, frame, maxFrames)
	return int(h.Sum32() & 0x7fffffff)
}

func clearWaveformCursor(t *testing.T, ss *MediorumServer) {
	t.Helper()
	_, err := ss.pgPool.Exec(context.Background(), `delete from waveform_cursor where id = 1`)
	require.NoError(t, err)
}

func readCursor(t *testing.T, ss *MediorumServer) waveformCursor {
	t.Helper()
	cur, err := ss.getWaveformCursor(context.Background())
	require.NoError(t, err)
	return cur
}

func TestWaveformCursorFirstRunIsNotAVersionChange(t *testing.T) {
	ss := testNetwork[0]
	clearWaveformCursor(t, ss)
	t.Cleanup(func() { clearWaveformCursor(t, ss) })

	// An absent row must report the current version, otherwise every first run
	// would look like a version change and log a spurious restart.
	cur := readCursor(t, ss)
	require.Equal(t, waveformVersion, cur.Version)
	require.False(t, cur.Exhausted)
}

// seedAudioUpload creates an upload with a 320 result and no waveform, i.e.
// exactly what a discovery sweep is supposed to find.
func seedAudioUpload(t *testing.T, ss *MediorumServer, prefix string) (Upload, string) {
	t.Helper()
	cid := prefix + "cid320"
	upload := Upload{
		ID:               prefix + "upload",
		Template:         JobTemplateAudio,
		CreatedAt:        time.Now().UTC().Truncate(time.Second),
		TranscodeResults: map[string]string{"320": cid},
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	t.Cleanup(func() {
		ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
		deleteTestWaveform(t, ss, cid)
	})
	return upload, cid
}

// backdateWaveformAttempt moves a row's last attempt into the past so its
// backoff has elapsed. Tests used to express this by stamping a negative
// interval; with the schedule derived, the fact itself is what moves.
func backdateWaveformAttempt(t *testing.T, ss *MediorumServer, cid string, ago time.Duration) {
	t.Helper()
	_, err := ss.pgPool.Exec(context.Background(),
		`update waveforms set last_attempted_at = now() - $2::interval where cid = $1`,
		cid, pgInterval(ago))
	require.NoError(t, err)
}

func drainWaveformWork(ss *MediorumServer) []string {
	cids := []string{}
	for {
		select {
		case job := <-ss.waveformWork:
			cids = append(cids, job.cid)
		default:
			return cids
		}
	}
}

func TestWaveformCursorExhaustionIsNotPermanent(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	clearWaveformCursor(t, ss)
	drainWaveformWork(ss)
	t.Cleanup(func() { clearWaveformCursor(t, ss); drainWaveformWork(ss) })

	require.NoError(t, ss.setWaveformCursorExhausted(ctx))
	cur := readCursor(t, ss)
	require.True(t, cur.Exhausted)
	require.Equal(t, waveformVersion, cur.Version)
	// updated_at is what the re-walk timer reads, so it must be stamped.
	require.WithinDuration(t, time.Now(), cur.UpdatedAt, time.Minute)

	// An upload this node learned about after the walk had already passed its
	// position. A descending cursor never goes back for it, so latching
	// exhausted forever left it without a waveform for good.
	_, cid := seedAudioUpload(t, ss, fmt.Sprintf("waveform-rewalk-%d-", time.Now().UnixNano()))

	// While still inside the interval, nothing should happen.
	ss.sweepWaveformDiscovery(ctx)
	require.Empty(t, drainWaveformWork(ss), "must not re-walk before the interval elapses")
	require.True(t, readCursor(t, ss).Exhausted)

	// Past the interval, the walk starts over and finds it.
	_, err := ss.pgPool.Exec(ctx,
		`update waveform_cursor set updated_at = now() - $1::interval where id = 1`,
		fmt.Sprintf("%d seconds", int(waveformRewalkInterval.Seconds())+60))
	require.NoError(t, err)

	ss.sweepWaveformDiscovery(ctx)
	require.Contains(t, drainWaveformWork(ss), cid, "a stale exhausted cursor must re-walk and find late arrivals")
}

func TestWaveformCursorResetsOnVersionChange(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	clearWaveformCursor(t, ss)
	drainWaveformWork(ss)
	t.Cleanup(func() { clearWaveformCursor(t, ss); drainWaveformWork(ss) })

	// Already analyzed, but under different settings.
	upload, cid := seedAudioUpload(t, ss, fmt.Sprintf("waveform-versionreset-%d-", time.Now().UnixNano()))
	insertTestWaveform(t, ss, cid, upload.ID)
	_, err := ss.pgPool.Exec(ctx, `update waveforms set version = $1 where cid = $2`, waveformVersion+1, cid)
	require.NoError(t, err)

	// A walk that finished under that older version, positioned past this
	// upload so only a reset could reach it again.
	_, err = ss.pgPool.Exec(ctx, `
		insert into waveform_cursor (id, created_at, upload_id, exhausted, version, updated_at)
		values (1, now() - interval '10 years', 'some-upload', true, $1, now())
	`, waveformVersion+1)
	require.NoError(t, err)

	// No interval wait needed: a version change restarts the walk immediately,
	// since every stored waveform is now computed under the wrong rules.
	ss.sweepWaveformDiscovery(ctx)

	require.Contains(t, drainWaveformWork(ss), cid, "a version change must re-enqueue stale waveforms")
	require.Equal(t, waveformVersion, readCursor(t, ss).Version, "cursor must adopt the running version")
}

func TestWaveformDiscoveryTreatsStaleVersionAsAbsent(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("waveform-stale-%d-", now.UnixNano())
	cid := prefix + "cid320"

	upload := Upload{
		ID:               prefix + "upload",
		Template:         JobTemplateAudio,
		CreatedAt:        now,
		TranscodeResults: map[string]string{"320": cid},
	}
	require.NoError(t, ss.crud.DB.Create(&upload).Error)
	t.Cleanup(func() {
		ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
		deleteTestWaveform(t, ss, cid)
	})

	// Current version present -> the upload is considered done.
	insertTestWaveform(t, ss, cid, upload.ID)
	batch, err := ss.nextWaveformUploadBatch(ctx, time.Time{}, "", 500)
	require.NoError(t, err)
	require.NotContains(t, waveformUploadIDs(batch), upload.ID)

	// Same row stamped with a different version -> it must look absent, which
	// is what makes a parameter change re-backfill without a separate sweep.
	_, err = ss.pgPool.Exec(ctx, `update waveforms set version = $1 where cid = $2`, waveformVersion+1, cid)
	require.NoError(t, err)

	batch, err = ss.nextWaveformUploadBatch(ctx, time.Time{}, "", 500)
	require.NoError(t, err)
	require.Contains(t, waveformUploadIDs(batch), upload.ID, "a stale-version row must be recomputed")
}

// named apart from transcode_test.go's uploadIDs, which takes []*Upload
func waveformUploadIDs(uploads []Upload) []string {
	ids := make([]string, 0, len(uploads))
	for _, u := range uploads {
		ids = append(ids, u.ID)
	}
	return ids
}

func TestNonTerminalRowsAreStampedAndHiddenFromDiscovery(t *testing.T) {
	// A blob missing during one pass and replicated later must still get a
	// waveform, but the retry sweep alone should schedule that. If these rows
	// carry no version, discovery treats the upload as outstanding forever and
	// re-enqueues it on every re-walk, bypassing the backoff entirely.
	ss := testNetwork[0]
	ctx := context.Background()
	drainWaveformWork(ss)
	t.Cleanup(func() { drainWaveformWork(ss) })

	upload, cid := seedAudioUpload(t, ss, fmt.Sprintf("waveform-notlocal-%d-", time.Now().UnixNano()))

	// Discovery sees it while nothing is recorded.
	batch, err := ss.nextWaveformUploadBatch(ctx, time.Time{}, "", 500)
	require.NoError(t, err)
	require.Contains(t, waveformUploadIDs(batch), upload.ID)

	// The blob is not on this node.
	require.NoError(t, ss.markWaveformStatus(ctx, cid, upload.ID, waveformStatusNotLocal, nil))

	row, err := ss.getWaveform(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, waveformStatusNotLocal, row.Status)
	require.Equal(t, waveformVersion, row.Version, "non-terminal rows must carry the running version")

	batch, err = ss.nextWaveformUploadBatch(ctx, time.Time{}, "", 500)
	require.NoError(t, err)
	require.NotContains(t, waveformUploadIDs(batch), upload.ID,
		"discovery must leave scheduling to the retry sweep")

	// The retry sweep still owns it, and not_local never spends the retry
	// budget, so it keeps coming back until the blob turns up.
	var errCount int
	var lastAttempt *time.Time
	require.NoError(t, ss.pgPool.QueryRow(ctx,
		`select error_count, last_attempted_at from waveforms where cid = $1`, cid,
	).Scan(&errCount, &lastAttempt))
	require.Zero(t, errCount, "not_local must not spend the retry budget")
	require.NotNil(t, lastAttempt, "the attempt must be recorded for the backoff to run from")

	// Once the blob replicates in and analysis succeeds, it becomes servable.
	require.NoError(t, ss.upsertWaveform(ctx, cid, "", &waveformResult{
		Peaks:       make([]byte, waveformBuckets),
		SampleRate:  waveformSampleRate,
		SampleCount: int64(waveformSampleRate),
		DurationMs:  1000,
	}))
	row, err = ss.getWaveform(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, waveformStatusDone, row.Status)
	require.Equal(t, waveformVersion, row.Version)
}

func TestAnalyzeWaveformRefusesArchiveTierAtTheRead(t *testing.T) {
	// A cid's tier is not fixed: rendezvous rank shifts when the validator set
	// changes, so a row recorded while it was primary-tier can be re-queued by
	// the retry sweep after it has become archive-tier. readBlob falls back to
	// archive unconditionally, so without a check at the read that path pulls
	// from cold storage with the flag off.
	ss := testNetwork[0]
	ctx := context.Background()

	archive, err := persistence.Open("file://" + t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { archive.Close() })

	origBucket, origStoreAll, origFlag := ss.archiveBucket, ss.Config.StoreAll, ss.Config.WaveformArchiveEnabled
	ss.archiveBucket, ss.Config.StoreAll, ss.Config.WaveformArchiveEnabled = archive, true, false
	t.Cleanup(func() {
		ss.archiveBucket, ss.Config.StoreAll, ss.Config.WaveformArchiveEnabled = origBucket, origStoreAll, origFlag
	})

	// Find a cid this node would route to archive, i.e. one it holds only
	// because StoreAll.
	var cid string
	for i := 0; i < 500 && cid == ""; i++ {
		candidate := fmt.Sprintf("waveform-archive-%d-%d", time.Now().UnixNano(), i)
		if ss.isArchiveCID(candidate, nil) {
			cid = candidate
		}
	}
	require.NotEmpty(t, cid, "expected some cid to rank into the archive tier")
	t.Cleanup(func() { deleteTestWaveform(t, ss, cid) })

	// Queued directly, as the retry sweep would after an earlier attempt.
	require.NoError(t, ss.analyzeWaveform(ctx, waveformJob{cid: cid}))

	row, err := ss.getWaveform(ctx, cid)
	require.NoError(t, err)
	// The distinguishing assertion: had the guard not fired, the blob is absent
	// from both buckets, so this would read and record not_local instead.
	require.Equal(t, waveformStatusArchiveSkipped, row.Status,
		"must skip before touching a bucket, not after failing to read one")

	// With the flag on it proceeds to the read and reports the blob missing.
	ss.Config.WaveformArchiveEnabled = true
	require.Error(t, ss.analyzeWaveform(ctx, waveformJob{cid: cid}))
	row, err = ss.getWaveform(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, waveformStatusNotLocal, row.Status)
}

func TestDiscoveryCursorStopsAtAFullQueue(t *testing.T) {
	// The cursor may only advance over uploads actually dealt with. Advancing
	// to the end of the batch regardless means anything a full queue rejected
	// is skipped until the next re-walk hours later -- silently, since the
	// enqueue result was discarded.
	ss := testNetwork[0]
	ctx := context.Background()
	clearWaveformCursor(t, ss)
	drainWaveformWork(ss)
	t.Cleanup(func() { clearWaveformCursor(t, ss); drainWaveformWork(ss) })

	// Dated into the future so the newest-first walk reaches these before
	// anything else in the table, making the assertions deterministic.
	prefix := fmt.Sprintf("waveform-backpressure-%d-", time.Now().UnixNano())
	base := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	const seeded = 5
	cids := make([]string, seeded)
	for i := 0; i < seeded; i++ {
		cid := fmt.Sprintf("%scid%d", prefix, i)
		cids[i] = cid
		upload := Upload{
			ID:               fmt.Sprintf("%supload%d", prefix, i),
			Template:         JobTemplateAudio,
			CreatedAt:        base.Add(-time.Duration(i) * time.Minute), // descending
			TranscodeResults: map[string]string{"320": cid},
		}
		require.NoError(t, ss.crud.DB.Create(&upload).Error)
		t.Cleanup(func() {
			ss.crud.DB.Where("id = ?", upload.ID).Delete(&Upload{})
			deleteTestWaveform(t, ss, cid)
		})
	}

	// Start the walk immediately above the seeded rows. Relying on them simply
	// being the newest in the table would make this depend on whatever else
	// the suite has left lying around.
	require.NoError(t, ss.setWaveformCursor(ctx, base.Add(time.Second), "~", 0, 0))

	// Leave room for exactly two before the queue is full.
	const free = 2
	filler := cap(ss.waveformWork) - free
	for i := 0; i < filler; i++ {
		require.True(t, ss.enqueueWaveformJob(waveformJob{cid: fmt.Sprintf("filler-%d", i)}))
	}

	require.True(t, ss.sweepWaveformDiscovery(ctx), "work remains, so the sweep must say so")

	queued := drainWaveformWork(ss)
	require.Len(t, queued, cap(ss.waveformWork), "queue should be full")
	accepted := queued[filler:]
	require.Equal(t, []string{cids[0], cids[1]}, accepted,
		"only the jobs that fit should have been queued")

	// The cursor must sit on the last accepted upload, so the next sweep
	// resumes at the third rather than past all five.
	cur := readCursor(t, ss)
	require.Equal(t, fmt.Sprintf("%supload1", prefix), cur.UploadID,
		"cursor must not advance past work the queue rejected")
}

// With backfill off the loop must still run. Returning early would strand the
// retry sweep, so a transient failure from the live transcode hook -- written
// with a next_attempt_at nobody reads -- would never be re-attempted, and the
// outstanding count would sit at zero while nothing had been looked at.
func TestSweepsRunWithBackfillDisabled(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	drainWaveformWork(ss)
	clearWaveformCursor(t, ss)
	t.Cleanup(func() { drainWaveformWork(ss); clearWaveformCursor(t, ss) })

	origBackfill := ss.Config.WaveformBackfillEnabled
	ss.Config.WaveformBackfillEnabled = false
	t.Cleanup(func() { ss.Config.WaveformBackfillEnabled = origBackfill })

	// A row the live hook could have written and failed on, its backoff spent.
	// Backdating the attempt is the whole of "already due" now -- there is no
	// schedule to stamp, so nothing has to encode the intent separately.
	cid := fmt.Sprintf("waveform-liveonly-%d", time.Now().UnixNano())
	t.Cleanup(func() { deleteTestWaveform(t, ss, cid) })
	require.NoError(t, ss.markWaveformStatus(ctx, cid, "", waveformStatusUnavailable, nil))
	backdateWaveformAttempt(t, ss, cid, waveformRetryBackoffUnavailable+time.Minute)

	ss.runWaveformSweeps(ctx)

	require.Contains(t, drainWaveformWork(ss), cid,
		"retries must run even when history is not being walked")

	// And the walk itself stays put, which is what backfill-off means.
	cur := readCursor(t, ss)
	require.True(t, cur.CreatedAt.IsZero(), "history must not be walked")
}

// -- replication handoff --------------------------------------------------
//
// The temp file has exactly one owner at a time. enqueueWaveformJob reports
// the transfer definitively, so these pin both sides of it: the worker releases
// what it accepted, and the enqueue site releases what it could not hand over.

func TestWorkerReleasesHandedOverFile(t *testing.T) {
	ss := testNetwork[0]
	ctx := context.Background()
	drainWaveformWork(ss)
	t.Cleanup(func() { drainWaveformWork(ss) })

	tmp, err := os.CreateTemp("", "waveform-handoff-*")
	require.NoError(t, err)
	path := tmp.Name()
	// Not audio, so analysis fails -- the file must still be released.
	_, err = tmp.WriteString("not audio")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())

	cid := fmt.Sprintf("waveform-handoff-%d", time.Now().UnixNano())
	t.Cleanup(func() { deleteTestWaveform(t, ss, cid); os.Remove(path) })

	ss.processWaveformJob(ctx, waveformJob{cid: cid, localPath: path})

	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr),
		"the worker owns the file once it accepts the job and must release it even when analysis fails")
}

func TestFullQueueLeavesNoOrphanedFile(t *testing.T) {
	ss := testNetwork[0]
	drainWaveformWork(ss)
	t.Cleanup(func() { drainWaveformWork(ss) })

	// Fill the queue so the handoff cannot succeed.
	for i := 0; i < cap(ss.waveformWork); i++ {
		require.True(t, ss.enqueueWaveformJob(waveformJob{cid: fmt.Sprintf("filler-%d", i)}))
	}

	tmp, err := os.CreateTemp("", "waveform-drop-*")
	require.NoError(t, err)
	path := tmp.Name()
	require.NoError(t, tmp.Close())
	t.Cleanup(func() { os.Remove(path) })

	// Mirrors the replication path: ownership is kept unless the send succeeds.
	handedOff := false
	func() {
		defer func() {
			if !handedOff {
				os.Remove(path)
			}
		}()
		if ss.enqueueWaveformJob(waveformJob{cid: "dropped", localPath: path}) {
			handedOff = true
		}
	}()

	require.False(t, handedOff, "a full queue must report the drop rather than silently accept")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "a dropped job must not leak its file")
}
