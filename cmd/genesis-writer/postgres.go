package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	pgDatabase = "openaudio"
	pgPort     = "5440"
)

// managedPostgres manages a local PostgreSQL instance for the genesis writer.
// It initializes, starts, and stops a postgres cluster whose data directory
// mirrors the production layout: <data-dir>/postgres alongside
// <data-dir>/core/<chain-id>/.
type managedPostgres struct {
	dataDir string
	port    string
	binDir  string
	logger  *zap.Logger
	started bool
}

// findPgBinDir searches common locations for the pg_ctl binary directory.
func findPgBinDir() (string, error) {
	candidates := []string{
		// macOS Homebrew
		"/opt/homebrew/opt/postgresql@17/bin",
		"/opt/homebrew/opt/postgresql@16/bin",
		"/opt/homebrew/opt/postgresql@15/bin",
		"/usr/local/opt/postgresql@17/bin",
		"/usr/local/opt/postgresql@16/bin",
		"/usr/local/opt/postgresql@15/bin",
		// Linux
		"/usr/lib/postgresql/17/bin",
		"/usr/lib/postgresql/16/bin",
		"/usr/lib/postgresql/15/bin",
		"/usr/bin",
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "pg_ctl")); err == nil {
			return dir, nil
		}
	}
	// Fall back to PATH
	if path, err := exec.LookPath("pg_ctl"); err == nil {
		return filepath.Dir(path), nil
	}
	return "", fmt.Errorf("could not find pg_ctl; install PostgreSQL or set --dst-dsn")
}

// startManagedPostgres ensures a local PostgreSQL instance is running with its
// data directory at <dataDir>/postgres/. It handles all combinations:
//
//   - Fresh run: initdb → start → create database
//   - Resume (stopped): start existing cluster → verify database exists
//   - Resume (already running): verify database exists
func startManagedPostgres(dataDir string, logger *zap.Logger) (*managedPostgres, string, error) {
	binDir, err := findPgBinDir()
	if err != nil {
		return nil, "", err
	}

	pgDataDir := filepath.Join(dataDir, "postgres")
	pg := &managedPostgres{
		dataDir: pgDataDir,
		port:    pgPort,
		binDir:  binDir,
		logger:  logger,
	}

	if pg.isRunning() {
		logger.Info("postgres already running", zap.String("dataDir", pgDataDir))
	} else if pg.isInitialized() {
		logger.Info("postgres cluster exists, starting", zap.String("dataDir", pgDataDir))
		if err := pg.start(); err != nil {
			return nil, "", fmt.Errorf("start postgres: %w", err)
		}
	} else {
		logger.Info("no postgres cluster found, initializing", zap.String("dataDir", pgDataDir))
		if err := pg.initDB(); err != nil {
			return nil, "", fmt.Errorf("initdb: %w", err)
		}
		if err := pg.start(); err != nil {
			return nil, "", fmt.Errorf("start postgres: %w", err)
		}
	}

	if err := pg.ensureDatabase(); err != nil {
		return nil, "", fmt.Errorf("ensure database: %w", err)
	}

	username := os.Getenv("USER")
	if username == "" {
		if u, err := user.Current(); err == nil {
			username = u.Username
		} else {
			username = "postgres"
		}
	}
	dsn := fmt.Sprintf("postgres://%s@localhost:%s/%s?sslmode=disable", username, pg.port, pgDatabase)
	logger.Info("managed postgres ready",
		zap.String("dataDir", pgDataDir),
		zap.String("dsn", dsn),
	)

	return pg, dsn, nil
}

func (pg *managedPostgres) pgCtl(args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(pg.binDir, "pg_ctl"), args...)
	cmd.Env = append(os.Environ(), "PGDATA="+pg.dataDir)
	return cmd
}

func (pg *managedPostgres) isInitialized() bool {
	_, err := os.Stat(filepath.Join(pg.dataDir, "PG_VERSION"))
	return err == nil
}

func (pg *managedPostgres) isRunning() bool {
	cmd := pg.pgCtl("status", "-D", pg.dataDir)
	return cmd.Run() == nil
}

