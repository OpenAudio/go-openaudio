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

func ReplaceCORSHeaders(resp *http.Response) {
	resp.Header.Del("Access-Control-Allow-Origin")
	resp.Header.Del("Access-Control-Allow-Methods")
	resp.Header.Del("Access-Control-Allow-Headers")
	resp.Header.Del("Access-Control-Allow-Credentials")
	resp.Header.Del("Access-Control-Expose-Headers")
	resp.Header.Del("Access-Control-Max-Age")

	if len(corsConfig.AllowOrigins) > 0 {
		if len(corsConfig.AllowOrigins) == 1 && corsConfig.AllowOrigins[0] == "*" {
			resp.Header.Set("Access-Control-Allow-Origin", "*")
		} else {
			resp.Header.Set("Access-Control-Allow-Origin", strings.Join(corsConfig.AllowOrigins, ", "))
		}
	}
	if len(corsConfig.AllowMethods) > 0 {
		resp.Header.Set("Access-Control-Allow-Methods", strings.Join(corsConfig.AllowMethods, ", "))
	}
	if len(corsConfig.AllowHeaders) > 0 {
		resp.Header.Set("Access-Control-Allow-Headers", strings.Join(corsConfig.AllowHeaders, ", "))
	}
}
