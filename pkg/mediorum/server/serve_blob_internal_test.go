package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
)

func TestServeInternalBlobGETRedirectsWhenBlobStorageStreamingEnabled(t *testing.T) {
	ctx := context.Background()
	baseURL, err := url.Parse("https://signed.example.test/blob")
	require.NoError(t, err)

	bucket, err := fileblob.OpenBucket(t.TempDir(), &fileblob.Options{
		CreateDir: true,
		NoTempDir: true,
		URLSigner: fileblob.NewURLSignerHMAC(baseURL, []byte("test-secret")),
	})
	require.NoError(t, err)
	t.Cleanup(func() { bucket.Close() })

	cid := "QmInternalBlobRedirectTest"
	key := putInternalBlobTestObject(t, ctx, bucket, cid, "redirect me")
	ss := newInternalBlobTestServer(bucket, true)

	c, rec := newInternalBlobTestContext(cid)
	require.NoError(t, ss.serveInternalBlobGET(c))

	require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
	location := rec.Header().Get("Location")
	require.NotEmpty(t, location)

	signedURL, err := url.Parse(location)
	require.NoError(t, err)
	require.Equal(t, "signed.example.test", signedURL.Host)
	require.Equal(t, key, signedURL.Query().Get("obj"))
	require.Equal(t, http.MethodGet, signedURL.Query().Get("method"))
	require.NotEmpty(t, signedURL.Query().Get("signature"))
}

func TestServeInternalBlobGETFallsBackToStreamWhenSignedURLUnsupported(t *testing.T) {
	ctx := context.Background()
	bucket := openMemBucket(t)

	cid := "QmInternalBlobFallbackTest"
	body := "stream me"
	putInternalBlobTestObject(t, ctx, bucket, cid, body)
	ss := newInternalBlobTestServer(bucket, true)

	c, rec := newInternalBlobTestContext(cid)
	require.NoError(t, ss.serveInternalBlobGET(c))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, body, rec.Body.String())
}

func newInternalBlobTestServer(bucket *blob.Bucket, streaming bool) *MediorumServer {
	return &MediorumServer{
		bucket: bucket,
		logger: zap.NewNop(),
		Config: MediorumConfig{
			BlobStorageStreaming: streaming,
		},
	}
}

func newInternalBlobTestContext(cid string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/internal/blobs/"+url.PathEscape(cid), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("cid")
	c.SetParamValues(cid)
	return c, rec
}

func putInternalBlobTestObject(t *testing.T, ctx context.Context, bucket *blob.Bucket, cid string, body string) string {
	t.Helper()

	key := cidutil.ShardCID(cid)
	w, err := bucket.NewWriter(ctx, key, &blob.WriterOptions{ContentType: "application/octet-stream"})
	require.NoError(t, err)
	_, err = w.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	return key
}
