#!/usr/bin/env bash
# Creates the genesis_writer_source database and seeds it with test data.
# Runs as part of postgres docker-entrypoint-initdb.d (after 01_schema.sql and 02_seed.sql).
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" <<-EOSQL
    CREATE DATABASE genesis_writer_source;
EOSQL

psql -v ON_ERROR_STOP=1 \
    --username "$POSTGRES_USER" \
    --dbname genesis_writer_source \
    -f /docker-entrypoint-initdb.d/01_schema.sql

psql -v ON_ERROR_STOP=1 \
    --username "$POSTGRES_USER" \
    --dbname genesis_writer_source \
    -f /tmp/source_seed.sql
