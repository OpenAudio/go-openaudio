// contains all the routes that core serves
package server

import (
	"net/http"
	"net/http/pprof"

	"github.com/AudiusProject/audiusd/pkg/core/console/views/sandbox"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func (s *Server) startEchoServer() error {
	s.logger.Info("core HTTP server starting")
	// create http server
	httpServer := s.httpServer
	httpServer.Pre(middleware.RemoveTrailingSlash())
	httpServer.Use(middleware.Recover())
	httpServer.Use(middleware.CORS())
	httpServer.HideBanner = true

	g := s.httpServer.Group("/core")

	g.GET("/rewards", s.getRewards)
	g.GET("/rewards/attestation", s.getRewardAttestation)
	g.GET("/nodes", s.getRegisteredNodes)
	g.GET("/nodes/verbose", s.getRegisteredNodes)
	g.GET("/nodes/discovery", s.getRegisteredNodes)
	g.GET("/nodes/discovery/verbose", s.getRegisteredNodes)
	g.GET("/nodes/content", s.getRegisteredNodes)
	g.GET("/nodes/content/verbose", s.getRegisteredNodes)
	g.GET("/nodes/eth", s.getEthNodesHandler)
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

	close(s.awaitHttpServerReady)
	s.logger.Info("core http server ready")

	if err := s.httpServer.Start(s.config.CoreServerAddr); err != nil {
		s.logger.Errorf("echo failed to start: %v", err)
		return err
	}
	return nil
}

func (s *Server) GetEcho() *echo.Echo {
	return s.httpServer
}
