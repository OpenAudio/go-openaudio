package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gocloud.dev/blob"
)

// fakeBackend stands in for a storage backend answering a presigned GET.
func fakeBackend(t *testing.T, status int, acao func(origin string) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if v := acao(r.Header.Get("Origin")); v != "" {
			w.Header().Set("Access-Control-Allow-Origin", v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("x"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestBlobCORSProbe(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name   string
		status int
		acao   func(string) string
		want   bool
	}{
		{"wildcard", http.StatusPartialContent, func(string) string { return "*" }, true},
		{"echoes origin", http.StatusPartialContent, func(o string) string { return o }, true},
		{"no cors rule (b2 default)", http.StatusPartialContent, func(string) string { return "" }, false},
		{"single listed origin", http.StatusPartialContent, func(string) string { return "https://audius.co" }, false},
		{"error status", http.StatusForbidden, func(string) string { return "*" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeBackend(t, tc.status, tc.acao)
			var bc blobCORSCache
			assert.Equal(t, tc.want, bc.allows(ctx, new(blob.Bucket), srv.URL+"/obj?X-Amz-Signature=abc"))
		})
	}

	t.Run("unreachable backend is not allowed", func(t *testing.T) {
		var bc blobCORSCache
		assert.False(t, bc.allows(ctx, new(blob.Bucket), "http://127.0.0.1:1/obj"))
	})
}

func TestBlobCORSCachePerBucketAndTTL(t *testing.T) {
	ctx := context.Background()
	allowSrv, allowHits := fakeBackend(t, http.StatusPartialContent, func(string) string { return "*" })
	denySrv, denyHits := fakeBackend(t, http.StatusPartialContent, func(string) string { return "" })

	now := time.Unix(1_700_000_000, 0)
	bc := blobCORSCache{now: func() time.Time { return now }}
	primary, archive := new(blob.Bucket), new(blob.Bucket)

	// Each bucket is probed once and then answered from cache.
	for i := 0; i < 3; i++ {
		assert.True(t, bc.allows(ctx, primary, allowSrv.URL))
		assert.False(t, bc.allows(ctx, archive, denySrv.URL))
	}
	assert.EqualValues(t, 1, allowHits.Load())
	assert.EqualValues(t, 1, denyHits.Load())

	// A denial is rechecked sooner, so an operator who adds a rule is picked up.
	now = now.Add(blobCORSDeniedTTL + time.Second)
	bc.allows(ctx, primary, allowSrv.URL)
	bc.allows(ctx, archive, denySrv.URL)
	assert.EqualValues(t, 1, allowHits.Load())
	assert.EqualValues(t, 2, denyHits.Load())

	now = now.Add(blobCORSAllowedTTL)
	bc.allows(ctx, primary, allowSrv.URL)
	assert.EqualValues(t, 2, allowHits.Load())
}

func TestBlobCORSProbeIgnoresCancelledRequest(t *testing.T) {
	srv, _ := fakeBackend(t, http.StatusPartialContent, func(string) string { return "*" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var bc blobCORSCache
	assert.True(t, bc.allows(ctx, new(blob.Bucket), srv.URL))
}
