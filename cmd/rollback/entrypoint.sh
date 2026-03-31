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

ROLLBACK_BIN="/bin/rollback-bin"

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

# Verify the rollback binary exists and is executable.
if [ ! -x "$ROLLBACK_BIN" ]; then
    echo "ERROR: Rollback binary not found at $ROLLBACK_BIN"
    echo "       Was the image built with PREBUILT_ROLLBACK_BINARY?"
    exit 1
fi

echo "CometBFT data: $COMET_DATA"
echo "Postgres DB:    $POSTGRES_DB"
echo "Postgres data:  $POSTGRES_DATA_DIR"
echo ""

# Safety check: ensure openaudio is not running.
# Check if postgres is already running (another container / the main node has it).
if "$PG_BIN/pg_isready" -q 2>/dev/null; then
    echo "ERROR: PostgreSQL is already running. Is the openaudio node still up?"
    echo "       Stop the node before running rollback to avoid data corruption."
    exit 1
fi

# Check if the PG postmaster.pid exists (stale or active).
if [ -f "$POSTGRES_DATA_DIR/postmaster.pid" ]; then
    PG_PID=$(head -1 "$POSTGRES_DATA_DIR/postmaster.pid")
    if kill -0 "$PG_PID" 2>/dev/null; then
        echo "ERROR: PostgreSQL process (PID $PG_PID) is still running on $POSTGRES_DATA_DIR."
        echo "       Stop the node before running rollback to avoid data corruption."
        exit 1
    else
        echo "WARNING: Stale postmaster.pid found (PID $PG_PID not running). Removing it."
        rm -f "$POSTGRES_DATA_DIR/postmaster.pid"
    fi
fi

# Check if the openaudio node is responding on localhost.
if command -v curl &>/dev/null; then
    if curl -sf --max-time 3 http://localhost/health-check &>/dev/null || \
       curl -sf --max-time 3 https://localhost/health-check --insecure &>/dev/null; then
        echo "ERROR: openaudio node is responding on localhost."
        echo "       Stop the node before running rollback to avoid data corruption."
        exit 1
    fi
fi

# Check if CometBFT data is locked (another process has it open).
for lockdb in blockstore state; do
    lockfile="$COMET_DATA/${lockdb}.db/LOCK"
    if [ -f "$lockfile" ]; then
        if fuser "$lockfile" 2>/dev/null | grep -q '[0-9]'; then
            echo "ERROR: CometBFT $lockdb database is locked by another process."
            echo "       Stop the node before running rollback to avoid data corruption."
            exit 1
        fi
    fi
done

echo "Pre-flight checks passed: no running node detected."
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
"$ROLLBACK_BIN" -comet-data "$COMET_DATA" -pg "$dbUrl" "$@"
EXIT_CODE=$?

# Stop postgres
echo ""
echo "Stopping PostgreSQL..."
su - postgres -c "$PG_BIN/pg_ctl -D $POSTGRES_DATA_DIR stop" 2>&1

exit $EXIT_CODE
