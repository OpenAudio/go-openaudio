package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed sql/migrations/*
var migrationsFS embed.FS

func RunMigrations(logger *common.Logger, pgConnectionString string, downFirst bool) error {
	tries := 10
	var db *sql.DB
	var err error

	for {
		if tries < 0 {
			return errors.New("ran out of retries for migrations")
		}
		db, err = sql.Open("postgres", pgConnectionString)
		if err != nil {
			logger.Errorf("error opening sql db: %v", err)
			tries--
			time.Sleep(2 * time.Second)
			continue
		}
		if err = db.Ping(); err != nil {
			logger.Errorf("could not ping postgres: %v", err)
			tries--
			time.Sleep(2 * time.Second)
			continue
		}
		break
	}
	defer db.Close()

	return runMigrations(logger, db, downFirst)
}

func runMigrations(logger *common.Logger, db *sql.DB, downFirst bool) error {
	driver, err := postgres.WithInstance(db, &postgres.Config{
		MigrationsTable: "etl_db_migrations",
	})
	if err != nil {
		return fmt.Errorf("error creating postgres driver: %w", err)
	}

	source, err := iofs.New(migrationsFS, "sql/migrations")
	if err != nil {
		return fmt.Errorf("error creating iofs source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("error initializing migrate: %w", err)
	}
	defer m.Close()

	if downFirst {
		if err := m.Down(); err != nil && err != migrate.ErrNoChange {
			return fmt.Errorf("error running down migrations: %w", err)
		}
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("error running up migrations: %w", err)
	}

	logger.Infof("Migrations applied successfully")
	return nil
}
