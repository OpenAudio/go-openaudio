package console

import (
	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/labstack/echo/v4"
)

func (cs *Console) overviewPage(c echo.Context) error {
	res, err := cs.core.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return err
	}
	return cs.views.RenderOverview(c, res.Msg)
}

func (cs *Console) overviewCriticalFragment(c echo.Context) error {
	res, err := cs.core.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewCritical(c, res.Msg)
}

func (cs *Console) overviewProcessesFragment(c echo.Context) error {
	res, err := cs.core.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewProcesses(c, res.Msg)
}

func (cs *Console) overviewResourcesFragment(c echo.Context) error {
	res, err := cs.core.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewResources(c, res.Msg)
}

func (cs *Console) overviewNetworkFragment(c echo.Context) error {
	res, err := cs.core.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewNetwork(c, res.Msg)
}
