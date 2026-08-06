package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestSetMediaResponseHeaders(t *testing.T) {
	tests := []struct {
		name                string
		storedContentType   string
		existingDisposition string
		wantContentType     string
		wantDisposition     string
		wantSafeInline      bool
	}{
		{
			name:              "jpeg is served inline",
			storedContentType: "image/jpeg",
			wantContentType:   "image/jpeg",
			wantSafeInline:    true,
		},
		{
			name:              "mp3 parameters are stripped",
			storedContentType: "audio/mpeg; charset=binary",
			wantContentType:   "audio/mpeg",
			wantSafeInline:    true,
		},
		{
			name:              "html is downloaded as opaque bytes",
			storedContentType: "text/html; charset=utf-8",
			wantContentType:   "application/octet-stream",
			wantDisposition:   "attachment",
		},
		{
			name:              "svg is not a safe image type",
			storedContentType: "image/svg+xml",
			wantContentType:   "application/octet-stream",
			wantDisposition:   "attachment",
		},
		{
			name:                "requested download filename is preserved",
			storedContentType:   "text/html",
			existingDisposition: `attachment; filename="track.mp3"`,
			wantContentType:     "application/octet-stream",
			wantDisposition:     `attachment; filename="track.mp3"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header)
			if test.existingDisposition != "" {
				header.Set(echo.HeaderContentDisposition, test.existingDisposition)
			}

			contentType, safeInline := setMediaResponseHeaders(header, test.storedContentType)

			require.Equal(t, test.wantContentType, contentType)
			require.Equal(t, test.wantSafeInline, safeInline)
			require.Equal(t, test.wantContentType, header.Get(echo.HeaderContentType))
			require.Equal(t, test.wantDisposition, header.Get(echo.HeaderContentDisposition))
			require.Equal(t, "nosniff", header.Get(headerXContentTypeOptions))
		})
	}
}

func TestMediaResponseHeadersPreventHTMLPolyglotRendering(t *testing.T) {
	body := append(
		[]byte(`<html><body><script>alert(document.domain)</script></body></html>`),
		[]byte("ID3\x04\x00\x00\x00\x00\x00\x22")...,
	)
	storedContentType := http.DetectContentType(body)
	require.Equal(t, "text/html; charset=utf-8", storedContentType)

	recorder := httptest.NewRecorder()
	setMediaResponseHeaders(recorder.Header(), storedContentType)
	http.ServeContent(
		recorder,
		httptest.NewRequest(http.MethodGet, "/content/test-cid", nil),
		"test-cid",
		time.Time{},
		bytes.NewReader(body),
	)

	result := recorder.Result()
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.Equal(t, mimeApplicationOctetStream, result.Header.Get(echo.HeaderContentType))
	require.Equal(t, "attachment", result.Header.Get(echo.HeaderContentDisposition))
	require.Equal(t, "nosniff", result.Header.Get(headerXContentTypeOptions))
}
