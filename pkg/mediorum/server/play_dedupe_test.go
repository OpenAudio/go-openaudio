package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestPlayDedupeOnePlayPerIPAndTrackPerWindow(t *testing.T) {
	var d playDedupe
	now := time.Now()

	assert.True(t, d.allow("1.2.3.4", "10", 5*time.Minute, now))
	assert.False(t, d.allow("1.2.3.4", "10", 5*time.Minute, now.Add(time.Second)))
	assert.False(t, d.allow("1.2.3.4", "10", 5*time.Minute, now.Add(4*time.Minute)))
	assert.True(t, d.allow("1.2.3.4", "10", 5*time.Minute, now.Add(5*time.Minute)))
}

func TestPlayDedupeKeysAreIndependent(t *testing.T) {
	var d playDedupe
	now := time.Now()

	assert.True(t, d.allow("1.2.3.4", "10", time.Minute, now))
	assert.True(t, d.allow("1.2.3.4", "11", time.Minute, now))
	assert.True(t, d.allow("5.6.7.8", "10", time.Minute, now))
	assert.False(t, d.allow("1.2.3.4", "11", time.Minute, now))
	assert.False(t, d.allow("5.6.7.8", "10", time.Minute, now))
}

func TestPlayDedupePrunesExpiredKeys(t *testing.T) {
	var d playDedupe
	now := time.Now()

	d.allow("1.2.3.4", "10", time.Minute, now)
	d.allow("1.2.3.4", "11", 10*time.Minute, now)
	assert.Len(t, d.expiry, 2)

	d.allow("5.6.7.8", "12", time.Minute, now.Add(2*time.Minute))
	assert.Len(t, d.expiry, 2)
	assert.NotContains(t, d.expiry, "1.2.3.4|10")
	assert.Contains(t, d.expiry, "1.2.3.4|11")
}

func TestPlayDedupeWindow(t *testing.T) {
	assert.Equal(t, playDedupeDefaultWindow, playDedupeWindow(0))
	assert.Equal(t, playDedupeMinWindow, playDedupeWindow(30))
	assert.Equal(t, 210*time.Second, playDedupeWindow(210))
	assert.Equal(t, playDedupeMaxWindow, playDedupeWindow(3*60*60))
}

func TestRangeStartsPlay(t *testing.T) {
	cases := map[string]bool{
		"":                  true,
		"bytes=0-":          true,
		"bytes=0-1":         true,
		" bytes=0-1023 ":    true,
		"bytes=0-99,200-":   true,
		"bytes=1-":          false,
		"bytes=1048576-":    false,
		"bytes=-500":        false,
		"bytes=500-999,0-1": false,
		"bytes=abc-":        false,
		"bytes=0":           false,
		"items=0-10":        false,
	}
	for header, want := range cases {
		assert.Equal(t, want, rangeStartsPlay(header), "Range %q", header)
	}
}

func TestIsNonListenerUserAgent(t *testing.T) {
	assert.True(t, isNonListenerUserAgent("Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"))
	assert.True(t, isNonListenerUserAgent("facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)"))
	assert.True(t, isNonListenerUserAgent("Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)"))

	assert.False(t, isNonListenerUserAgent(""))
	assert.False(t, isNonListenerUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"))
	assert.False(t, isNonListenerUserAgent("Mozilla/5.0 (Linux; Android 10; Cubot X30) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36"))
	assert.False(t, isNonListenerUserAgent("AppleCoreMedia/1.0.0.21A329 (iPhone; U; CPU OS 17_0 like Mac OS X; en_us)"))
}

func newPlayTestContext(e *echo.Echo, ip, rangeHeader, userAgent string, durationSeconds float64) echo.Context {
	req := httptest.NewRequest(http.MethodGet, "/tracks/cidstream/QmTrack?signature=x", nil)
	req.RemoteAddr = ip + ":5555"
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	c := e.NewContext(req, httptest.NewRecorder())
	if durationSeconds > 0 {
		c.Set("trackDurationSeconds", durationSeconds)
	}
	return c
}

func TestShouldCountPlayBrowserSession(t *testing.T) {
	e := echo.New()
	ss := &MediorumServer{}
	browser := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0 Safari/537.36"

	// An audio element's initial request counts; its seeks, a re-buffer from
	// byte 0, and a fresh /stream signature for the same track do not.
	assert.True(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "bytes=0-", browser, 200), "10"))
	assert.False(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "bytes=2000000-", browser, 200), "10"))
	assert.False(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "bytes=0-", browser, 200), "10"))
	assert.False(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "", browser, 200), "10"))

	// Another listener, or another track, still counts.
	assert.True(t, ss.shouldCountPlay(newPlayTestContext(e, "5.6.7.8", "bytes=0-", browser, 200), "10"))
	assert.True(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "bytes=0-", browser, 200), "11"))
}

func TestShouldCountPlaySkipsWithoutConsumingWindow(t *testing.T) {
	e := echo.New()
	ss := &MediorumServer{}

	// A seek or a crawler from an IP must not use up that IP's play.
	assert.False(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "bytes=100-", "", 0), "10"))
	assert.False(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "", "Googlebot/2.1", 0), "10"))
	assert.True(t, ss.shouldCountPlay(newPlayTestContext(e, "1.2.3.4", "", "", 0), "10"))
}

func TestShouldCountPlayUsesForwardedClientIP(t *testing.T) {
	e := echo.New()
	ss := &MediorumServer{}

	// Behind a proxy every request shares RemoteAddr; the dedupe key must be
	// the client IP from X-Forwarded-For, the same one geo lookup uses.
	a := newPlayTestContext(e, "10.0.0.1", "", "", 0)
	a.Request().Header.Set("X-Forwarded-For", "1.2.3.4")
	b := newPlayTestContext(e, "10.0.0.1", "", "", 0)
	b.Request().Header.Set("X-Forwarded-For", "5.6.7.8")

	assert.True(t, ss.shouldCountPlay(a, "10"))
	assert.True(t, ss.shouldCountPlay(b, "10"))
}
