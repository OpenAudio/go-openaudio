#!/bin/bash
# This script should be run from the Makefile only.
set -eo pipefail

make_target="$1"
arch="$2"
platform="$3"

VERSION_LDFLAG="-X main.Version=$(git rev-parse HEAD)"

CGO_ENABLED=0 GOOS="$platform" GOARCH="$arch" go build -ldflags "$VERSION_LDFLAG" -o "$make_target" ./cmd/openaudio

# Also build the rollback binary alongside
rollback_target="${make_target/openaudio/rollback}"
CGO_ENABLED=0 GOOS="$platform" GOARCH="$arch" go build -o "$rollback_target" ./cmd/rollback
