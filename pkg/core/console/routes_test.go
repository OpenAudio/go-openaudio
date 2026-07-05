package console

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestConsoleHistoricalPageLimiter(t *testing.T) {
	e := echo.New()
	limited := consoleHistoricalPageLimiter()(func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/console/tx/abc", nil)
		req.RemoteAddr = "203.0.113.10:1234"

		if err := limited(e.NewContext(req, rec)); err != nil {
			t.Fatalf("request %d returned error: %v", i+1, err)
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, want %d", i+1, rec.Code, http.StatusNoContent)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/console/tx/abc", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	if err := limited(e.NewContext(req, rec)); err != nil {
		t.Fatalf("limited request returned error: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("limited request status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestConsoleNoIndexMiddleware(t *testing.T) {
	e := echo.New()
	handler := consoleNoIndexMiddleware(func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/console/overview", nil)

	if err := handler(e.NewContext(req, rec)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("X-Robots-Tag = %q, want %q", got, "noindex, nofollow")
	}
}
