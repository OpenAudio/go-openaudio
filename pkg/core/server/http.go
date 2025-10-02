// contains all the routes that core serves
package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/console/views/sandbox"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"
)

func (s *Server) startEchoServer(ctx context.Context) error {
	s.StartProcess(ProcessStateEchoServer)
	s.logger.Info("core HTTP server starting")
	// create http server
	httpServer := s.httpServer
	httpServer.Pre(middleware.RemoveTrailingSlash())
	httpServer.Use(middleware.Recover())
	httpServer.Use(middleware.CORS())
	httpServer.HideBanner = true

	g := s.httpServer.Group("/core")

	// TODO: add into connectrpc
	g.GET("/rewards", s.getRewards)
	g.GET("/rewards/attestation", s.getRewardAttestation)
	g.GET("/nodes", s.getRegisteredNodes)
	g.GET("/nodes/verbose", s.getRegisteredNodes)
	g.GET("/nodes/discovery", s.getRegisteredNodes)
	g.GET("/nodes/discovery/verbose", s.getRegisteredNodes)
	g.GET("/nodes/content", s.getRegisteredNodes)
	g.GET("/nodes/content/verbose", s.getRegisteredNodes)
	g.GET("/status", func(c echo.Context) error {
		if s.self == nil {
			return c.String(http.StatusServiceUnavailable, "starting up")
		}
		res, err := s.self.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res.Msg)
	})

	// proxy cometbft requests
	g.Any("/crpc*", s.proxyCometRequest)

	// kind of weird pattern
	s.createEthRPC()

	g.GET("/sdk", echo.WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandbox.ServeSandbox(s.config, w, r)
	})))

	if s.config.DebugModule {
		g.GET("/debug/mempl", s.getMempl)
	}

	if s.config.PprofModule {
		g.GET("/debug/pprof/", echo.WrapHandler(http.HandlerFunc(pprof.Index)))
		g.GET("/debug/pprof/cmdline", echo.WrapHandler(http.HandlerFunc(pprof.Cmdline)))
		g.GET("/debug/pprof/profile", echo.WrapHandler(http.HandlerFunc(pprof.Profile)))
		g.GET("/debug/pprof/symbol", echo.WrapHandler(http.HandlerFunc(pprof.Symbol)))
		g.POST("/debug/pprof/symbol", echo.WrapHandler(http.HandlerFunc(pprof.Symbol)))
		g.GET("/debug/pprof/trace", echo.WrapHandler(http.HandlerFunc(pprof.Trace)))
		g.GET("/debug/pprof/heap", echo.WrapHandler(pprof.Handler("heap")))
		g.GET("/debug/pprof/goroutine", echo.WrapHandler(pprof.Handler("goroutine")))
		g.GET("/debug/pprof/threadcreate", echo.WrapHandler(pprof.Handler("threadcreate")))
		g.GET("/debug/pprof/block", echo.WrapHandler(pprof.Handler("block")))
	}

	done := make(chan error, 1)
	go func() {
		err := s.httpServer.Start(s.config.CoreServerAddr)
		if err != nil && err != http.ErrServerClosed {
			s.logger.Error("echo server error", zap.Error(err))
			s.ErrorProcess(ProcessStateEchoServer, fmt.Sprintf("echo server error: %v", err))
			done <- err
		} else {
			done <- nil
		}
	}()

	close(s.awaitHttpServerReady)
	s.RunningProcess(ProcessStateEchoServer)
	s.logger.Info("core http server ready")

	select {
	case err := <-done:
		if err != nil {
			s.ErrorProcess(ProcessStateEchoServer, fmt.Sprintf("echo server failed: %v", err))
		} else {
			s.CompleteProcess(ProcessStateEchoServer)
		}
		return err
	case <-ctx.Done():
		s.SleepingProcess(ProcessStateEchoServer)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := s.httpServer.Shutdown(shutdownCtx)
		if err != nil {
			s.logger.Error("failed to shutdown echo server", zap.Error(err))
			s.ErrorProcess(ProcessStateEchoServer, fmt.Sprintf("failed to shutdown echo server: %v", err))
			return err
		}
		s.CompleteProcess(ProcessStateEchoServer)
		return ctx.Err()
	}
}

func (s *Server) GetEcho() *echo.Echo {
	return s.httpServer
}
