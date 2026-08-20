package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
)

// blobFetchChunkAttempts is per chunk, not per blob. A failure costs one chunk
// rather than restarting a multi-gigabyte transfer.
const blobFetchChunkAttempts = 3

// blobFetchChunkSize bounds how much one request may transfer, which is what
// lets an ordinary client timeout do the job. peerHTTPClient allows three
// minutes, so a chunk this size fails only below ~11 Mbit/s -- generous for a
// bucket-to-node link, and a chunk that has not finished in that window is
// stalled rather than merely large.
//
// The alternative, one request for the whole blob, is why long-form audio did
// not replicate: a 1.9GB original needed ~85 Mbit/s sustained between one
// specific pair of nodes for the entire transfer, or the client gave up.
//
// A var only so tests can shrink it; nothing reassigns it at runtime.
var blobFetchChunkSize int64 = 256 << 20

// chunkedBlobReader streams a blob from a peer as a series of ranged requests.
//
// It reads like any other body, so callers that io.Copy it into a bucket or a
// temp file need no changes. What differs is that no single request is
// unbounded, so peerHTTPClient's timeout applies to a known quantity of bytes.
type chunkedBlobReader struct {
	ctx  context.Context
	ss   *MediorumServer
	cid  string
	host string

	// origin is this peer's blob endpoint. target is where ranges are actually
	// fetched from: the same endpoint when the peer served us bytes directly, or
	// a presigned bucket URL when it redirected. signTarget tracks which, since
	// only the peer endpoint wants our signature -- a presigned URL carries its
	// own auth and Go strips ours across the redirect anyway.
	origin     string
	target     string
	signTarget bool

	total  int64
	offset int64

	cur io.ReadCloser

	// singleStream means the peer ignored Range and handed back the whole body:
	// an older node still using c.Stream. Consume it as one stream rather than
	// re-requesting ranges it will not honour.
	singleStream bool
}

func (r *chunkedBlobReader) rangeRequest(ctx context.Context, rawURL string, sign bool, start int64) (*http.Request, error) {
	var req *http.Request
	var err error
	if sign {
		req, err = signature.SignedGet(ctx, rawURL, r.ss.Config.privateKey, r.ss.Config.Self.Host)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	}
	if err != nil {
		return nil, err
	}
	end := start + blobFetchChunkSize - 1
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	return req, nil
}

// parseContentRange reads the total size out of "bytes 0-1023/4096".
func parseContentRange(v string) (int64, error) {
	slash := strings.LastIndex(v, "/")
	if slash < 0 {
		return 0, fmt.Errorf("malformed Content-Range %q", v)
	}
	sizePart := v[slash+1:]
	if sizePart == "*" {
		return 0, fmt.Errorf("Content-Range %q has unknown total size", v)
	}
	return strconv.ParseInt(sizePart, 10, 64)
}

// start issues the first ranged request against the peer and works out how the
// rest of the transfer should proceed.
//
// The Range header rides through the redirect -- Go strips only sensitive
// headers when a redirect crosses hosts -- so this one request either returns
// the first chunk from the bucket or the first chunk from the peer, and
// resp.Request.URL tells us which we got.
func (r *chunkedBlobReader) start() error {
	req, err := r.rangeRequest(r.ctx, r.origin, true, 0)
	if err != nil {
		return err
	}
	resp, err := r.ss.peerHTTPClient.Do(req)
	if err != nil {
		return err
	}

	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			resp.Body.Close()
			return err
		}
		r.total = total
		final := resp.Request.URL.String()
		r.target = final
		r.signTarget = final == r.origin
		r.cur = resp.Body
		return nil

	case http.StatusOK:
		// Range ignored: an older peer on c.Stream, or a body small enough that
		// the server chose not to partial it. Either way there is nothing to
		// chunk.
		r.singleStream = true
		r.cur = resp.Body
		return nil

	default:
		resp.Body.Close()
		return fmt.Errorf("pull blob: bad status: %d cid: %s host: %s", resp.StatusCode, r.cid, r.host)
	}
}

// nextChunk fetches the range beginning at r.offset.
func (r *chunkedBlobReader) nextChunk() error {
	var lastErr error
	for attempt := 0; attempt < blobFetchChunkAttempts; attempt++ {
		req, err := r.rangeRequest(r.ctx, r.target, r.signTarget, r.offset)
		if err != nil {
			return err
		}
		resp, err := r.ss.peerHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusPartialContent {
			r.cur = resp.Body
			return nil
		}
		resp.Body.Close()

		// A presigned URL has a finite life and a large blob can outlast it.
		// Ask the peer again and carry on from the same offset.
		if !r.signTarget && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) {
			if err := r.refreshTarget(); err != nil {
				return err
			}
			continue
		}
		lastErr = fmt.Errorf("pull blob chunk at %d: bad status: %d cid: %s host: %s",
			r.offset, resp.StatusCode, r.cid, r.host)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("pull blob chunk at %d: exhausted attempts cid: %s host: %s", r.offset, r.cid, r.host)
	}
	return lastErr
}

// refreshTarget re-resolves an expired presigned URL by asking the peer for a
// zero-length range, which costs one round trip and no payload.
func (r *chunkedBlobReader) refreshTarget() error {
	req, err := signature.SignedGet(r.ctx, r.origin, r.ss.Config.privateKey, r.ss.Config.Self.Host)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := r.ss.peerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh blob target: bad status: %d cid: %s host: %s", resp.StatusCode, r.cid, r.host)
	}
	io.Copy(io.Discard, resp.Body)
	final := resp.Request.URL.String()
	r.target = final
	r.signTarget = final == r.origin
	return nil
}

func (r *chunkedBlobReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			return 0, io.EOF
		}
		n, err := r.cur.Read(p)
		if n > 0 {
			r.offset += int64(n)
			return n, nil
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return 0, err
		}

		r.cur.Close()
		r.cur = nil
		if r.singleStream || r.offset >= r.total {
			return 0, io.EOF
		}
		if err := r.nextChunk(); err != nil {
			return 0, err
		}
	}
}

func (r *chunkedBlobReader) Close() error {
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}
