package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os/exec"
	"strings"
)

const (
	// waveformBuckets, waveformSampleRate, waveformInitialFrameSize and
	// waveformMaxFrames are compile-time constants rather than config, and
	// must stay that way.
	//
	// Waveforms are not replicated: every node computes its own. A client that
	// re-fetches a CID from a different node after a reroute must still get
	// identical bytes, so every input to the computation has to be pinned. An
	// operator-tunable bucket count or sample rate would make two nodes
	// disagree silently, which is worse than either value being wrong.
	waveformBuckets          = 750
	waveformSampleRate       = 44100
	waveformInitialFrameSize = 32
	waveformMaxFrames        = 48000

	// waveformAlgorithmVersion covers changes the constants below cannot
	// describe -- a different accumulation strategy, a different normalization,
	// a second array in the payload. Bump it by hand for those.
	waveformAlgorithmVersion = 1

	// waveformMaxDecodeSeconds is a poison-pill guard against a blob that
	// decodes to something absurd. It sits alongside the per-job context
	// timeout, which covers hangs rather than runaway length.
	waveformMaxDecodeSeconds = 14400

	// waveformMaxStderrBytes caps how much ffmpeg diagnostic output we retain.
	// The error string lands in a database column, so it must be bounded.
	waveformMaxStderrBytes = 8 * 1024
)

// waveformVersion is stamped on every row and is what a re-backfill keys off:
// a row whose version differs from this one is treated as absent, so the
// discovery sweep picks it up and recomputes it.
//
// It is derived rather than hand-maintained because the parameters above change
// the output just as surely as the algorithm does. Deriving it means changing
// waveformBuckets or waveformSampleRate cannot silently leave a network full of
// waveforms computed under the old settings, which is exactly the mistake a
// hand-bumped constant invites.
//
// The consequence is that the number is a fingerprint, not a sequence -- it
// does not increase, and comparisons must be equality, never ordering. The
// status endpoint reports the inputs alongside it so it is still debuggable.
var waveformVersion = computeWaveformVersion()

func computeWaveformVersion() int {
	h := fnv.New32a()
	fmt.Fprintf(h, "alg=%d;buckets=%d;rate=%d;frame=%d;maxframes=%d",
		waveformAlgorithmVersion,
		waveformBuckets,
		waveformSampleRate,
		waveformInitialFrameSize,
		waveformMaxFrames,
	)
	// Clear the sign bit: this lands in an int column and a negative version
	// would read as corruption.
	return int(h.Sum32() & 0x7fffffff)
}

// waveformResult is the output of a single analysis.
type waveformResult struct {
	// Peaks holds exactly waveformBuckets bytes, one per bucket: the bucket's
	// RMS amplitude normalized so the loudest bucket is 255.
	Peaks       []byte
	SampleRate  int
	SampleCount int64
	DurationMs  int64
}

// errWaveformNoAudio means ffmpeg produced no samples. A zero-length or
// non-audio blob lands here rather than producing an all-zero waveform, so it
// is recorded as an error instead of masquerading as a silent track.
var errWaveformNoAudio = errors.New("waveform: decoded zero audio samples")

// computeWaveform decodes audio from r and reduces it to a fixed-size
// amplitude envelope.
//
// The reader is streamed straight into ffmpeg; nothing is buffered to disk.
// expectedBytes, when positive, is checked against the number of bytes we
// managed to feed ffmpeg, so a source that ends early without reporting an
// error cannot be silently analyzed as a shorter track.
// errWaveformSourceTooLong marks a source longer than we are willing to decode.
// It is terminal rather than a failure: nothing about the blob is wrong, and no
// number of retries makes it shorter. Reported apart from decode errors so a
// handful of very long uploads cannot read as a decoding problem.
var errWaveformSourceTooLong = errors.New("waveform: source longer than the decode limit")

// decodeStoppedAtCap reports whether the decode ended because it reached
// waveformMaxDecodeSeconds rather than because the source ran out. ffmpeg
// stops on its own -t, so the byte counts look identical to a truncated read.
func decodeStoppedAtCap(sampleCount int64) bool {
	return sampleCount >= int64(waveformMaxDecodeSeconds)*int64(waveformSampleRate)
}

