package console

import (
	"fmt"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/console/views"
	"github.com/AudiusProject/audiusd/pkg/core/console/views/layout"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/eth"
	"github.com/cometbft/cometbft/rpc/client"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

type Console struct {
	config *config.Config
	rpc    client.Client
	db     *db.Queries
	e      *echo.Echo
	logger *common.Logger
	eth    *eth.EthService

	state   *State
	layouts *layout.Layout
	views   *views.Views
}

func NewConsole(config *config.Config, logger *common.Logger, e *echo.Echo, pool *pgxpool.Pool, ethService *eth.EthService) (*Console, error) {
	l := logger.Child("console")
	db := db.New(pool)
	httprpc, err := rpchttp.New(config.RPCladdr)
	if err != nil {
		return nil, fmt.Errorf("could not create rpc client: %v", err)
	}
	state, err := NewState(config, httprpc, l, db)
	if err != nil {
		return nil, err
	}
	c := &Console{
		config:  config,
		rpc:     httprpc,
		e:       e,
		logger:  l,
		eth:     ethService,
		db:      db,
		state:   state,
		views:   views.NewViews(config, baseURL),
		layouts: layout.NewLayout(config, baseURL),
	}

	c.registerRoutes(logger, e)

	return c, nil
}

func (c *Console) Start() error {
	return c.state.Start()
}
