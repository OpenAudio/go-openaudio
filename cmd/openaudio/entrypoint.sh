#!/bin/bash

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

if [ -n "$creatorNodeEndpoint" ]; then
    POSTGRES_DB="audius_creator_node"
    POSTGRES_DATA_DIR="${POSTGRES_DATA_DIR:-/data/creator-node-db-15}"
elif [ -n "$nodeEndpoint" ]; then
    POSTGRES_DB="openaudio"
    POSTGRES_DATA_DIR="${POSTGRES_DATA_DIR:-/data/openaudio-validator-db}"
elif [ -n "$audius_discprov_url" ]; then
    POSTGRES_DB="audius_discovery"
    POSTGRES_DATA_DIR="${POSTGRES_DATA_DIR:-/data/discovery-provider-db}"
else
    POSTGRES_DB="${POSTGRES_DB:-openaudio}"
    POSTGRES_DATA_DIR="${POSTGRES_DATA_DIR:-/data/postgres}"
fi

POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
dbUrl="${dbUrl:-postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:5432/${POSTGRES_DB}?sslmode=disable}"
uptimeDataDir="${uptimeDataDir:-/data/bolt}"
audius_core_root_dir="${audius_core_root_dir:-/data/bolt}"

export POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD POSTGRES_DATA_DIR dbUrl uptimeDataDir audius_core_root_dir

setup_postgres() {
    PG_BIN="/usr/lib/postgresql/15/bin"
    mkdir -p /data
    mkdir -p "$POSTGRES_DATA_DIR"
    chown -R postgres:postgres /data
    chown -R postgres:postgres "$POSTGRES_DATA_DIR"
    chmod -R 700 "$POSTGRES_DATA_DIR"

    # Initialize if needed
    if [ -z "$(ls -A $POSTGRES_DATA_DIR)" ] || ! [ -f "$POSTGRES_DATA_DIR/PG_VERSION" ]; then
        echo "Initializing PostgreSQL data directory at $POSTGRES_DATA_DIR..."
        su - postgres -c "$PG_BIN/initdb -D $POSTGRES_DATA_DIR"
        
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
    echo "Starting PostgreSQL service..."
    su - postgres -c "$PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR start"
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
    echo "Starting openaudio..."
    exec /bin/openaudio "$@"
fi
