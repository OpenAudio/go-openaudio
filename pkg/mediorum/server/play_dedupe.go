package server

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	playDedupeDefaultWindow = 5 * time.Minute
	playDedupeMinWindow     = 2 * time.Minute
	playDedupeMaxWindow     = 15 * time.Minute

	playDedupePruneInterval = time.Minute
	// Past this many live keys the map is dropped and plays count again,
	// so a flood of distinct IPs can't grow memory without bound.
	playDedupeMaxEntries = 500_000
)

// playDedupe counts at most one play per (client IP, track) per window.
// Every /v1/tracks/:id/stream call mints a fresh signature, so without this
// an audio element re-requesting the file, a crawler, or a server-side
// fetcher logs a play on every GET. The zero value is ready to use.
type playDedupe struct {
	mu        sync.Mutex
	expiry    map[string]time.Time
	lastPrune time.Time
}

// allow reports whether a play of trackID from ip should be counted at now,
// and if so suppresses further plays of that pair until now+window.
func (d *playDedupe) allow(ip, trackID string, window time.Duration, now time.Time) bool {
	key := ip + "|" + trackID

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.expiry == nil {
		d.expiry = make(map[string]time.Time)
	}
	if now.Sub(d.lastPrune) >= playDedupePruneInterval {
		d.prune(now)
	}

	if exp, ok := d.expiry[key]; ok && now.Before(exp) {
		return false
	}
	if len(d.expiry) >= playDedupeMaxEntries {
		d.expiry = make(map[string]time.Time)
	}
	d.expiry[key] = now.Add(window)
	return true
}

func (d *playDedupe) prune(now time.Time) {
	for k, exp := range d.expiry {
		if !now.Before(exp) {
			delete(d.expiry, k)
		}
	}
	d.lastPrune = now
}

// playDedupeWindow is the track's duration, clamped so short tracks still
// absorb a burst of re-requests and long mixes don't hide a real replay
// for hours. Unknown duration gets the default.
func playDedupeWindow(durationSeconds float64) time.Duration {
	if durationSeconds <= 0 {
		return playDedupeDefaultWindow
	}
	w := time.Duration(durationSeconds * float64(time.Second))
	return min(max(w, playDedupeMinWindow), playDedupeMaxWindow)
}

// rangeStartsPlay reports whether a request with this Range header begins a
// listen: no Range, or a range starting at byte 0. Seeks and tail reads
// (bytes=N-, bytes=-N) continue a listen that was already counted, and a
// malformed Range gets a 416 rather than audio.
func rangeStartsPlay(header string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return true
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(spec, ",")
	start, _, ok := strings.Cut(strings.TrimSpace(first), "-")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	return err == nil && n == 0
}

// Crawlers and link-preview fetchers that pull audio but aren't listeners.
// Matched as lowercase substrings of the User-Agent.
var nonListenerUserAgents = []string{
	"googlebot",
	"bingbot",
	"applebot",
	"yandexbot",
	"baiduspider",
	"duckduckbot",
	"bytespider",
	"ahrefsbot",
	"semrushbot",
	"petalbot",
	"gptbot",
	"claudebot",
	"facebookexternalhit",
	"twitterbot",
	"slackbot",
	"discordbot",
	"telegrambot",
	"linkedinbot",
	"embedly",
}

func isNonListenerUserAgent(ua string) bool {
	ua = strings.ToLower(ua)
	for _, token := range nonListenerUserAgents {
		if strings.Contains(ua, token) {
			return true
		}
	}
	return false
}

// shouldCountPlay reports whether a cidstream GET is a new listen of trackID
// rather than a seek, a crawler, or a repeat from the same client.
func (ss *MediorumServer) shouldCountPlay(c echo.Context, trackID string) bool {
	req := c.Request()
	if !rangeStartsPlay(req.Header.Get("Range")) || isNonListenerUserAgent(req.UserAgent()) {
		return false
	}
	durationSeconds, _ := c.Get("trackDurationSeconds").(float64)
	return ss.playDedupe.allow(c.RealIP(), trackID, playDedupeWindow(durationSeconds), time.Now())
}
