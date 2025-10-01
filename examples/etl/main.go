package main

import (
	"context"
	"fmt"
	"log"

	etlv1connect "github.com/AudiusProject/audiusd/pkg/api/etl/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/etl"
	"github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/labstack/echo/v4"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/sync/errgroup"
)

func main() {
	dbURL, err := startPostgres(context.Background())
	if err != nil {
		log.Fatalf("Failed to start postgres: %s", err)
	}

	logger := common.NewLogger(nil)

	logger.Info("pgURL", "url", dbURL)

	// create auds instance
	auds := sdk.NewAudiusdSDK("audius-content-12.cultur3stake.com")

	// pass core client to etl service
	etl := etl.NewETLService(auds.Core, nil)
	etl.SetDBURL(dbURL)
	etl.SetRunDownMigrations(true)
	// index 500 blocks
	etl.SetEndingBlockHeight(500)

	e := echo.New()
	e.HideBanner = true

	// register etl routes to echo server
	path, handler := etlv1connect.NewETLServiceHandler(etl)
	e.Group("").Any(path+"*", echo.WrapHandler(handler))

	// run echo server and etl service in parallel
	g, _ := errgroup.WithContext(context.Background())

	g.Go(func() error {
		return e.Start(":8080")
	})

	g.Go(func() error {
		return etl.Run()
	})

	if err := g.Wait(); err != nil {
		log.Fatalf("Failed to run etl: %s", err)
	}
}

func startPostgres(ctx context.Context) (string, error) {
	pguser := "postgres"
	pgpass := "postgres"
	pgdb := "etl_db"

	req := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		Name:         "postgres-etl",
		ExposedPorts: []string{"5432:5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     pguser,
			"POSTGRES_PASSWORD": pgpass,
			"POSTGRES_DB":       pgdb,
		},
		WaitingFor: wait.ForListeningPort("5432/tcp"),
	}
	postgresC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
		Reuse:            true,
	})
	if err != nil {
		log.Fatalf("Failed to start container: %s", err)
	}

	host, _ := postgresC.Host(ctx)
	port, _ := postgresC.MappedPort(ctx, "5432")

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", pguser, pgpass, host, port.Port(), pgdb)
	return dsn, nil
}
