package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/stretchr/testify/require"
)

func proxyResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCometProxyCacheCoalescesAndPreservesIDs(t *testing.T) {
	var cache cometProxyCache
	var calls atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := rpctypes.NewRPCRequest(rpctypes.JSONRPCStringID(fmt.Sprint(i)), "status", nil)
			resp, err := cache.do("POST:{}", req, func() (*http.Response, error) {
				calls.Add(1)
				body, _ := json.Marshal(rpctypes.NewRPCSuccessResponse(req.ID, json.RawMessage(`{"height":"42"}`)))
				return proxyResponse(200, string(body)), nil
			})
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			var result rpctypes.RPCResponse
			if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
				errs <- err
				return
			}
			if result.ID != req.ID || string(result.Result) != `{"height":"42"}` {
				errs <- fmt.Errorf("incorrect response: %s", result.ID)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, calls.Load())
}

func TestCometProxyCacheKeysAndExpiry(t *testing.T) {
	var cache cometProxyCache
	calls := 0
	req := rpctypes.NewRPCRequest(rpctypes.JSONRPCIntID(1), "validators", nil)
	fetch := func() (*http.Response, error) {
		calls++
		return proxyResponse(200, `{"jsonrpc":"2.0","id":1,"result":{"validators":[]}}`), nil
	}
	call := func(key string) {
		t.Helper()
		resp, err := cache.do(key, req, fetch)
		require.NoError(t, err)
		resp.Body.Close()
	}
	call(`POST:{"height":"10","page":"1"}`)
	call(`POST:{"height":"10","page":"1"}`)
	require.Equal(t, 1, calls)
	call(`POST:{"height":"11","page":"1"}`)
	call(`POST:{"height":"11","page":"2"}`)
	call(`GET:height=11&page=2`)
	require.Equal(t, 4, calls)
	cache.expires = time.Time{}
	call(`GET:height=11&page=2`)
	require.Equal(t, 5, calls)
}

func TestCometProxyCacheDoesNotCacheErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP failure", 503, `unavailable`},
		{"RPC failure", 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"unavailable"}}`},
		{"invalid JSON", 200, `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cache cometProxyCache
			calls := 0
			for range 2 {
				resp, err := cache.do("key", rpctypes.NewRPCRequest(rpctypes.JSONRPCIntID(1), "status", nil), func() (*http.Response, error) { calls++; return proxyResponse(tc.status, tc.body), nil })
				require.NoError(t, err)
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				require.NoError(t, err)
				require.Equal(t, tc.body, string(body))
				require.Equal(t, tc.status, resp.StatusCode)
			}
			require.Equal(t, 2, calls)
		})
	}
}
