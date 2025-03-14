// contains all the routes that core serves
package server

import (
	"context"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_gql"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"github.com/AudiusProject/audiusd/pkg/core/gql"
	"github.com/gorilla/websocket"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
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

	// wait for grpc server to start first since the http
	// forward requires the grpc routes to be functional
	<-s.awaitGrpcServerReady
	gwMux := runtime.NewServeMux()
	if err := core_proto.RegisterProtocolHandlerServer(context.TODO(), gwMux, s); err != nil {
		s.logger.Errorf("could not register protocol handler server: %v", err)
	}

	gqlResolver := gql.NewGraphQLServer(s.config, s.logger, s.db)
	srv := handler.New(core_gql.NewExecutableSchema(core_gql.Config{Resolvers: gqlResolver}))
	srv.Use(extension.Introspection{})
	queryHandler := func(c echo.Context) error {
		srv.ServeHTTP(c.Response(), c.Request())
		return nil
	}

	// Add WebSocket support for subscriptions
	srv.AddTransport(transport.Websocket{
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		KeepAlivePingInterval: 10 * time.Second,
	})

	// HTTP transport for queries and mutations
	srv.AddTransport(transport.POST{})

	gqlPlayground := playground.Handler("GraphQL playground", "/core/gql")
	graphiqlHandler := func(c echo.Context) error {
		gqlPlayground.ServeHTTP(c.Response(), c.Request())
		return nil
	}

	g := s.httpServer.Group("/core")

	/** /core routes **/
	g.Any("/grpc/*", echo.WrapHandler(gwMux))
	g.Any("/gql", queryHandler)
	g.GET("/graphiql", graphiqlHandler)
	g.GET("/nodes", s.getRegisteredNodes)
	g.GET("/nodes/verbose", s.getRegisteredNodes)
	g.GET("/nodes/discovery", s.getRegisteredNodes)
	g.GET("/nodes/discovery/verbose", s.getRegisteredNodes)
	g.GET("/nodes/content", s.getRegisteredNodes)
	g.GET("/nodes/content/verbose", s.getRegisteredNodes)
	g.GET("/nodes/eth", s.getEthNodesHandler)

	if s.config.CometModule {
		g.Any("/debug/comet*", s.proxyCometRequest)
	}

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
