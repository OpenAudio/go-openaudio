package console

import (
	"embed"
	"net/http"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/console/middleware"
	"github.com/labstack/echo/v4"
)

const baseURL = "/console"

//go:embed assets/js/*
//go:embed assets/images/*
var embeddedAssets embed.FS

func (c *Console) registerRoutes(logger *common.Logger, e *echo.Echo) {

	g := e.Group(baseURL)

	g.Use(middleware.JsonExtensionMiddleware)
	g.Use(middleware.ErrorLoggerMiddleware(logger, c.views))

	g.GET("", func(ctx echo.Context) error {
		// Redirect to the base group's overview page
		basePath := ctx.Path()
		return ctx.Redirect(http.StatusMovedPermanently, basePath+"/overview")
	})

	g.StaticFS("/*", embeddedAssets)

	g.GET("/overview", c.overviewPage)
	g.GET("/validators", c.nodesPage)
	g.GET("/validator", c.nodesPage)
	g.GET("/validator/:validator", c.nodePage)
	g.GET("/uptime/:rollup/:endpoint", c.uptimeFragment)
	g.GET("/uptime/:rollup", c.uptimeFragment)
	g.GET("/uptime", c.uptimeFragment)
	g.GET("/pos", c.posFragment)
	g.GET("/block/:block", c.blockPage)
	g.GET("/tx/:tx", c.txPage)
	g.GET("/genesis", c.genesisPage)
	g.GET("/adjudicate/:sp", c.adjudicateFragment)

	g.GET("/fragments/nav/chain_data", c.navChainData)
	g.GET("/fragments/overview/critical", c.overviewCriticalFragment)
	g.GET("/fragments/overview/processes", c.overviewProcessesFragment)
	g.GET("/fragments/overview/resources", c.overviewResourcesFragment)
	g.GET("/fragments/overview/network", c.overviewNetworkFragment)

	// future pages
	// g.GET("/blocks", c.blocksPage)
	// g.GET("/txs", c.txsPage)
	//g.GET("/nodes/:node", c.nodePage)
	//g.GET("/content/users", c.usersPage)
	//g.GET("/content/tracks", c.tracksPage)
	//g.GET("/content/plays", c.playsPage)
}
