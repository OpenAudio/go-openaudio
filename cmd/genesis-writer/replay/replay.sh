#!/usr/bin/env bash
# replay.sh -- replay a genesis chain into an ETL database on the bulk-load
# fast path, then put the database back into serving shape.
#
# Run the ETL on the host, not in the node container: round-trip latency to
# Postgres is the single largest factor (see README.md). This script drives
# the host-native runner in examples/etl and needs go and psql on PATH.
#
#   ./replay.sh run --rpc http://localhost:50051 --db postgres://... --end 5800000
#   ./replay.sh restore --db postgres://...
#
# `run` executes the whole pipeline:
#
#   settings    apply bulk-load-settings.sql; restart Postgres if needed
#   bootstrap   index one block so the ETL migrations create the schema
#   slim        apply drop-serving-indexes.sql and bulk-load-tables.sql
#   replay      index to --end
#   restore     restore-settings.sql, recreate-serving-indexes.sql,
#               VACUUM ANALYZE
#
# Every phase is idempotent, so if the replay dies partway just rerun `run`
# with the same arguments: it resumes from the last indexed block. Nothing is
# restored on failure on purpose -- recreating the indexes on a partially
# loaded database is expensive and a resume would only drop them again. Run
# `replay.sh restore` to give up and put the database back into serving shape.
#
# Flags for `run`:
#   --rpc URL          core RPC endpoint (fallback: $ETL_RPC_URL)
#   --db URL           Postgres URL, must be allowed ALTER SYSTEM (fallback: $ETL_DB_URL)
#   --end HEIGHT       final block height of the replay (required)
#   --restart-cmd CMD  how to restart Postgres when shared_buffers changes,
#                      e.g. 'docker restart etl-pg' or 'pg_ctl -D ... restart'.
#                      Without it the script stops and asks you to restart.
#   --stream           pass --stream to the ETL runner (gRPC block stream)
#   --insecure         pass --insecure to the ETL runner (self-signed TLS)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'replay.sh: %s\n' "$*" >&2; exit 1; }

psql_run() { psql "$DB_URL" -X -q -v ON_ERROR_STOP=1 "$@"; }

usage() { sed -n '2,36p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }

DB_URL="${ETL_DB_URL:-}"
RPC_URL="${ETL_RPC_URL:-}"
END_BLOCK=""
RESTART_CMD=""
ETL_FLAGS=()

cmd="${1:-}"
shift || true
case "$cmd" in run|restore) ;; *) usage ;; esac

while [ $# -gt 0 ]; do
  case "$1" in
    --rpc)         RPC_URL="$2"; shift 2 ;;
    --db)          DB_URL="$2"; shift 2 ;;
    --end)         END_BLOCK="$2"; shift 2 ;;
    --restart-cmd) RESTART_CMD="$2"; shift 2 ;;
    --stream)      ETL_FLAGS+=(--stream); shift ;;
    --insecure)    ETL_FLAGS+=(--insecure); shift ;;
    *)             usage ;;
  esac
done

[ -n "$DB_URL" ] || die "--db (or ETL_DB_URL) is required"

run_etl() {
  local end="$1"
  (cd "$REPO_ROOT" && go run ./examples/etl \
    --rpc "$RPC_URL" --db "$DB_URL" --end "$end" \
    ${ETL_FLAGS+"${ETL_FLAGS[@]}"})
}

phase_settings() {
  log "applying bulk-load settings"
  psql_run -f "$SCRIPT_DIR/bulk-load-settings.sql"
  local pending
  pending=$(psql_run -Atc "select count(*) from pg_settings where pending_restart")
  if [ "$pending" -gt 0 ]; then
    if [ -z "$RESTART_CMD" ]; then
      die "shared_buffers changed and needs a restart. Restart Postgres and rerun, or pass --restart-cmd."
    fi
    log "restarting Postgres: $RESTART_CMD"
    eval "$RESTART_CMD"
    for _ in $(seq 1 60); do
      psql_run -Atc "select 1" >/dev/null 2>&1 && break
      sleep 2
    done
    psql_run -Atc "select 1" >/dev/null || die "Postgres did not come back after restart"
  fi
}

phase_bootstrap() {
  log "bootstrap: indexing one block so the ETL migrations create the schema"
  run_etl 1
}

phase_slim() {
  log "slim: dropping serving indexes, disabling autovacuum on hot tables"
  psql_run -f "$SCRIPT_DIR/drop-serving-indexes.sql"
  psql_run -f "$SCRIPT_DIR/bulk-load-tables.sql"
}

phase_replay() {
  log "replay: indexing to block $END_BLOCK"
  run_etl "$END_BLOCK"
}

phase_restore() {
  log "restore: settings, indexes, VACUUM ANALYZE"
  psql_run -f "$SCRIPT_DIR/restore-settings.sql"
  psql_run -f "$SCRIPT_DIR/recreate-serving-indexes.sql"
  psql_run -c "VACUUM ANALYZE"
  log "database is back in serving shape"
}

if [ "$cmd" = restore ]; then
  phase_restore
  exit 0
fi

[ -n "$RPC_URL" ]   || die "--rpc (or ETL_RPC_URL) is required for run"
[ -n "$END_BLOCK" ] || die "--end is required for run"

phase_settings
phase_bootstrap
phase_slim
phase_replay
phase_restore
