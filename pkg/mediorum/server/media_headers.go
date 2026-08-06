package server

import (
	"mime"
	"net/http"
	"strings"
)

const (
	headerContentDisposition   = "Content-Disposition"
	headerContentType          = "Content-Type"
	headerXContentTypeOptions  = "X-Content-Type-Options"
	mimeApplicationOctetStream = "application/octet-stream"
)

// safeInlineMediaTypes is intentionally limited to the non-executable media
// types that net/http.DetectContentType can assign to blobs written by
// gocloud.dev/blob. In particular, SVG and all text types must be downloaded
// rather than rendered in the content node's origin.
var safeInlineMediaTypes = map[string]struct{}{
	"application/ogg": {},
	"audio/aiff":      {},
	"audio/midi":      {},
	"audio/mpeg":      {},
	"audio/wave":      {},
	"image/bmp":       {},
	"image/gif":       {},
	"image/jpeg":      {},
	"image/png":       {},
	"image/webp":      {},
	"image/x-icon":    {},
	"video/avi":       {},
	"video/mp4":       {},
	"video/webm":      {},
}

func mediaResponseContentType(storedContentType string) (contentType string, safeInline bool) {
	mediaType, _, err := mime.ParseMediaType(storedContentType)
	if err == nil {
		mediaType = strings.ToLower(mediaType)
		if _, ok := safeInlineMediaTypes[mediaType]; ok {
			return mediaType, true
		}
	}
	return mimeApplicationOctetStream, false
}

// setMediaResponseHeaders returns the content type that should be used for a
// public blob response. Unknown or potentially executable types are forced to
// download as opaque bytes. An existing Content-Disposition (for example one
// supplied through the filename query parameter) is preserved.
func setMediaResponseHeaders(header http.Header, storedContentType string) (contentType string, safeInline bool) {
	header.Set(headerXContentTypeOptions, "nosniff")
	contentType, safeInline = mediaResponseContentType(storedContentType)

	if !safeInline && header.Get(headerContentDisposition) == "" {
		header.Set(headerContentDisposition, "attachment")
	}
	header.Set(headerContentType, contentType)
	return contentType, safeInline
}
