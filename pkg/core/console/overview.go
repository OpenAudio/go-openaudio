package console

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/labstack/echo/v4"
)

func (cs *Console) getConsoleStatus(ctx context.Context) (*v1.GetStatusResponse, error) {
	res, err := cs.core.GetStatus(ctx, &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return nil, err
	}
	cs.applyChainHeightFallback(ctx, res.Msg)
	return res.Msg, nil
}

func (cs *Console) overviewPage(c echo.Context) error {
	status, err := cs.getConsoleStatus(c.Request().Context())
	if err != nil {
		return err
	}
	return cs.views.RenderOverview(c, status)
}

func (cs *Console) overviewCriticalFragment(c echo.Context) error {
	status, err := cs.getConsoleStatus(c.Request().Context())
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewCritical(c, status)
}

func (cs *Console) overviewProcessesFragment(c echo.Context) error {
	status, err := cs.getConsoleStatus(c.Request().Context())
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewProcesses(c, status)
}

func (cs *Console) overviewResourcesFragment(c echo.Context) error {
	status, err := cs.getConsoleStatus(c.Request().Context())
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewResources(c, status)
}

func (cs *Console) overviewStorageFragment(c echo.Context) error {
	status, err := cs.getConsoleStatus(c.Request().Context())
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewStorage(c, status)
}

func (cs *Console) overviewNetworkFragment(c echo.Context) error {
	status, err := cs.getConsoleStatus(c.Request().Context())
	if err != nil {
		return err
	}
	return cs.views.RenderOverviewNetwork(c, status)
}
