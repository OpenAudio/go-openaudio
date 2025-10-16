package core

import (
	"context"
	"fmt"
	_ "net/http/pprof"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/console"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/eth"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/OpenAudio/go-openaudio/pkg/pos"
	"go.uber.org/zap"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, lc *lifecycle.Lifecycle, logger *zap.Logger, posChannel chan pos.PoSRequest, coreService *server.CoreService, ethService *eth.EthService) error {
	return run(ctx, lc, logger, posChannel, coreService, ethService)
}

func run(ctx context.Context, lc *lifecycle.Lifecycle, logger *zap.Logger, posChannel chan pos.PoSRequest, coreService *server.CoreService, ethService *eth.EthService) error {
	logger.Info("good morning!")

	config, cometConfig, err := config.SetupNode(logger)
	if err != nil {
		return fmt.Errorf("setting up node: %v", err)
	}

	logger.Info("configuration created")

	// db migrations
	if err := db.RunMigrations(logger, config.PSQLConn, config.RunDownMigrations()); err != nil {
		return fmt.Errorf("running migrations: %v", err)
	}

	logger.Info("db migrations successful")

	// Use the passed context for the pool
	pool, err := pgxpool.New(ctx, config.PSQLConn)
	if err != nil {
		return fmt.Errorf("couldn't create pgx pool: %v", err)
	}
	defer pool.Close()

	s, err := server.NewServer(lc, config, cometConfig, logger, pool, ethService, posChannel)
	if err != nil {
		return fmt.Errorf("server init error: %v", err)
	}

	s.CompactStateDB()
	s.CompactBlockstoreDB()
	logger.Info("finished compacting db")

	// console gets run from core(main).go since it is an isolated go module
	// unlike the other modules that register themselves on the echo http server
	e := s.GetEcho()
	_, err = console.NewConsole(config, logger, e, pool, ethService, coreService)
	if err != nil {
		logger.Error("console init error", zap.Error(err))
		return err
	}

	// create core service
	coreService.SetCore(s)

	if err := s.Start(); err != nil {
		logger.Error("core service crashed", zap.Error(err))
		return err
	}

	return s.Shutdown()
}
