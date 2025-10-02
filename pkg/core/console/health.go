package console

import (
	"os"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/labstack/echo/v4"
)

type HealthCheckResponse struct {
	Healthy           bool     `json:"healthy"`
	Errors            []string `json:"errors"`
	TotalBlocks       int64    `json:"totalBlocks"`
	TotalTransactions int64    `json:"totalTransactions"`
	ChainId           string   `json:"chainId"`
	EthAddress        string   `json:"ethAddress"`
	CometAddress      string   `json:"cometAddress"`
	Git               string   `json:"git"`
}

func (con *Console) getHealth(c echo.Context) error {
	errs := []string{}

	res := HealthCheckResponse{}

	if con.core == nil {
		return c.JSON(500, "not ready")
	}
	c.Response().Header().Set("Access-Control-Allow-Origin", "*")
	statusRes, err := con.core.GetStatus(c.Request().Context(), &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		errs = append(errs, err.Error())
	}

	if statusRes != nil {
		status := statusRes.Msg

		if !status.SyncInfo.Synced {
			errs = append(errs, "Node is syncing")
		}

		res.Healthy = status.Ready
		res.TotalBlocks = status.ChainInfo.CurrentHeight
		res.TotalTransactions = status.ChainInfo.TotalTxCount
		res.ChainId = status.ChainInfo.ChainId
		res.EthAddress = status.NodeInfo.EthAddress
		res.CometAddress = status.NodeInfo.CometAddress
		res.Git = os.Getenv("GIT_SHA")
	}

	res.Errors = errs
	if len(res.Errors) > 0 {
		res.Healthy = false
	}
	return c.JSON(200, res)
}
