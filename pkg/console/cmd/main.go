package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/console"
	"github.com/AudiusProject/audiusd/pkg/etl"
	"github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func main() {
	logger := common.NewLogger(nil)

	logger.Info("Starting Console")

	// Start postgres container with volume
	dbURL, err := startPostgres(context.Background())
	if err != nil {
		log.Fatalf("Failed to start postgres: %s", err)
	}

	logger.Info("pgURL", "url", dbURL)
	// wait for postgres to be ready
	time.Sleep(5 * time.Second)

	auds := sdk.NewAudiusdSDK("rpc.audius.engineering")

	etl := etl.NewETLService(auds.Core, nil)
	etl.SetDBURL(dbURL)
	etl.SetCheckReadiness(false)

	console := console.NewConsole(etl, nil, "prod")
	console.Initialize()

	defer console.Stop()
	if err := console.Run(); err != nil {
		log.Fatal(err)
	}
}

func startPostgres(ctx context.Context) (string, error) {
	pguser := "postgres"
	pgpass := "postgres"
	pgdb := "audiusd"

	// Get current working directory and create absolute path for postgres data
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %w", err)
	}

	postgresDataPath := filepath.Join(cwd, "tmp", "postgres-data")

	// Create the directory if it doesn't exist
	if err := os.MkdirAll(postgresDataPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create postgres data directory: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		Name:         "audiusd-console-postgres",
		ExposedPorts: []string{"15432:5432/tcp"},
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
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	host, err := postgresC.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get container host: %w", err)
	}

	// Use fixed port 15432 for consistent TablePlus connections
	dsn := fmt.Sprintf("postgres://%s:%s@%s:15432/%s?sslmode=disable", pguser, pgpass, host, pgdb)
	return dsn, nil
}
