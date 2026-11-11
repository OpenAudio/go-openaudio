package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
	"gocloud.dev/blob/memblob"
)

// A legacy-shaped cid: Qm prefix, 46 chars.
const testLegacyCID = "QmXt9U9N2BpM4jUjHxCDKG2RSXoyTjpoRDFvj8YJGCicTr"

// failingReader yields n bytes, then errors the way a dropped peer connection does.
type failingReader struct {
	n int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("connection reset by peer")
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	for i := range p {
		p[i] = 'x'
	}
	f.n -= len(p)
	return len(p), nil
}

// A copy that fails partway must leave nothing under the key. Closing the
// writer after the failure used to commit the partial bytes.
func TestCopyToBucketDiscardsFailedWrite(t *testing.T) {
	buckets := map[string]func(t *testing.T) *blob.Bucket{
		"memblob": func(t *testing.T) *blob.Bucket { return memblob.OpenBucket(nil) },
		"fileblob": func(t *testing.T) *blob.Bucket {
			b, err := fileblob.OpenBucket(t.TempDir(), nil)
			require.NoError(t, err)
			return b
		},
	}
	for name, open := range buckets {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			bucket := open(t)
			defer bucket.Close()

			key := cidutil.ShardCID(testLegacyCID)
			_, err := copyToBucket(ctx, bucket, key, &failingReader{n: 100_000})
			require.Error(t, err)

			exists, err := bucket.Exists(ctx, key)
			require.NoError(t, err)
			require.False(t, exists, "a failed copy committed a partial blob")

			n, err := copyToBucket(ctx, bucket, key, strings.NewReader("complete"))
			require.NoError(t, err)
			require.EqualValues(t, len("complete"), n)
			got, err := bucket.ReadAll(ctx, key)
			require.NoError(t, err)
			require.Equal(t, "complete", string(got))
		})
	}
}

// legacyPeer serves a blob the way an older peer on c.Stream does: 200, no
// Content-Length, and a body cut short of the size its info endpoint reports.
func legacyPeer(t *testing.T, cid string, body []byte, reportedSize int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/blobs/info/" + cid:
			json.NewEncoder(w).Encode(map[string]any{"Size": reportedSize})
		case "/internal/blobs/" + cid:
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush() // forces chunked encoding: no Content-Length
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
}

func legacyPullServer(t *testing.T) *MediorumServer {
	t.Helper()
	ss := blobFetchTestServer(t)
	ss.reqClient = req.C()
	ss.bucket = memblob.OpenBucket(nil)
	t.Cleanup(func() { ss.bucket.Close() })
	return ss
}

// The hoodinternet case: a peer holding 6% of a legacy file serves it as if it
// were whole. The pull must fail and store nothing, since a Qm cid offers no
// hash to catch it later.
func TestPullFileFromHostRejectsTruncatedLegacyBlob(t *testing.T) {
	ss := legacyPullServer(t)
	full := bytes.Repeat([]byte("audio"), 2000)
	peer := legacyPeer(t, testLegacyCID, full[:600], int64(len(full)))
	defer peer.Close()

	err := ss.pullFileFromHost(context.Background(), peer.URL, testLegacyCID, nil)
	require.ErrorIs(t, err, errPulledBlobSizeMismatch)

	exists, err := ss.bucket.Exists(context.Background(), cidutil.ShardCID(testLegacyCID))
	require.NoError(t, err)
	require.False(t, exists, "truncated legacy blob was stored")
}

func TestOpenBlobFromHostAcceptsCompleteLegacyBlob(t *testing.T) {
	ss := legacyPullServer(t)
	full := bytes.Repeat([]byte("audio"), 2000)
	peer := legacyPeer(t, testLegacyCID, full, int64(len(full)))
	defer peer.Close()

	r, err := ss.openBlobFromHost(context.Background(), peer.URL, testLegacyCID)
	require.NoError(t, err)
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.True(t, bytes.Equal(full, got))
}

// Without a size there is nothing to check a legacy pull against, so refuse it
// rather than store whatever arrives.
func TestOpenBlobFromHostRefusesLegacyBlobOfUnknownSize(t *testing.T) {
	ss := legacyPullServer(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/blobs/info/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		w.Write([]byte("some bytes"))
	}))
	defer peer.Close()

	_, err := ss.openBlobFromHost(context.Background(), peer.URL, testLegacyCID)
	require.Error(t, err)
}

// The ranged path reports its size in Content-Range; a peer that serves more
// than it claimed is as wrong as one that serves less.
func TestChunkedBlobReaderRejectsBodyLongerThanContentRange(t *testing.T) {
	ss := blobFetchTestServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-9/10")
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		w.Write(bytes.Repeat([]byte("y"), 25))
	}))
	defer srv.Close()

	_, err := readAllFrom(t, ss, srv.URL)
	require.ErrorIs(t, err, errPulledBlobSizeMismatch)
}
