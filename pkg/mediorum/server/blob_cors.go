package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"gocloud.dev/blob"
)

// A presigned redirect hands the browser to the storage backend, so the
// backend's own CORS rules decide whether a cross-origin fetch or a
// crossorigin media element can read the audio. Buckets without a CORS rule
// (B2 has none by default) answer with no Access-Control-Allow-Origin and web
// audio goes silent. Before redirecting a request that carries an Origin we
// check, once per bucket, that the backend allows it, and serve the bytes
// ourselves when it does not.

const (
	blobCORSProbeOrigin  = "https://cors-probe.openaudio.invalid"
	blobCORSProbeTimeout = 3 * time.Second
	blobCORSAllowedTTL   = time.Hour
	blobCORSDeniedTTL    = 10 * time.Minute
)

type blobCORSResult struct {
	allowed   bool
	checkedAt time.Time
}

// blobCORSCache is safe to use as a zero value.
type blobCORSCache struct {
	mu      sync.Mutex
	results map[*blob.Bucket]blobCORSResult
	client  *http.Client
	now     func() time.Time
}

func (bc *blobCORSCache) clock() time.Time {
	if bc.now != nil {
		return bc.now()
	}
	return time.Now()
}

// allows reports whether the backend behind bucket answers a cross-origin GET
// with an Access-Control-Allow-Origin that covers any origin. signedURL is a
// presigned GET for an object in that bucket and is used for the probe.
func (bc *blobCORSCache) allows(ctx context.Context, bucket *blob.Bucket, signedURL string) bool {
	bc.mu.Lock()
	res, ok := bc.results[bucket]
	bc.mu.Unlock()
	if ok {
		ttl := blobCORSDeniedTTL
		if res.allowed {
			ttl = blobCORSAllowedTTL
		}
		if bc.clock().Sub(res.checkedAt) < ttl {
			return res.allowed
		}
	}

	allowed := bc.probe(ctx, signedURL)

	bc.mu.Lock()
	if bc.results == nil {
		bc.results = make(map[*blob.Bucket]blobCORSResult)
	}
	bc.results[bucket] = blobCORSResult{allowed: allowed, checkedAt: bc.clock()}
	bc.mu.Unlock()
	return allowed
}

func (bc *blobCORSCache) probe(ctx context.Context, signedURL string) bool {
	// Detached from the request so a client hanging up does not get cached
	// as a denial.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), blobCORSProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Origin", blobCORSProbeOrigin)
	req.Header.Set("Range", "bytes=0-0")

	client := bc.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode >= 400 {
		return false
	}
	// Some backends echo the request origin instead of "*". The probe origin
	// is one nobody would list, so an echo means any origin is allowed.
	acao := resp.Header.Get("Access-Control-Allow-Origin")
	return acao == "*" || acao == blobCORSProbeOrigin
}
