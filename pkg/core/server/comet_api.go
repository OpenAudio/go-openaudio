package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

func (s *Server) registerCRPCRoutes(g *echo.Group) {
	// Explicit REST-style endpoints
	g.GET("/crpc/status", s.getStatus)
	g.GET("/crpc/health", s.getHealth)
	g.GET("/crpc/block", s.getBlock)

	// JSON-RPC endpoint
	g.POST("/crpc", s.handleJSONRPC)
}

// ---------- Plain GET endpoints ----------

func (s *Server) getStatus(c echo.Context) error {
	ctx := c.Request().Context()
	res, err := s.rpc.Status(ctx)
	if err != nil {
		return respondWithError(c, 502, err.Error())
	}
	return c.JSON(http.StatusOK, wrapJSONRPC(res))
}

func (s *Server) getHealth(c echo.Context) error {
	ctx := c.Request().Context()
	res, err := s.rpc.Health(ctx)
	if err != nil {
		return respondWithError(c, 502, err.Error())
	}
	return c.JSON(http.StatusOK, wrapJSONRPC(res))
}

func (s *Server) getBlock(c echo.Context) error {
	ctx := c.Request().Context()
	heightParam := c.QueryParam("height")

	var height *int64
	if heightParam != "" {
		h, err := strconv.ParseInt(heightParam, 10, 64)
		if err != nil {
			return respondWithError(c, 400, "invalid height")
		}
		height = &h
	}

	res, err := s.rpc.Block(ctx, height)
	if err != nil {
		return respondWithError(c, 502, err.Error())
	}
	return c.JSON(http.StatusOK, wrapJSONRPC(res))
}

// ---------- JSON-RPC endpoint ----------

func (s *Server) handleJSONRPC(c echo.Context) error {
	ctx := c.Request().Context()

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := c.Bind(&req); err != nil {
		return respondWithError(c, 400, "bad request")
	}

	switch req.Method {
	case "status":
		res, err := s.rpc.Status(ctx)
		if err != nil {
			return respondWithError(c, 502, err.Error())
		}
		return c.JSON(http.StatusOK, newJSONRPCResponse(req.ID, res))

	case "health":
		res, err := s.rpc.Health(ctx)
		if err != nil {
			return respondWithError(c, 502, err.Error())
		}
		return c.JSON(http.StatusOK, newJSONRPCResponse(req.ID, res))

	case "block":
		var params []any
		_ = json.Unmarshal(req.Params, &params)

		var height *int64
		if len(params) > 0 {
			switch v := params[0].(type) {
			case float64:
				h := int64(v)
				height = &h
			case string:
				if h, err := strconv.ParseInt(v, 10, 64); err == nil {
					height = &h
				}
			}
		}
		res, err := s.rpc.Block(ctx, height)
		if err != nil {
			return respondWithError(c, 502, err.Error())
		}
		return c.JSON(http.StatusOK, newJSONRPCResponse(req.ID, res))

	default:
		return c.JSON(http.StatusOK, map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]any{
				"code":    -32601,
				"message": "method not found",
			},
		})
	}
}

// ---------- Helpers ----------

func newJSONRPCResponse(id any, result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      idOrDefault(id),
		"result":  result,
	}
}

func idOrDefault(id any) any {
	if id == nil {
		return -1
	}
	return id
}

func wrapJSONRPC(result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      -1,
		"result":  result,
	}
}

func respondWithError(c echo.Context, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}
