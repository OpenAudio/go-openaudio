#!/bin/bash

# Dispatch subcommands to their own entrypoints
if [ "$1" = "rollback" ]; then
    shift
    exec /bin/rollback-entrypoint.sh "$@"
fi

NETWORK="${NETWORK:-prod}"
ENV_FILE="/env/${NETWORK}.env"

if [ ! -f "$ENV_FILE" ]; then
    echo "Error: Network environment file not found at $ENV_FILE"
    exit 1
fi

source_env_file() {
    local file=$1
    if [ ! -f "$file" ]; then
        echo "WARN Environment file $file not found"
        return 0
    fi

    echo "Loading environment from $file"
    while IFS='=' read -r key value || [ -n "$key" ]; do
        [[ "$key" =~ ^#.*$ ]] && continue
        [[ -z "$key" ]] && continue
        val="${value%\"}"
        val="${val#\"}"
        [ -z "${!key}" ] && export "$key"="$val"
    done < "$file"
}

source_env_file "$ENV_FILE"

if [ -d "/data/creator-node-db-15" ] && [ "$(ls -A /data/creator-node-db-15)" ]; then
    # use existing db
    POSTGRES_DB="audius_creator_node"
    POSTGRES_DATA_DIR="/data/creator-node-db-15"
else
    POSTGRES_DB="${POSTGRES_DB:-openaudio}"
    POSTGRES_DATA_DIR="${POSTGRES_DATA_DIR:-/data/postgres}"
fi

POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
dbUrl="${dbUrl:-postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:5432/${POSTGRES_DB}?sslmode=disable}"

export POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DATA_DIR dbUrl uptimeDataDir audius_core_root_dir

setup_postgres_tuning() {
    # Apply OpenAudio's baked Postgres defaults and any operator
    # overrides from OPENAUDIO_PG_* env vars. Postgres reads conf.d
    # entries in alphabetical order; later wins. The layout is:
    #
    #   conf.d/00-openaudio-defaults.conf  baked-in (this image)
    #   conf.d/50-openaudio-env.conf       OPENAUDIO_PG_* env overrides
    #   conf.d/99-*.conf                   operator drop-in slot
    #   postgresql.auto.conf               ALTER SYSTEM (always last)
    #
    # The 00- and 50- files are re-rendered every container start, so
    # image upgrades and .env changes both take effect on next restart.
    local data_dir="$1"
    local conf_d="${data_dir}/conf.d"
    local pg_conf="${data_dir}/postgresql.conf"
    local defaults_src="/etc/openaudio/postgres-defaults.conf"

    mkdir -p "$conf_d" || return 0

    if [ -f "$defaults_src" ]; then
        cp "$defaults_src" "$conf_d/00-openaudio-defaults.conf"
    fi

    local env_out="$conf_d/50-openaudio-env.conf"
    if [ -n "${OPENAUDIO_PG_SHARED_BUFFERS:-}${OPENAUDIO_PG_WORK_MEM:-}${OPENAUDIO_PG_MAINTENANCE_WORK_MEM:-}${OPENAUDIO_PG_EFFECTIVE_CACHE_SIZE:-}${OPENAUDIO_PG_WAL_BUFFERS:-}${OPENAUDIO_PG_MAX_WAL_SIZE:-}${OPENAUDIO_PG_MIN_WAL_SIZE:-}" ]; then
        {
            echo "# Auto-generated from OPENAUDIO_PG_* environment variables."
            echo "# Edit by setting the matching env var in your .env, then restart."
            [ -n "${OPENAUDIO_PG_SHARED_BUFFERS:-}" ]       && echo "shared_buffers = ${OPENAUDIO_PG_SHARED_BUFFERS}"
            [ -n "${OPENAUDIO_PG_WORK_MEM:-}" ]             && echo "work_mem = ${OPENAUDIO_PG_WORK_MEM}"
            [ -n "${OPENAUDIO_PG_MAINTENANCE_WORK_MEM:-}" ] && echo "maintenance_work_mem = ${OPENAUDIO_PG_MAINTENANCE_WORK_MEM}"
            [ -n "${OPENAUDIO_PG_EFFECTIVE_CACHE_SIZE:-}" ] && echo "effective_cache_size = ${OPENAUDIO_PG_EFFECTIVE_CACHE_SIZE}"
            [ -n "${OPENAUDIO_PG_WAL_BUFFERS:-}" ]          && echo "wal_buffers = ${OPENAUDIO_PG_WAL_BUFFERS}"
            [ -n "${OPENAUDIO_PG_MAX_WAL_SIZE:-}" ]         && echo "max_wal_size = ${OPENAUDIO_PG_MAX_WAL_SIZE}"
            [ -n "${OPENAUDIO_PG_MIN_WAL_SIZE:-}" ]         && echo "min_wal_size = ${OPENAUDIO_PG_MIN_WAL_SIZE}"
        } > "$env_out"
    else
        rm -f "$env_out"
    fi

    chown -R postgres:postgres "$conf_d" 2>/dev/null || true

    # Wire postgresql.conf to read conf.d. Idempotent; runs on first
    # init and on existing data dirs that pre-date this feature.
    if [ -f "$pg_conf" ] && ! grep -qE "^include_dir = 'conf.d'" "$pg_conf"; then
        {
            echo ""
            echo "# Added by OpenAudio entrypoint; reads conf.d/*.conf in name order."
            echo "include_dir = 'conf.d'"
        } >> "$pg_conf"
    fi
}

setup_postgres() {
    PG_BIN="/usr/lib/postgresql/15/bin"
    mkdir -p /data
    mkdir -p "$POSTGRES_DATA_DIR"
    chown -R postgres:postgres /data
    chown -R postgres:postgres "$POSTGRES_DATA_DIR"
    chmod -R 700 "$POSTGRES_DATA_DIR"

    # Ensure locale environment variables are set for PostgreSQL
    export LANG=en_US.UTF-8
    export LC_ALL=en_US.UTF-8
    export LC_CTYPE=en_US.UTF-8

    # Initialize if needed
    if [ -z "$(ls -A $POSTGRES_DATA_DIR)" ] || ! [ -f "$POSTGRES_DATA_DIR/PG_VERSION" ]; then
        echo "Initializing PostgreSQL data directory at $POSTGRES_DATA_DIR..."
        # Initialize with explicit UTF-8 encoding
        su - postgres -c "LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 $PG_BIN/initdb -D $POSTGRES_DATA_DIR --encoding=UTF8 --locale=en_US.UTF-8"
        
        # Configure authentication and logging
        sed -i "s/peer/trust/g; s/md5/trust/g" "$POSTGRES_DATA_DIR/pg_hba.conf"
        sed -i "s|#log_destination = 'stderr'|log_destination = 'stderr'|; \
                s|#logging_collector = on|logging_collector = off|" \
                "$POSTGRES_DATA_DIR/postgresql.conf"

        if [ "${OPENAUDIO_PGALL:-false}" = "true" ]; then
            # WARNING: use only with `-p "127.0.0.1:5432:5432"`
            echo "WARNING: OPENAUDIO_PGALL is set to true, this will allow all connections from any host"
            echo "host all all 0.0.0.0/0 trust" >> "$POSTGRES_DATA_DIR/pg_hba.conf"
            sed -i "s|#listen_addresses = 'localhost'|listen_addresses = '*'|" "$POSTGRES_DATA_DIR/postgresql.conf"
        fi

        # Only set up database and user on fresh initialization
        echo "Setting up PostgreSQL user and database..."
        # Start PostgreSQL temporarily to create user and database
        su - postgres -c "$PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR start"
        until su - postgres -c "$PG_BIN/pg_isready -q"; do
            sleep 1
        done
        
        su - postgres -c "psql -c \"ALTER USER ${POSTGRES_USER} WITH PASSWORD '${POSTGRES_PASSWORD}';\""
        su - postgres -c "psql -tc \"SELECT 1 FROM pg_database WHERE datname = '${POSTGRES_DB}'\" | grep -q 1 || \
                         psql -c \"CREATE DATABASE ${POSTGRES_DB};\""
        
        # Stop PostgreSQL to restart it properly
        su - postgres -c "$PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR stop"
    fi

    setup_postgres_tuning "$POSTGRES_DATA_DIR"

    echo "Starting PostgreSQL service..."
    # Ensure locale is set when starting PostgreSQL
    su - postgres -c "LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 $PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR start"
    until su - postgres -c "$PG_BIN/pg_isready -q"; do
        echo "Waiting for PostgreSQL to start..."
        sleep 2
    done
}

if [ "${OPENAUDIO_CORE_ONLY:-false}" = "true" ]; then
    echo "Running in core only mode, skipping PostgreSQL setup..."
    echo "Starting openaudio..."
    exec /bin/openaudio "$@"
elif [ "${OPENAUDIO_TEST_HARNESS_MODE:-false}" = "true" ]; then
    setup_postgres
    echo "Starting openaudio in test mode..."
    for sql_file in /app/openaudio/.initdb/*.sql; do
        if [ -f "$sql_file" ]; then
            echo "Executing $sql_file..."
            su - postgres -c "psql -f $sql_file"
        fi
    done
    echo "executing command:" "$@"
    exec "$@"
else
    setup_postgres
    if [ "${OPENAUDIO_CI:-false}" != "true" ] && [ "${OPENAUDIO_HOT_RELOAD:-false}" = "true" ]; then
        echo "Starting openaudio with hot reload (wgo)..."
        cd /app || exit 1
        # Use wgo to watch .go and .templ files, exclude generated files
        exec wgo -file=.go -file=.templ -xfile=_templ.go -xfile=.pb.go -xfile=.sql.go go run ./cmd/openaudio "$@"
    else
        echo "Starting openaudio..."
        exec /bin/openaudio "$@"
    fi
fi