func computeWaveform(ctx context.Context, r io.Reader, expectedBytes int64) (*waveformResult, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner",
		// stdin is our data pipe, so ffmpeg must never treat it as a console.
		"-nostdin",
		"-loglevel", "error",
		"-i", "pipe:0",
		// 320s frequently carry embedded cover art as a video stream. -vn
		// drops it; the explicit audio map guarantees we take the audio
		// stream even if stream ordering is unusual.
		"-vn",
		"-map", "0:a:0",
		"-ac", "1",
		"-ar", fmt.Sprintf("%d", waveformSampleRate),
		"-f", "s16le",
		"-c:a", "pcm_s16le",
		"-t", fmt.Sprintf("%d", waveformMaxDecodeSeconds),
		// Backfill runs alongside transcoding, which caps itself at two threads
		// per worker for the same reason. Decoding to PCM is essentially
		// single-threaded anyway, so pinning this costs nothing and keeps a
		// pool of workers from competing with the node's real work.
		"-threads", "1",
		"pipe:1",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("waveform: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("waveform: stdout pipe: %w", err)
	}
	stderr := &boundedBuffer{limit: waveformMaxStderrBytes}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("waveform: start ffmpeg: %w", err)
	}

	// Feed ffmpeg on a goroutine. Write errors here are expected and ignored:
	// when -t trips, or on a malformed blob, ffmpeg exits before consuming the
	// whole stream and our writes fail with EPIPE. ffmpeg's exit status is the
	// authority on whether the decode worked.
	//
	// Read errors are a different matter, and src records them separately --
	// a failed bucket read must not be mistaken for a short track.
	src := &countingReader{r: r}
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(stdin, src)
		stdin.Close()
	}()

	// Drain stdout to EOF before Wait. StdoutPipe closes the pipe inside
	// Wait, so waiting first would truncate the audio or deadlock.
	acc := newWaveformAccumulator()
	readErr := acc.consumePCM(stdout)

	<-copyDone
	waitErr := cmd.Wait()

	if src.err != nil {
		return nil, fmt.Errorf("waveform: read source: %w", src.err)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("waveform: ffmpeg failed: %w (%s)", waitErr, summarizeFFmpegError(stderr.String()))
	}
	if readErr != nil {
		return nil, fmt.Errorf("waveform: read pcm: %w", readErr)
	}
	peaks, sampleCount, err := acc.finalize()
	if err != nil {
		return nil, err
	}

	// A source that stopped early without erroring would otherwise yield a
	// plausible-looking waveform for a fraction of the track.
	//
	// Except when we are the ones who stopped it. ffmpeg is given -t
	// waveformMaxDecodeSeconds, so on a longer source it exits at the cap and
	// the copier stops with the source unread -- indistinguishable here from a
	// truncated blob by byte count alone. The sample count tells them apart,
	// and conflating them reports a deliberate limit as a corrupt file and
	// retries a multi-hour decode against a source that will never fit.
	if expectedBytes > 0 && src.n != expectedBytes {
		if decodeStoppedAtCap(sampleCount) {
			return nil, errWaveformSourceTooLong
		}
		return nil, fmt.Errorf("waveform: short read from source: got %d bytes, expected %d", src.n, expectedBytes)
	}

	return &waveformResult{
		Peaks:       peaks,
		SampleRate:  waveformSampleRate,
		SampleCount: sampleCount,
		// Exact because the sample rate is pinned, which is what lets us skip
		// an ffprobe call and avoid the uploads.ff_probe column (probed from
		// the original upload, not the 320, and null on older rows).
		DurationMs: sampleCount * 1000 / waveformSampleRate,
	}, nil
}

// waveformAccumulator reduces an arbitrarily long PCM stream to a fixed set of
// buckets in constant memory.
//
// It accumulates sum-of-squares into fixed-size frames. When it runs out of
// frames it merges adjacent pairs and doubles the frame size, so coverage
// doubles each time while memory stays flat. Starting at 32 samples per frame
// it covers ~35s before the first merge and over an hour after six.
//
// Sums and counts are carried separately and sqrt is applied once, at the
// bucket level. Taking RMS per frame and averaging those would be wrong: RMS
// of RMS is not RMS unless every frame holds the same number of samples, which
// stops being true the moment a partial trailing frame exists.
type waveformAccumulator struct {
	sums      []float64
	counts    []int64
	n         int
	frameSize int

	curSum   float64
	curCount int64
}

func newWaveformAccumulator() *waveformAccumulator {
	return &waveformAccumulator{
		sums:      make([]float64, waveformMaxFrames),
		counts:    make([]int64, waveformMaxFrames),
		frameSize: waveformInitialFrameSize,
	}
}

