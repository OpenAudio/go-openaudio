package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/registrar"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// blobFetchTestServer builds the minimum a chunkedBlobReader touches: a signing
// key, a self host, and the peer client whose timeout bounds each chunk.
func blobFetchTestServer(t *testing.T) *MediorumServer {
	t.Helper()
	return &MediorumServer{
		Config: MediorumConfig{
			Self:       registrar.Peer{Host: "http://self.test"},
			privateKey: generateTestPrivateKey(1),
		},
		peerHTTPClient: &http.Client{Timeout: 10 * time.Second},
		logger:         zap.NewNop(),
	}
}

// rangeServer serves body honouring Range, counting requests.
func rangeServer(body []byte, requests *atomic.Int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		http.ServeContent(w, r, "blob", time.Unix(0, 0), bytes.NewReader(body))
	}))
}

func readAllFrom(t *testing.T, ss *MediorumServer, origin string) ([]byte, error) {
	t.Helper()
	r := &chunkedBlobReader{ctx: context.Background(), ss: ss, cid: "testcid", host: "peer", origin: origin}
	if err := r.start(); err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// The point of chunking: no single request covers the whole blob, so an
// ordinary client timeout bounds a known quantity of bytes.
func TestChunkedBlobReaderFetchesInRanges(t *testing.T) {
	ss := blobFetchTestServer(t)

	// Three chunks and a remainder, without allocating 256MB per chunk.
	body := bytes.Repeat([]byte("mediorum"), 4096)
	var requests atomic.Int64
	srv := rangeServer(body, &requests)
	defer srv.Close()

	withChunkSize(t, len(body)/3)

	got, err := readAllFrom(t, ss, srv.URL)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got), "assembled blob differs from the source")
	require.Greater(t, requests.Load(), int64(1), "fetched in a single request; chunking did not happen")
}

// Every chunk after the first is a fresh request, so each must carry the peer
// signature -- the whole transfer is unauthenticated past byte one otherwise.
// This is the file:// path: no redirect, so the target stays the peer itself and
// signTarget must stay true. A test server that ignores Authorization would pass
// either way, so this one rejects unsigned requests.
func TestChunkedBlobReaderSignsEveryChunkAgainstThePeer(t *testing.T) {
	ss := blobFetchTestServer(t)
	body := bytes.Repeat([]byte("signed"), 4096)

	var signed, unsigned atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			unsigned.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		signed.Add(1)
		http.ServeContent(w, r, "blob", time.Unix(0, 0), bytes.NewReader(body))
	}))
	defer srv.Close()

	withChunkSize(t, len(body)/3)

	got, err := readAllFrom(t, ss, srv.URL)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got))
	require.Zero(t, unsigned.Load(), "a chunk was fetched without the peer signature")
	require.Greater(t, signed.Load(), int64(1), "only one request; the multi-chunk path did not run")
}

// An older peer still on c.Stream ignores Range and returns the whole body.
// The reader must consume that rather than re-requesting ranges it won't honour.
func TestChunkedBlobReaderFallsBackWhenRangeIgnored(t *testing.T) {
	ss := blobFetchTestServer(t)
	body := bytes.Repeat([]byte("x"), 5000)

	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	withChunkSize(t, 1000)

	got, err := readAllFrom(t, ss, srv.URL)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got))
	require.EqualValues(t, 1, requests.Load(), "kept issuing ranges to a peer that ignores them")
}

// A presigned URL has a finite life and a large blob can outlast it. Expiry
// mid-transfer must re-resolve and carry on from the same offset, not restart.
func TestChunkedBlobReaderRefreshesExpiredPresignedURL(t *testing.T) {
	ss := blobFetchTestServer(t)
	body := bytes.Repeat([]byte("abcdefgh"), 2048)

	var bucketHits atomic.Int64
	var expired atomic.Bool
	expired.Store(true)

	bucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail exactly once, partway in, the way a lapsed signature would.
		if bucketHits.Add(1) == 2 && expired.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "blob", time.Unix(0, 0), bytes.NewReader(body))
	}))
	defer bucket.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, bucket.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	withChunkSize(t, len(body)/4)

	got, err := readAllFrom(t, ss, origin.URL)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got), "resumed transfer does not match the source")
	require.False(t, expired.Load(), "the expiry branch never ran; the test proved nothing")
}

// A chunk failure costs one chunk, not the whole blob.
func TestChunkedBlobReaderRetriesOneChunk(t *testing.T) {
	ss := blobFetchTestServer(t)
	body := bytes.Repeat([]byte("0123456789"), 1024)

	var failed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail the first non-initial range once.
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=") &&
			!strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") &&
			failed.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, "blob", time.Unix(0, 0), bytes.NewReader(body))
	}))
	defer srv.Close()

	withChunkSize(t, len(body)/3)

	got, err := readAllFrom(t, ss, srv.URL)
	require.NoError(t, err)
	require.True(t, bytes.Equal(body, got))
	require.True(t, failed.Load(), "the retry branch never ran; the test proved nothing")
}

func TestChunkedBlobReaderSurfacesBadStatus(t *testing.T) {
	ss := blobFetchTestServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := readAllFrom(t, ss, srv.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprint(http.StatusNotFound))
}

func TestParseContentRange(t *testing.T) {
	total, err := parseContentRange("bytes 0-1023/4096")
	require.NoError(t, err)
	require.EqualValues(t, 4096, total)

	_, err = parseContentRange("bytes 0-1023/*")
	require.Error(t, err, "unknown total size must not be read as a length")

	_, err = parseContentRange("nonsense")
	require.Error(t, err)
}

// withChunkSize shrinks the chunk for the duration of a test so a few kilobytes
// exercise the same multi-request path a multi-gigabyte blob would.
func withChunkSize(t *testing.T, n int) {
	t.Helper()
	original := blobFetchChunkSize
	blobFetchChunkSize = int64(n)
	t.Cleanup(func() { blobFetchChunkSize = original })
}
