#!/bin/bash
set -euo pipefail

# Backup original config
cp ./dev/env/openaudio-3.env ./dev/env/openaudio-3.env.backup
trap 'mv ./dev/env/openaudio-3.env.backup ./dev/env/openaudio-3.env' EXIT

# Add state sync config
sed '/^OPENAUDIO_STATE_SYNC_ENABLE=/d; /^OPENAUDIO_STATE_SYNC_RPC_SERVERS=/d' \
	./dev/env/openaudio-3.env.backup > ./dev/env/openaudio-3.env
echo "OPENAUDIO_STATE_SYNC_ENABLE=true" >> ./dev/env/openaudio-3.env
echo 'OPENAUDIO_STATE_SYNC_RPC_SERVERS=https://node1.oap.devnet,https://node2.oap.devnet' >> ./dev/env/openaudio-3.env

# Stop container
docker compose \
	--file='dev/docker-compose.yml' \
	--project-name='dev' \
	--project-directory='./' \
	--profile=openaudio-dev \
	stop openaudio-3

# Explicitly clear the local dev node's sync markers while its validator is stopped.
# The snapshot restore replaces the remaining core state. Leave media/ETL data alone.
docker compose \
	--file='dev/docker-compose.yml' \
	--project-name='dev' \
	--project-directory='./' \
	--profile=openaudio-dev \
	run --rm --no-deps -T --entrypoint bash openaudio-3 -euc '
		stop_postgres() {
			su - postgres -c "/usr/lib/postgresql/15/bin/pg_ctl -D /data/postgres -m fast -w stop"
		}
		su - postgres -c "/usr/lib/postgresql/15/bin/pg_ctl -D /data/postgres -w start"
		trap stop_postgres EXIT
		su - postgres -c "psql -X -v ON_ERROR_STOP=1 -h /var/run/postgresql -d openaudio -c \"TRUNCATE TABLE public.core_blocks, public.core_app_state\""
	'

# Remove CometBFT stores/WAL, preserving config and priv_validator_state.json.
rm -rf ./tmp/oap3-data/core/*/data/*.db ./tmp/oap3-data/core/*/data/cs.wal

# Recreate container with state sync enabled
docker compose \
	--file='dev/docker-compose.yml' \
	--project-name='dev' \
	--project-directory='./' \
	--profile=openaudio-dev \
	up -d --force-recreate openaudio-3

# animate?
echo "opening console..."
spinner="⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
for i in {1..60}; do
	printf "\b${spinner:$i:1}"
	sleep 0.1
done
echo
open https://node3.oap.devnet/console

echo "State sync started. Original config will be restored on exit."
