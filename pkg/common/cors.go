package common

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var corsConfig = middleware.CORSConfig{
	AllowOrigins: []string{"*"},
	AllowMethods: []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPut,
		http.MethodPatch,
		http.MethodPost,
		http.MethodDelete,
		http.MethodOptions,
	},
	AllowHeaders: []string{
		echo.HeaderOrigin,
		echo.HeaderContentType,
		echo.HeaderAccept,
		echo.HeaderAuthorization,
		"X-User-Wallet-Addr",
	},
}

func CORS() echo.MiddlewareFunc {
	return middleware.CORSWithConfig(corsConfig)
}

func ApplyCORSHeaders(resp *http.Response) {
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		if len(corsConfig.AllowOrigins) > 0 {
			resp.Header.Set("Access-Control-Allow-Origin", strings.Join(corsConfig.AllowOrigins, ", "))
		}
	}
	if resp.Header.Get("Access-Control-Allow-Methods") == "" {
		if len(corsConfig.AllowMethods) > 0 {
			resp.Header.Set("Access-Control-Allow-Methods", strings.Join(corsConfig.AllowMethods, ", "))
		}
	}
	if resp.Header.Get("Access-Control-Allow-Headers") == "" {
		if len(corsConfig.AllowHeaders) > 0 {
			resp.Header.Set("Access-Control-Allow-Headers", strings.Join(corsConfig.AllowHeaders, ", "))
		}
	}
}
