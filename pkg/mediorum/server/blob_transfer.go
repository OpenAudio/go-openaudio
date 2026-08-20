package server

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Blob payload transfer is bounded by progress rather than by total duration.
// A single whole-request timeout cannot tell a long transfer from a stalled
// one: at three minutes it rejects any blob that cannot cross the wire at
// several MB/s, which for a three-hour mix means the transcode needs ~19Mbit/s
// and a wav original ~85Mbit/s sustained between one specific pair of nodes.
// Those transfers do not fail slowly -- they never complete, and the sweep
// re-sends the same bytes on every cycle.
//
// Vars rather than consts so tests can shrink them; nothing else rebinds them.
var (
	// No bytes at all for this long means the transfer is wedged, not slow.
	// Generous enough to cover a cold read on the peer's bucket before the
	// first byte arrives.
	blobTransferStallTimeout = 60 * time.Second

	// Backstop for a transfer that keeps trickling but will never finish, so
	// it cannot hold a replication worker indefinitely.
	blobTransferMaxDuration = 60 * time.Minute

	// requestPeerPull is answered only after the peer has run the entire
	// transfer on our behalf, so there is no progress to watch from this side
	// -- just a ceiling, and it has to outlast the peer's own.
	peerPullMaxDuration = blobTransferMaxDuration + 5*time.Minute
)

// transferGuard bounds an in-flight transfer by progress. Its context is
// cancelled when no bytes have moved for stallTimeout, or when maxDuration
// elapses regardless of progress.
//
// The guard is started before the request rather than at the first byte, so a
// peer that accepts the connection and then goes quiet is cut loose within the
// stall window instead of holding a worker until the ceiling.
type transferGuard struct {
	ctx    context.Context
	cancel context.CancelFunc

	lastProgress atomic.Int64
	stopWatch    chan struct{}
	stopOnce     sync.Once
}

func newTransferGuard(parent context.Context, stallTimeout, maxDuration time.Duration) *transferGuard {
	ctx, cancel := context.WithTimeout(parent, maxDuration)
	g := &transferGuard{
		ctx:       ctx,
		cancel:    cancel,
		stopWatch: make(chan struct{}),
	}
	g.lastProgress.Store(time.Now().UnixNano())

	go func() {
		ticker := time.NewTicker(stallTimeout / 4)
		defer ticker.Stop()
		for {
			select {
			case <-g.stopWatch:
				return
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, g.lastProgress.Load())) >= stallTimeout {
					cancel()
					return
				}
			}
		}
	}()

	return g
}

// progress records that bytes moved.
func (g *transferGuard) progress() {
	g.lastProgress.Store(time.Now().UnixNano())
}

// settle ends stall detection but leaves the ceiling in place. Callers use it
// once the payload has finished moving and the peer is doing its own work
// before it answers: committing a multi-gigabyte blob to a bucket can outlast
// the stall window, and that silence is expected rather than a wedged peer.
func (g *transferGuard) settle() {
	g.stopOnce.Do(func() { close(g.stopWatch) })
}

// release ends the transfer and frees the context.
func (g *transferGuard) release() {
	g.settle()
	g.cancel()
}

// reader wraps r so that reads report progress.
func (g *transferGuard) reader(r io.Reader) io.Reader {
	return &progressReader{r: r, guard: g}
}

// writer wraps w so that writes report progress. For a request body written
// into an io.Pipe, a write completes only once the transport has taken the
// bytes, so this measures the network rather than the local source.
func (g *transferGuard) writer(w io.Writer) io.Writer {
	return &progressWriter{w: w, guard: g}
}

type progressReader struct {
	r     io.Reader
	guard *transferGuard
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.guard.progress()
	}
	return n, err
}

type progressWriter struct {
	w     io.Writer
	guard *transferGuard
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	if n > 0 {
		p.guard.progress()
	}
	return n, err
}

// guardedBody ties a guard's lifetime to the response body it protects, so the
// bound stays in force for as long as the caller is still reading.
type guardedBody struct {
	io.Reader
	body  io.Closer
	guard *transferGuard
}

func (g *guardedBody) Close() error {
	g.guard.release()
	return g.body.Close()
}
