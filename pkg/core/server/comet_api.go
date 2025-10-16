// Request forward for the internal cometbft rpc. Debug info and to be turned off by default.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
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
		var rpcReq rpctypes.RPCRequest
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

		// Check if method is allowed
		if _, ok := allowedMethods[basePath]; !ok {
			s.logger.Warn("blocked unauthorized RPC method",
				zap.String("method", basePath))
			return respondWithError(c, http.StatusForbidden, "RPC method not allowed")
		}
	}

	// Create HTTP client with Unix socket transport
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialer := net.Dialer{}
				return dialer.DialContext(ctx, "unix", config.CometRPCSocket)
			},
		},
	}

	s.logger.Info("request", zap.String("socket", config.CometRPCSocket), zap.String("method", c.Request().Method), zap.String("url", c.Request().RequestURI))

	// For Unix sockets, the host is ignored, but we need to provide one
	path := "http://localhost" + strings.TrimPrefix(c.Request().RequestURI, "/core/crpc")

	req, err := http.NewRequest(c.Request().Method, path, bodyToForward)
	if err != nil {
		s.logger.Error("failed to create internal comet api request", zap.Error(err))
		return respondWithError(c, http.StatusInternalServerError, "failed to create internal comet request")
	}

	copyHeaders(c.Request().Header, req.Header)

	resp, err := httpClient.Do(req)
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
