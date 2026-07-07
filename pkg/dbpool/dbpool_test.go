package dbpool

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

func TestConfigurePGXUsesConservativeDefaults(t *testing.T) {
	restoreEnv(t)

	dsn := "postgres://postgres:postgres@localhost:5432/audius_creator_node?sslmode=disable"
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}

	ConfigurePGX(config, dsn)

	if config.MaxConns != defaultPGXMaxConns {
		t.Fatalf("MaxConns = %d, want %d", config.MaxConns, defaultPGXMaxConns)
	}
	if config.MaxConnIdleTime != defaultPGXMaxConnIdleTime {
		t.Fatalf("MaxConnIdleTime = %s, want %s", config.MaxConnIdleTime, defaultPGXMaxConnIdleTime)
	}
}

func TestConfigurePGXRespectsDSNPoolParams(t *testing.T) {
	restoreEnv(t)

	dsn := "postgres://postgres:postgres@localhost:5432/audius_creator_node?sslmode=disable&pool_max_conns=12&pool_max_conn_idle_time=2m"
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}

	ConfigurePGX(config, dsn)

	if config.MaxConns != 12 {
		t.Fatalf("MaxConns = %d, want 12", config.MaxConns)
	}
	if config.MaxConnIdleTime != 2*time.Minute {
		t.Fatalf("MaxConnIdleTime = %s, want 2m", config.MaxConnIdleTime)
	}
}

func TestConfigurePGXEnvOverridesDSNPoolParams(t *testing.T) {
	restoreEnv(t)
	t.Setenv(envPGXMaxConns, "6")
	t.Setenv(envPGXMaxConnIdleTime, "30s")

	dsn := "postgres://postgres:postgres@localhost:5432/audius_creator_node?sslmode=disable&pool_max_conns=12&pool_max_conn_idle_time=2m"
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}

	ConfigurePGX(config, dsn)

	if config.MaxConns != 6 {
		t.Fatalf("MaxConns = %d, want 6", config.MaxConns)
	}
	if config.MaxConnIdleTime != 30*time.Second {
		t.Fatalf("MaxConnIdleTime = %s, want 30s", config.MaxConnIdleTime)
	}
}

func TestConfigureSQLCapsMaxOpenConns(t *testing.T) {
	restoreEnv(t)
	t.Setenv(envSQLMaxOpenConns, "13")

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ConfigureSQL(db)

	if got := db.Stats().MaxOpenConnections; got != 13 {
		t.Fatalf("MaxOpenConnections = %d, want 13", got)
	}
}

func restoreEnv(t *testing.T) {
	t.Helper()

	keys := []string{
		envPGXMaxConns,
		envPGXMaxConnIdleTime,
		envSQLMaxOpenConns,
		envSQLMaxIdleConns,
		envSQLConnMaxIdleTime,
	}

	original := make(map[string]*string, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			v := value
			original[key] = &v
		}
		os.Unsetenv(key)
	}

	t.Cleanup(func() {
		for _, key := range keys {
			if value := original[key]; value != nil {
				os.Setenv(key, *value)
			} else {
				os.Unsetenv(key)
			}
		}
	})
}
