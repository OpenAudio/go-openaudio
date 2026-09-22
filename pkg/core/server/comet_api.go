// Request forward for the internal cometbft rpc. Debug info and to be turned off by default.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// allowedMethods defines the allowed RPC methods for state sync
// This is read-only after initialization, making it safe for concurrent access
var allowedMethods = map[string]struct{}{
	"status":           {},
	"block":            {},
	"commit":           {},
	"validators":       {},
	"consensus_params": {}, // Required for state sync to fetch consensus parameters
	"health":           {},
}

// maxRequestBodySize limits request size to 64KB - RPC requests should be tiny
const maxRequestBodySize = 64 * 1024

func (s *Server) proxyCometRequest(c echo.Context) error {
	if !s.config.StateSync.ServeSnapshots {
		return respondWithError(c, http.StatusForbidden, "state sync not enabled")
	}

	// Only allow GET and POST methods
	if c.Request().Method != "GET" && c.Request().Method != "POST" {
		return respondWithError(c, http.StatusMethodNotAllowed, "method not allowed")
	}

	// Handle validation based on request method
	var bodyToForward io.Reader = c.Request().Body
	var rpcReq rpctypes.RPCRequest

	if c.Request().Method == "POST" {
		// Read the body to validate JSONRPC method (with size limit)
		body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxRequestBodySize))
		if err != nil {
			s.logger.Error("failed to read request body", zap.Error(err))
			return respondWithError(c, http.StatusBadRequest, "failed to read request")
		}

		// Check if request was too large (if we read exactly the limit, there might be more)
		if len(body) == maxRequestBodySize {
			s.logger.Warn("request body too large", zap.Int("size", len(body)))
			return respondWithError(c, http.StatusRequestEntityTooLarge, "request body too large")
		}

		// Parse JSONRPC request
		if err := json.Unmarshal(body, &rpcReq); err != nil {
			s.logger.Error("failed to parse JSONRPC request", zap.Error(err))
			return respondWithError(c, http.StatusBadRequest, "invalid JSONRPC request")
		}

		// Check if method is allowed
		if _, ok := allowedMethods[rpcReq.Method]; !ok {
			s.logger.Warn("blocked unauthorized RPC method",
				zap.String("method", rpcReq.Method))
			return respondWithError(c, http.StatusForbidden, "RPC method not allowed")
		}

		// Create new reader from body for forwarding
		bodyToForward = bytes.NewReader(body)
	} else if c.Request().Method == "GET" {
		// For GET requests, check the path
		rpcPath := strings.TrimPrefix(c.Request().RequestURI, "/core/crpc")
		basePath := strings.TrimPrefix(rpcPath, "/")
		if idx := strings.Index(basePath, "?"); idx != -1 {
			basePath = basePath[:idx]
		}

		rpcReq = rpctypes.NewRPCRequest(rpctypes.JSONRPCIntID(-1), basePath, nil)
		// Check if method is allowed
		if _, ok := allowedMethods[basePath]; !ok {
			s.logger.Warn("blocked unauthorized RPC method",
				zap.String("method", basePath))
			return respondWithError(c, http.StatusForbidden, "RPC method not allowed")
		}
	}

	// Serve expensive monitoring queries through the same cache as local callers.
	if rpcReq.Method == "status" || rpcReq.Method == "validators" {
		return s.serveCachedCometRequest(c, rpcReq)
	}

	s.logger.Info("request", zap.String("socket", config.CometRPCSocket), zap.String("method", c.Request().Method), zap.String("url", c.Request().RequestURI))

	// For Unix sockets, the host is ignored, but we need to provide one
	path := "http://localhost" + strings.TrimPrefix(c.Request().RequestURI, "/core/crpc")

	req, err := http.NewRequestWithContext(c.Request().Context(), c.Request().Method, path, bodyToForward)
	if err != nil {
		s.logger.Error("failed to create internal comet api request", zap.Error(err))
		return respondWithError(c, http.StatusInternalServerError, "failed to create internal comet request")
	}

	copyHeaders(c.Request().Header, req.Header)

	resp, err := cometProxyClient.Do(req)
	if err != nil {
		s.logger.Error("failed to forward comet api request", zap.Error(err))
		return respondWithError(c, http.StatusInternalServerError, "failed to forward request")
	}
	defer resp.Body.Close()

	c.Response().Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	c.Response().WriteHeader(resp.StatusCode)
	_, err = io.Copy(c.Response().Writer, resp.Body)
	if err != nil {
		return respondWithError(c, http.StatusInternalServerError, "failed to stream response")
	}

	return nil
}

func copyHeaders(source http.Header, destination http.Header) {
	// Only copy safe headers for RPC requests
	safeHeaders := map[string]bool{
		"Content-Type":  true,
		"Accept":        true,
		"Cache-Control": true,
		"User-Agent":    true,
	}

	for k, v := range source {
		// Only copy whitelisted headers
		if safeHeaders[k] {
			destination[k] = v
		}
	}
}

func respondWithError(c echo.Context, statusCode int, message string) error {
	return c.JSON(statusCode, map[string]string{"error": message})
}

// Reusing the transport also bounds idle Unix-socket connections.
var cometProxyClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns: 16, MaxIdleConnsPerHost: 16, IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", config.CometRPCSocket)
		},
	},
}

func (s *Server) serveCachedCometRequest(c echo.Context, req rpctypes.RPCRequest) error {
	select {
	case <-s.awaitRpcReady:
	default:
		return respondWithError(c, http.StatusServiceUnavailable, "RPC not ready")
	}
	var result any
	var err error
	if req.Method == "status" {
		result, err = s.rpc.Status(c.Request().Context())
	} else {
		height, page, perPage, parseErr := cometValidatorParams(c.Request(), req.Params)
		if parseErr != nil {
			return c.JSON(http.StatusOK, rpctypes.RPCInvalidParamsError(req.ID, parseErr))
		}
		result, err = s.rpc.Validators(c.Request().Context(), height, page, perPage)
	}
	if err != nil {
		return c.JSON(http.StatusOK, rpctypes.RPCInternalError(req.ID, err))
	}
	return c.JSON(http.StatusOK, rpctypes.NewRPCSuccessResponse(req.ID, result))
}

func cometValidatorParams(req *http.Request, raw json.RawMessage) (*int64, *int, *int, error) {
	var height *int64
	var page, perPage *int
	fields := []struct {
		name  string
		value any
	}{{"height", &height}, {"page", &page}, {"per_page", &perPage}}
	params := map[string]json.RawMessage{}
	if req.Method == http.MethodGet {
		for _, field := range fields {
			if value := req.URL.Query().Get(field.name); value != "" {
				params[field.name], _ = json.Marshal(value)
			}
		}
	} else if len(raw) > 0 && string(raw) != "null" {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var values []json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, nil, nil, err
			}
			if len(values) != len(fields) {
				return nil, nil, nil, fmt.Errorf("expected 3 validator parameters")
			}
			for i, field := range fields {
				params[field.name] = values[i]
			}
		} else if err := json.Unmarshal(raw, &params); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, field := range fields {
		if value, ok := params[field.name]; ok {
			if err := cmtjson.Unmarshal(value, field.value); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	return height, page, perPage, nil
}
