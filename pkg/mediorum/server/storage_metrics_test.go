package server

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageMetricsRingBufferOverflow(t *testing.T) {
	m := newStorageMetrics()

	// push 3x capacity worth, expect only last `cap` to remain, in append order
	total := recentServedCap * 3
	for i := 0; i < total; i++ {
		m.recordServed(ServedItem{At: time.Now(), CID: fmt.Sprintf("cid-%d", i), Action: StreamTrack})
	}

	got := m.snapshotRecentServed()
	require.Len(t, got, recentServedCap)
	// oldest in the snapshot should be index (total - cap)
	assert.Equal(t, fmt.Sprintf("cid-%d", total-recentServedCap), got[0].CID)
	assert.Equal(t, fmt.Sprintf("cid-%d", total-1), got[len(got)-1].CID)
}

func TestStorageMetricsPoSCountersAndRing(t *testing.T) {
	m := newStorageMetrics()

	for i := 0; i < 7; i++ {
		ok := i%2 == 0
		errMsg := ""
		if !ok {
			errMsg = "boom"
		}
		m.recordPoS(PoSResult{At: time.Now(), CID: fmt.Sprintf("c-%d", i), OK: ok, Error: errMsg})
	}

	assert.EqualValues(t, 7, m.posAttempted.Load())
	assert.EqualValues(t, 4, m.posPassed.Load())
	assert.EqualValues(t, 3, m.posFailed.Load())
	assert.Len(t, m.snapshotRecentPoS(), 7)
}

func TestStorageMetricsConcurrentAppend(t *testing.T) {
	m := newStorageMetrics()

	const (
		writers = 32
		perGo   = 200
	)

	var wg sync.WaitGroup
	wg.Add(writers * 2)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perGo; i++ {
				m.recordServed(ServedItem{At: time.Now(), CID: fmt.Sprintf("s-%d-%d", w, i), Action: StreamTrack})
			}
		}(w)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perGo; i++ {
				m.recordPoS(PoSResult{At: time.Now(), CID: fmt.Sprintf("p-%d-%d", w, i), OK: i%3 != 0})
			}
		}(w)
	}
	wg.Wait()

	totalPoS := int64(writers * perGo)
	assert.EqualValues(t, totalPoS, m.posAttempted.Load(),
		"posAttempted should equal total recorded PoS results")
	assert.EqualValues(t, totalPoS, m.posPassed.Load()+m.posFailed.Load(),
		"passed+failed should equal attempted")

	// Ring buffers stay at-or-below cap and never panic under concurrent writes
	assert.LessOrEqual(t, len(m.snapshotRecentServed()), recentServedCap)
	assert.LessOrEqual(t, len(m.snapshotRecentPoS()), recentPoSCap)
}

func TestByteCountingResponseWriterPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	var counter atomic.Int64
	w := &byteCountingResponseWriter{ResponseWriter: rec, counter: &counter}

	body := []byte("hello, world")
	n, err := w.Write(body)
	require.NoError(t, err)
	assert.Equal(t, len(body), n)
	assert.EqualValues(t, len(body), counter.Load())
	assert.Equal(t, string(body), rec.Body.String())
}

func TestByteCountingResponseWriterFlushForwards(t *testing.T) {
	flushable := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	var counter atomic.Int64
	w := &byteCountingResponseWriter{ResponseWriter: flushable, counter: &counter}
	w.Flush()
	assert.True(t, flushable.flushed)
}

// Hijack should forward when underlying supports it, return error otherwise.
func TestByteCountingResponseWriterHijackFallback(t *testing.T) {
	rec := httptest.NewRecorder() // doesn't implement Hijacker
	var counter atomic.Int64
	w := &byteCountingResponseWriter{ResponseWriter: rec, counter: &counter}
	_, _, err := w.Hijack()
	require.Error(t, err)
}

func TestByteCountingResponseWriterHijackForwards(t *testing.T) {
	expected := errors.New("hijacker invoked")
	hj := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder(), err: expected}
	var counter atomic.Int64
	w := &byteCountingResponseWriter{ResponseWriter: hj, counter: &counter}
	_, _, err := w.Hijack()
	assert.True(t, hj.called)
	assert.Equal(t, expected, err)
}

func TestByteCountingResponseWriterPushFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	var counter atomic.Int64
	w := &byteCountingResponseWriter{ResponseWriter: rec, counter: &counter}
	assert.Equal(t, http.ErrNotSupported, w.Push("/static/x.js", nil))
}

func TestByteCountingResponseWriterCloseNotifyAlwaysReturnsChan(t *testing.T) {
	rec := httptest.NewRecorder()
	var counter atomic.Int64
	w := &byteCountingResponseWriter{ResponseWriter: rec, counter: &counter}
	ch := w.CloseNotify()
	require.NotNil(t, ch)
	// channel should not fire on its own
	select {
	case <-ch:
		t.Fatal("CloseNotify channel fired unexpectedly")
	case <-time.After(20 * time.Millisecond):
	}
}

// helpers

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

type hijackableRecorder struct {
	*httptest.ResponseRecorder
	called bool
	err    error
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.called = true
	return nil, nil, h.err
}