// consumePCM reads little-endian signed 16-bit mono samples until EOF.
func (a *waveformAccumulator) consumePCM(r io.Reader) error {
	br := bufio.NewReaderSize(r, 64*1024)
	buf := make([]byte, 64*1024)

	// A read can split a sample across chunk boundaries.
	var pending byte
	var hasPending bool

	for {
		n, err := br.Read(buf)
		chunk := buf[:n]

		if hasPending && len(chunk) > 0 {
			a.push(int16(binary.LittleEndian.Uint16([]byte{pending, chunk[0]})))
			chunk = chunk[1:]
			hasPending = false
		}
		for len(chunk) >= 2 {
			a.push(int16(binary.LittleEndian.Uint16(chunk)))
			chunk = chunk[2:]
		}
		if len(chunk) == 1 {
			pending = chunk[0]
			hasPending = true
		}

		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (a *waveformAccumulator) push(sample int16) {
	f := float64(sample) / 32768.0
	a.curSum += f * f
	a.curCount++
	if a.curCount >= int64(a.frameSize) {
		a.flushFrame()
	}
}

func (a *waveformAccumulator) flushFrame() {
	if a.curCount == 0 {
		return
	}
	if a.n == waveformMaxFrames {
		a.halve()
	}
	a.sums[a.n] = a.curSum
	a.counts[a.n] = a.curCount
	a.n++
	a.curSum = 0
	a.curCount = 0
}

// halve merges adjacent frame pairs, freeing half the buffer.
//
// Merging sums and counts is exact -- no information is lost beyond time
// resolution, which is the point. waveformMaxFrames is even and we only merge
// at capacity, so there is never an odd frame left over.
func (a *waveformAccumulator) halve() {
	for i := 0; i < a.n/2; i++ {
		a.sums[i] = a.sums[2*i] + a.sums[2*i+1]
		a.counts[i] = a.counts[2*i] + a.counts[2*i+1]
	}
	a.n /= 2
	a.frameSize *= 2
}

// finalize reduces frames to buckets and quantizes to uint8.
func (a *waveformAccumulator) finalize() ([]byte, int64, error) {
	a.flushFrame()
	if a.n == 0 {
		return nil, 0, errWaveformNoAudio
	}

	var sampleCount int64
	for i := 0; i < a.n; i++ {
		sampleCount += a.counts[i]
	}

	rms := make([]float64, waveformBuckets)
	maxRMS := 0.0
	for b := 0; b < waveformBuckets; b++ {
		start := b * a.n / waveformBuckets
		end := (b + 1) * a.n / waveformBuckets
		// When there are fewer frames than buckets -- any clip under ~0.5s --
		// the naive range is empty and would divide by zero. Widening to a
		// single frame turns that case into nearest-neighbour upsampling.
		if end <= start {
			end = start + 1
		}
		if end > a.n {
			end = a.n
		}

		var sum float64
		var count int64
		for i := start; i < end; i++ {
			sum += a.sums[i]
			count += a.counts[i]
		}
		if count > 0 {
			rms[b] = math.Sqrt(sum / float64(count))
		}
		if rms[b] > maxRMS {
			maxRMS = rms[b]
		}
	}

	peaks := make([]byte, waveformBuckets)
	// Digital silence normalizes to 0/0. Leaving peaks all-zero is the honest
	// answer and avoids NaN propagating into every byte.
	if maxRMS > 0 {
		for b := 0; b < waveformBuckets; b++ {
			v := rms[b] / maxRMS
			if v > 1 {
				v = 1
			}
			// Round rather than truncate: truncation loses a full step and
			// biases the whole envelope downward.
			peaks[b] = uint8(math.Round(v * 255))
		}
	}

	return peaks, sampleCount, nil
}

// countingReader records the byte count and the first read error from the
// underlying source, so a bucket failure can be told apart from ffmpeg
// closing the pipe early.
type countingReader struct {
	r   io.Reader
	n   int64
	err error
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if err != nil && err != io.EOF && c.err == nil {
		c.err = err
	}
	return n, err
}

// boundedBuffer keeps at most limit bytes. ffmpeg on a badly broken file can
// emit a great deal of output, and this ends up in a database column.
type boundedBuffer struct {
	buf   []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - len(b.buf); remaining > 0 {
		if len(p) > remaining {
			b.buf = append(b.buf, p[:remaining]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	// Always report a full write; truncating diagnostics must not fail ffmpeg.
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.buf) }

// summarizeFFmpegError collapses ffmpeg stderr into a single short line
// suitable for storing alongside the row.
func summarizeFFmpegError(stderr string) string {
	const maxLen = 400

	fields := strings.Fields(stderr)
	if len(fields) == 0 {
		return "no ffmpeg diagnostics"
	}
	s := strings.Join(fields, " ")
	if len(s) > maxLen {
		// Keep the tail: ffmpeg reports the actual failure last.
		s = "..." + s[len(s)-maxLen:]
	}
	return s
}
