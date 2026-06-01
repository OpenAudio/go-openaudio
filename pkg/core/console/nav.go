package console

import (
	"fmt"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/labstack/echo/v4"
)

func (con *Console) navChainData(c echo.Context) error {
	res, err := con.core.GetNodeInfo(c.Request().Context(), &connect.Request[v1.GetNodeInfoRequest]{})
	if err != nil {
		return err
	}

	totalBlocks := fmt.Sprint(con.latestKnownHeight(c.Request().Context(), res.Msg.CurrentHeight))
	isSyncing := !res.Msg.Synced

	return con.views.RenderNavChainData(c, totalBlocks, isSyncing)
}

func (con *Console) navJailedStatus(c echo.Context) error {
	jailed, err := con.currentNodeJailed(c.Request().Context())
	if err != nil {
		return err
	}
	return con.views.RenderNavJailedStatus(c, jailed)
}
