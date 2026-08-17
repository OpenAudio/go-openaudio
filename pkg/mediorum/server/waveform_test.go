package server

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
