#!/bin/bash
# Go Test Wrapper for VS Code
# This script wraps `go test` to run tests in Docker when appropriate
# 
# To use: Set this as your go test command in VS Code settings:
# "go.alternateTools": {
#   "go": "/path/to/this/script"
# }

# Check if we should run in container
if [ "$USE_CONTAINER" = "true" ] || [ -n "$VSCODE_GO_TEST_IN_CONTAINER" ]; then
    # Find workspace root (where go.mod is)
    WORKSPACE_ROOT=""
    CURRENT_DIR="$PWD"
    
    while [ "$CURRENT_DIR" != "/" ]; do
        if [ -f "$CURRENT_DIR/go.mod" ]; then
            WORKSPACE_ROOT="$CURRENT_DIR"
            break
        fi
        CURRENT_DIR=$(dirname "$CURRENT_DIR")
    done
    
    if [ -z "$WORKSPACE_ROOT" ]; then
        echo "Error: Could not find workspace root (go.mod)" >&2
        exit 1
    fi
    
    # Extract the package path from arguments
    PACKAGE=""
    TEST_NAME=""
    
    for arg in "$@"; do
        # Handle both relative paths and full package paths
        if [[ "$arg" == ./pkg/* ]] || [[ "$arg" == ./cmd/* ]]; then
            PACKAGE="$arg"
        elif [[ "$arg" == github.com/OpenAudio/go-openaudio/* ]]; then
            # Convert full package path to relative path
            PACKAGE="./${arg#github.com/OpenAudio/go-openaudio/}"
        elif [[ "$arg" == -run=* ]] || [[ "$arg" == -run ]]; then
            TEST_NAME="${arg#-run=}"
        fi
    done
    
    # Determine which profile to use
    if [[ "$PACKAGE" == *"mediorum"* ]] || [[ "$*" == *"mediorum"* ]]; then
        PROFILE="mediorum-unittests"
        SERVICE="test-mediorum-unittests"
    elif [[ "$PACKAGE" == *"integration_tests"* ]] || [[ "$*" == *"integration_tests"* ]]; then
        PROFILE="integration-tests"
        SERVICE="test-integration"
    else
        PROFILE="unittests"
        SERVICE="test-unittests"
    fi
    
    # Change to workspace root before running docker compose
    cd "$WORKSPACE_ROOT" || exit 1
    
    # Build docker command - use workspace root for project directory
    CMD=(docker compose --file=dev/docker-compose.yml --project-name=test --project-directory="$WORKSPACE_ROOT" --profile="$PROFILE" run --rm "$SERVICE" go)
    
    # Pass through all arguments
    exec "${CMD[@]}" "$@"
else
    # Run go normally
    exec /usr/bin/env go "$@"
fi
