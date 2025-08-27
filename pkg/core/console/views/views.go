package views

import (
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/console/views/layout"
	"github.com/AudiusProject/audiusd/pkg/core/console/views/pages"
	"github.com/labstack/echo/v4"
)

type Views struct {
	pages   *pages.Pages
	layouts *layout.Layout
}

func NewViews(config *config.Config, baseUrl string) *Views {
	return &Views{
		pages:   pages.NewPages(config, baseUrl),
		layouts: layout.NewLayout(config, baseUrl),
	}
}

func (v *Views) RenderNavChainData(c echo.Context, totalBlocks string, syncing bool) error {
	return v.layouts.NavBlockData(totalBlocks, syncing).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderNodesView(c echo.Context, view *pages.NodesView) error {
	return v.pages.NodesPageHTML(view).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderNodeView(c echo.Context, view *pages.NodePageView) error {
	return v.pages.NodePageHTML(view).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderContentView(c echo.Context) error {
	return v.pages.ContentPageHTML().Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderUptimeView(c echo.Context, data *pages.UptimePageView) error {
	return v.pages.UptimePageHTML(data).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderPoSView(c echo.Context, data *pages.PoSPageView) error {
	return v.pages.PoSPageHTML(data).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderErrorView(c echo.Context, errorID string) error {
	return v.pages.ErrorPageHTML(errorID).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderGenesisView(c echo.Context, g map[string]interface{}) error {
	return v.pages.GenesisHTML(g).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderUploadPageView(c echo.Context) error {
	return v.pages.UploadPage().Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderBlockView(c echo.Context, view *pages.BlockView) error {
	return v.pages.BlockPageHTML(view).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderTxView(c echo.Context, view *pages.TxView) error {
	return v.pages.TxPageHTML(view).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderAdjudicateView(c echo.Context, view *pages.AdjudicatePageView) error {
	return v.pages.AdjudicatePageHTML(view).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderOverview(c echo.Context, status *v1.GetStatusResponse) error {
	return v.pages.OverviewPage(status).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderOverviewCritical(c echo.Context, status *v1.GetStatusResponse) error {
	return v.pages.OverviewCriticalFragment(status).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderOverviewProcesses(c echo.Context, status *v1.GetStatusResponse) error {
	return v.pages.OverviewProcessesFragment(status).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderOverviewResources(c echo.Context, status *v1.GetStatusResponse) error {
	return v.pages.OverviewResourcesFragment(status).Render(c.Request().Context(), c.Response().Writer)
}

func (v *Views) RenderOverviewNetwork(c echo.Context, status *v1.GetStatusResponse) error {
	return v.pages.OverviewNetworkFragment(status).Render(c.Request().Context(), c.Response().Writer)
}
