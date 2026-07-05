package console

import (
	"embed"
	"net/http"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/core/console/middleware"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
)

const baseURL = "/console"

//go:embed assets/js/*
//go:embed assets/css/*
//go:embed assets/images/*
var embeddedAssets embed.FS

func (c *Console) registerRoutes() {

	g := c.e.Group(baseURL)
	historicalPageLimiter := consoleHistoricalPageLimiter()

	g.Use(consoleNoIndexMiddleware)
	g.Use(middleware.JsonExtensionMiddleware)
	g.Use(middleware.ErrorLoggerMiddleware(c.logger, c.views))

	g.GET("", func(ctx echo.Context) error {
		// Redirect to the base group's overview page
		basePath := ctx.Path()
		return ctx.Redirect(http.StatusMovedPermanently, basePath+"/overview")
	})

	g.StaticFS("/*", embeddedAssets)

	g.GET("/overview", c.overviewPage)
	g.GET("/storage", c.storagePage)
	g.GET("/validators", c.nodesPage)
	g.GET("/validator", c.nodesPage)
	g.GET("/api/core-validators-endpoints", c.coreValidatorsEndpointsAPI)
	g.GET("/api/matrix", c.matrixAPI)
	g.GET("/api/version-adoption", c.versionAdoptionAPI)
	g.GET("/validator/:validator", c.nodePage)
	g.GET("/uptime/:rollup/:endpoint", c.uptimeFragment, historicalPageLimiter)
	g.GET("/uptime/:rollup", c.uptimeFragment, historicalPageLimiter)
	g.GET("/uptime", c.uptimeFragment, historicalPageLimiter)
	g.GET("/pos", c.posFragment, historicalPageLimiter)
	g.GET("/pos/:address", c.posFragment, historicalPageLimiter)
	g.GET("/block/:block", c.blockPage, historicalPageLimiter)
	g.GET("/tx/:tx", c.txPage, historicalPageLimiter)
	g.GET("/genesis", c.genesisPage)
	g.GET("/adjudicate/:sp", c.adjudicateFragment, historicalPageLimiter)
	g.GET("/health_check", c.getHealth)

	g.GET("/fragments/nav/chain_data", c.navChainData)
	g.GET("/fragments/nav/jailed_status", c.navJailedStatus)
	g.GET("/fragments/overview/critical", c.overviewCriticalFragment)
	g.GET("/fragments/overview/processes", c.overviewProcessesFragment)
	g.GET("/fragments/overview/resources", c.overviewResourcesFragment)
	g.GET("/fragments/overview/storage", c.overviewStorageFragment)
	g.GET("/fragments/overview/network", c.overviewNetworkFragment)

	// future pages
	// g.GET("/blocks", c.blocksPage)
	// g.GET("/txs", c.txsPage)
	//g.GET("/nodes/:node", c.nodePage)
	//g.GET("/content/users", c.usersPage)
	//g.GET("/content/tracks", c.tracksPage)
	//g.GET("/content/plays", c.playsPage)
}

func consoleNoIndexMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		c.Response().Header().Set("X-Robots-Tag", "noindex, nofollow")
		return next(c)
	}
}

func consoleHistoricalPageLimiter() echo.MiddlewareFunc {
	store := echomiddleware.NewRateLimiterMemoryStoreWithConfig(echomiddleware.RateLimiterMemoryStoreConfig{
		Rate:      0.5,
		Burst:     10,
		ExpiresIn: 10 * time.Minute,
	})

	return echomiddleware.RateLimiter(store)
}
