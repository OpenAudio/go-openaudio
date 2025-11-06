#!/bin/bash

# Backup original config
cp ./dev/env/openaudio-3.env ./dev/env/openaudio-3.env.backup

# Add state sync config
echo "stateSyncEnable=true" >> ./dev/env/openaudio-3.env
echo 'stateSyncRPCServers="https://node1.oap.devnet,https://node2.oap.devnet"' >> ./dev/env/openaudio-3.env

# Stop container
docker compose \
	--file='dev/docker-compose.yml' \
	--project-name='dev' \
	--project-directory='./' \
	--profile=openaudio-dev \
	stop openaudio-3

# Delete core data to trigger state sync
rm -rf ./tmp/oap3-data/core

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

# Restore original config
mv ./dev/env/openaudio-3.env.backup ./dev/env/openaudio-3.env

echo "State sync test complete. Config restored."
