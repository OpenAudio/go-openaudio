package dbpool

import (
	"database/sql"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPGXMaxConns        = int32(8)
	defaultPGXMaxConnIdleTime = 5 * time.Minute
	defaultSQLMaxOpenConns    = 20
	defaultSQLMaxIdleConns    = 5
	defaultSQLConnMaxIdleTime = 5 * time.Minute
	envPGXMaxConns            = "OPENAUDIO_PGX_POOL_MAX_CONNS"
	envPGXMaxConnIdleTime     = "OPENAUDIO_PGX_POOL_MAX_CONN_IDLE_TIME"
	envSQLMaxOpenConns        = "OPENAUDIO_GORM_MAX_OPEN_CONNS"
	envSQLMaxIdleConns        = "OPENAUDIO_GORM_MAX_IDLE_CONNS"
	envSQLConnMaxIdleTime     = "OPENAUDIO_GORM_CONN_MAX_IDLE_TIME"
)

// ConfigurePGX applies conservative defaults to long-lived application pools.
// Explicit env vars win, then explicit pgxpool DSN params, then defaults.
func ConfigurePGX(config *pgxpool.Config, dsn string) {
	if maxConns, ok := lookupPositiveInt(defaultPGXMaxConns, envPGXMaxConns); ok {
		config.MaxConns = int32(maxConns)
	} else if !dsnHasPoolParam(dsn, "pool_max_conns") {
		config.MaxConns = defaultPGXMaxConns
	}

	if idleTime, ok := lookupDuration(defaultPGXMaxConnIdleTime, envPGXMaxConnIdleTime); ok {
		config.MaxConnIdleTime = idleTime
	} else if !dsnHasPoolParam(dsn, "pool_max_conn_idle_time") {
		config.MaxConnIdleTime = defaultPGXMaxConnIdleTime
	}
}

// ConfigureSQL applies conservative defaults to database/sql pools.
func ConfigureSQL(db *sql.DB) {
	maxOpen := env.GetInt(defaultSQLMaxOpenConns, envSQLMaxOpenConns)
	if maxOpen < 1 {
		maxOpen = defaultSQLMaxOpenConns
	}

	maxIdle := env.GetInt(defaultSQLMaxIdleConns, envSQLMaxIdleConns)
	if maxIdle < 1 {
		maxIdle = defaultSQLMaxIdleConns
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(env.GetDuration(defaultSQLConnMaxIdleTime, envSQLConnMaxIdleTime))
}

func lookupPositiveInt(defaultValue int32, keys ...string) (int, bool) {
	if val, ok := env.Lookup(keys...); ok {
		i, err := strconv.Atoi(val)
		if err != nil || i < 1 {
			return int(defaultValue), true
		}
		return i, true
	}
	return 0, false
}

func lookupDuration(defaultValue time.Duration, keys ...string) (time.Duration, bool) {
	if val, ok := env.Lookup(keys...); ok {
		d, err := time.ParseDuration(val)
		if err != nil {
			return defaultValue, true
		}
		return d, true
	}
	return 0, false
}

func dsnHasPoolParam(dsn, param string) bool {
	if u, err := url.Parse(dsn); err == nil && u.Query().Has(param) {
		return true
	}

	return strings.Contains(dsn, param+"=")
}
