// Request forward for the internal cometbft rpc. Debug info and to be turned off by default.
package server

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
}

func (s *Server) proxyCometRequest(c echo.Context) error {
	rpcUrl := strings.ReplaceAll(s.config.RPCladdr, "tcp", "http")

	s.logger.Info("request", zap.String("url", rpcUrl), zap.String("method", c.Request().Method), zap.String("url", c.Request().RequestURI))

	path := rpcUrl + strings.TrimPrefix(c.Request().RequestURI, "/core/crpc")

	req, err := http.NewRequest(c.Request().Method, path, c.Request().Body)
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
	for k, v := range source {
		destination[k] = v
	}
}

func respondWithError(c echo.Context, statusCode int, message string) error {
	return c.JSON(statusCode, map[string]string{"error": message})
}
