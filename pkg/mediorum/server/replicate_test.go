package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func countReplicateFileToHostGoroutines() int {
	var stacks bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
	return strings.Count(stacks.String(), "replicateFileToHost.func1")
}

func TestReplicateFileToHostClosesPipeOnEarlyHTTPError(t *testing.T) {
	ss := testNetwork[0]
	originalClient := ss.peerHTTPClient
	ss.peerHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Status:     "500 Internal Server Error",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
	}
	t.Cleanup(func() {
		ss.peerHTTPClient = originalClient
	})

	before := countReplicateFileToHostGoroutines()
	err := ss.replicateFileToHost(
		context.Background(),
		"http://unread-peer.test",
		"leak-regression-cid",
		strings.NewReader(strings.Repeat("x", 1024*1024)),
		nil,
	)
	assert.Error(t, err)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if countReplicateFileToHostGoroutines() <= before {
			return
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, countReplicateFileToHostGoroutines(), before)
}
