#!/bin/bash
# VS Code Test Runner Script for Containerized Tests
# This script allows VS Code's "run test" feature to execute tests in the Docker environment

set -e

# Default values
TEST_PACKAGE="${1:-./pkg/mediorum/server}"
TEST_NAME="${2:-}"

# Determine which profile to use based on the package
if [[ "$TEST_PACKAGE" == *"mediorum"* ]]; then
    PROFILE="mediorum-unittests"
    SERVICE="test-mediorum-unittests"
elif [[ "$TEST_PACKAGE" == *"integration_tests"* ]]; then
    PROFILE="integration-tests"
    SERVICE="test-integration"
else
    PROFILE="unittests"
    SERVICE="test-unittests"
fi

# Build the test command
TEST_CMD="go test -v -count=1 -timeout=60s"

if [ -n "$TEST_NAME" ]; then
    TEST_CMD="$TEST_CMD -run $TEST_NAME"
fi

TEST_CMD="$TEST_CMD $TEST_PACKAGE"

echo "Running tests in container: $TEST_CMD"
echo "Profile: $PROFILE, Service: $SERVICE"

# Ensure docker harness is built
if [ -z "$OPENAUDIO_CI" ]; then
    make docker-harness > /dev/null 2>&1 || true
fi

# Run the test in the container
docker compose \
    --file='dev/docker-compose.yml' \
    --project-name='test' \
    --project-directory='./' \
    --profile="$PROFILE" \
    run --rm "$SERVICE" \
    sh -c "$TEST_CMD"