func (pg *managedPostgres) initDB() error {
	pg.logger.Info("initializing postgres", zap.String("dataDir", pg.dataDir))
	if err := os.MkdirAll(pg.dataDir, 0o700); err != nil {
		return err
	}
	initdb := exec.Command(filepath.Join(pg.binDir, "initdb"), "-D", pg.dataDir, "--encoding=UTF8")
	initdb.Stdout = os.Stdout
	initdb.Stderr = os.Stderr
	if err := initdb.Run(); err != nil {
		return fmt.Errorf("initdb failed: %w", err)
	}

	// Configure for trust auth (local connections only) and performance.
	confPath := filepath.Join(pg.dataDir, "postgresql.conf")
	conf, err := os.ReadFile(confPath)
	if err != nil {
		return fmt.Errorf("read postgresql.conf: %w", err)
	}
	extras := fmt.Sprintf(`
# genesis-writer overrides
port = %s
shared_buffers = '256MB'
maintenance_work_mem = '1GB'
wal_level = minimal
max_wal_senders = 0
fsync = off
synchronous_commit = off
full_page_writes = off
checkpoint_completion_target = 0.9
max_wal_size = '4GB'
`, pg.port)
	if err := os.WriteFile(confPath, append(conf, []byte(extras)...), 0o600); err != nil {
		return fmt.Errorf("write postgresql.conf: %w", err)
	}

	// Trust all local connections.
	hbaPath := filepath.Join(pg.dataDir, "pg_hba.conf")
	hba, err := os.ReadFile(hbaPath)
	if err != nil {
		return fmt.Errorf("read pg_hba.conf: %w", err)
	}
	hbaStr := strings.ReplaceAll(string(hba), "scram-sha-256", "trust")
	hbaStr = strings.ReplaceAll(hbaStr, "md5", "trust")
	hbaStr = strings.ReplaceAll(hbaStr, "peer", "trust")
	if err := os.WriteFile(hbaPath, []byte(hbaStr), 0o600); err != nil {
		return fmt.Errorf("write pg_hba.conf: %w", err)
	}

	// Pin listen_addresses to localhost since we're using trust auth.
	conf, err = os.ReadFile(confPath)
	if err != nil {
		return fmt.Errorf("read postgresql.conf: %w", err)
	}
	confStr := string(conf)
	if !strings.Contains(confStr, "listen_addresses = 'localhost'") {
		confStr += "\nlisten_addresses = 'localhost'\n"
		if err := os.WriteFile(confPath, []byte(confStr), 0o600); err != nil {
			return fmt.Errorf("write postgresql.conf: %w", err)
		}
	}

	return nil
}

func (pg *managedPostgres) start() error {
	pg.logger.Info("starting postgres", zap.String("dataDir", pg.dataDir), zap.String("port", pg.port))
	logFile := filepath.Join(pg.dataDir, "logfile")
	cmd := pg.pgCtl("start", "-D", pg.dataDir, "-l", logFile, "-o", fmt.Sprintf("-p %s", pg.port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_ctl start: %w", err)
	}
	pg.started = true

	// Wait for ready.
	pgIsReady := filepath.Join(pg.binDir, "pg_isready")
	for i := 0; i < 30; i++ {
		cmd := exec.Command(pgIsReady, "-p", pg.port, "-q")
		if cmd.Run() == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("postgres did not become ready within 30s")
}

// ensureDatabase creates the openaudio database if it doesn't exist.
func (pg *managedPostgres) ensureDatabase() error {
	psql := filepath.Join(pg.binDir, "psql")
	out, err := exec.Command(psql, "-p", pg.port, "-d", "postgres", "-tAc",
		fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", pgDatabase)).Output()
	if err != nil {
		return fmt.Errorf("check database: %w", err)
	}
	if strings.TrimSpace(string(out)) == "1" {
		return nil
	}
	pg.logger.Info("creating database", zap.String("name", pgDatabase))
	cmd := exec.Command(psql, "-p", pg.port, "-d", "postgres", "-c",
		fmt.Sprintf("CREATE DATABASE %s", pgDatabase))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Stop shuts down the managed postgres instance if we started it.
func (pg *managedPostgres) Stop() {
	if !pg.started {
		return
	}
	pg.logger.Info("stopping managed postgres")
	cmd := pg.pgCtl("stop", "-D", pg.dataDir, "-m", "fast")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		pg.logger.Warn("failed to stop postgres", zap.Error(err))
	}
}
