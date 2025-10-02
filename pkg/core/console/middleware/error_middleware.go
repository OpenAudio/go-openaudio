package middleware

import (
	"github.com/OpenAudio/go-openaudio/pkg/core/console/views"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

func ErrorLoggerMiddleware(logger *zap.Logger, views *views.Views) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			if err != nil {
				errorID := uuid.NewString()
				logger.Error("error occured", zap.String("id", errorID), zap.Error(err), zap.String("path", c.Path()))
				return views.RenderErrorView(c, errorID)
			}
			return nil
		}
	}
}
