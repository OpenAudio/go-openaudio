package server

import (
	"sync"
	"time"
)

// logThrottle rate-limits repeated log lines from hot paths (e.g. the per-CID
// disk-space check in the repair sweep, which emitted ~2,500 identical warns
// per second during the July 2026 outage). The zero value is ready to use.
// It gates logging decisions only — callers must never let it influence
// behavior.
type logThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allow reports whether the log line identified by key may be emitted now,
// permitting at most one emission per interval per key.
func (t *logThrottle) allow(key string, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if last, ok := t.last[key]; ok && now.Sub(last) < interval {
		return false
	}
	if t.last == nil {
		t.last = make(map[string]time.Time)
	}
	t.last[key] = now
	return true
}
