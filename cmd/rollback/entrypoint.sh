#!/bin/bash
set -e

# Rollback entrypoint: starts PG, runs rollback, stops PG.
# Designed to run inside the openaudio container with /data mounted.

NETWORK="${NETWORK:-prod}"
ENV_FILE="/env/${NETWORK}.env"

source_env_file() {
    local file=$1
    [ ! -f "$file" ] && return 0
    while IFS='=' read -r key value || [ -n "$key" ]; do
        [[ "$key" =~ ^#.*$ ]] && continue
        [[ -z "$key" ]] && continue
        val="${value%\"}"
        val="${val#\"}"
        [ -z "${!key}" ] && export "$key"="$val"
    done < "$file"
}

[ -f "$ENV_FILE" ] && source_env_file "$ENV_FILE"

# Determine postgres settings (same logic as main entrypoint)
if [ -d "/data/creator-node-db-15" ] && [ "$(ls -A /data/creator-node-db-15)" ]; then
    POSTGRES_DB="audius_creator_node"
    POSTGRES_DATA_DIR="/data/creator-node-db-15"
else
    POSTGRES_DB="${POSTGRES_DB:-openaudio}"
    POSTGRES_DATA_DIR="${POSTGRES_DATA_DIR:-/data/postgres}"
fi

POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
dbUrl="postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:5432/${POSTGRES_DB}?sslmode=disable"

PG_BIN="/usr/lib/postgresql/15/bin"

# Find CometBFT data directory (auto-discover chain ID)
COMET_DATA=""
for dir in /data/core/*/data; do
    if [ -d "$dir" ]; then
        COMET_DATA="$dir"
        break
    fi
done

if [ -z "$COMET_DATA" ]; then
    echo "ERROR: Could not find CometBFT data directory under /data/core/*/data"
    exit 1
fi

echo "CometBFT data: $COMET_DATA"
echo "Postgres DB:    $POSTGRES_DB"
echo "Postgres data:  $POSTGRES_DATA_DIR"
echo ""

# Start postgres
echo "Starting PostgreSQL..."
export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8
su - postgres -c "LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 $PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR start" 2>&1
until su - postgres -c "$PG_BIN/pg_isready -q" 2>/dev/null; do
    sleep 1
done
echo "PostgreSQL ready."
echo ""

# Run rollback
/bin/rollback -comet-data "$COMET_DATA" -pg "$dbUrl" "$@"
EXIT_CODE=$?

# Stop postgres
echo ""
echo "Stopping PostgreSQL..."
su - postgres -c "$PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR stop" 2>&1

exit $EXIT_CODE
