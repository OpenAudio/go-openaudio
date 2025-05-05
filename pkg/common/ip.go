package common

import (
	"context"

	"github.com/labstack/echo/v4"
)

type contextKey string

const ClientIPKey contextKey = "client-ip"

func GetClientIP(ctx context.Context) string {
	val := ctx.Value(ClientIPKey)
	if val == nil {
		return ""
	}

	ip, ok := val.(string)
	if !ok {
		return ""
	}

	return ip
}

func InjectRealIP() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ip := c.RealIP()
			ctx := context.WithValue(c.Request().Context(), ClientIPKey, ip)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
