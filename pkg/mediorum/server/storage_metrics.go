package server

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
)

// Process-local counters and ring buffers backing the Storage console page.
// Reset on restart.
type storageMetrics struct {
	// serve-blob path
	readLocalHits     atomic.Int64
	readMisses        atomic.Int64
	readPullAttempts  atomic.Int64
	readPullSuccesses atomic.Int64
	readProxied       atomic.Int64
	readRedirected    atomic.Int64

	// serve-waveform path. Kept apart from the blob counters because a waveform
	// miss means something different: the blob may well be here and simply not
	// analyzed yet. Redirects are also the only measure of how evenly waveforms
	// are spread across the network, since they are computed per node rather
	// than replicated.
	waveformServed     atomic.Int64
	waveformMisses     atomic.Int64
	waveformRedirected atomic.Int64

	// proof of storage
	posAttempted atomic.Int64
	posPassed    atomic.Int64
	posFailed    atomic.Int64

	// http bandwidth
	bytesIngress atomic.Int64
	bytesEgress  atomic.Int64

	mu           sync.Mutex
	recentPoS    []PoSResult
	recentServed []ServedItem
}

type PoSResult struct {
	At    time.Time
	CID   string
	OK    bool
	Error string
}

type ServedItem struct {
	At     time.Time
	CID    string
	Action string
}

const (
	recentPoSCap    = 50
	recentServedCap = 200
)

func newStorageMetrics() *storageMetrics {
	return &storageMetrics{
		recentPoS:    make([]PoSResult, 0, recentPoSCap),
		recentServed: make([]ServedItem, 0, recentServedCap),
	}
}

func (m *storageMetrics) recordPoS(r PoSResult) {
	m.posAttempted.Add(1)
	if r.OK {
		m.posPassed.Add(1)
	} else {
		m.posFailed.Add(1)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recentPoS) >= recentPoSCap {
		m.recentPoS = m.recentPoS[1:]
	}
	m.recentPoS = append(m.recentPoS, r)
}

func (m *storageMetrics) recordServed(s ServedItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recentServed) >= recentServedCap {
		m.recentServed = m.recentServed[1:]
	}
	m.recentServed = append(m.recentServed, s)
}

func (m *storageMetrics) snapshotRecentPoS() []PoSResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PoSResult, len(m.recentPoS))
	copy(out, m.recentPoS)
	return out
}

func (m *storageMetrics) snapshotRecentServed() []ServedItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServedItem, len(m.recentServed))
	copy(out, m.recentServed)
	return out
}

// echo middleware that totals request body + response body sizes.
func (ss *MediorumServer) bandwidthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if cl := c.Request().ContentLength; cl > 0 {
			ss.metrics.bytesIngress.Add(cl)
		}
		c.Response().Writer = &byteCountingResponseWriter{
			ResponseWriter: c.Response().Writer,
			counter:        &ss.metrics.bytesEgress,
		}
		return next(c)
	}
}

type byteCountingResponseWriter struct {
	http.ResponseWriter
	counter *atomic.Int64
}

func (w *byteCountingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if n > 0 {
		w.counter.Add(int64(n))
	}
	return n, err
}

// Forward optional interfaces to the underlying writer so handlers that
// downcast (e.g. http.ServeContent for streaming, reverse proxies for
// upgrades) keep working through our wrapper.

func (w *byteCountingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *byteCountingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support Hijack")
}

func (w *byteCountingResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (w *byteCountingResponseWriter) CloseNotify() <-chan bool {
	if c, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return c.CloseNotify()
	}
	// Return a never-firing channel rather than nil so callers select-on-it safely.
	ch := make(chan bool, 1)
	return ch
}
