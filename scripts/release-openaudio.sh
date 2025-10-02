#!/bin/bash
# This script should be run from the Makefile only.
make_target="$1"

if [ -n "$(git status -s)" ]; then
    echo "You have uncommitted changes. Commit them first before releasing a docker image."
    exit 1
fi

if ! which -s crane; then
    echo "No crane installation found. Run 'make install-deps' and try again."
    exit 1
fi

case "$make_target" in
    force-release-stage)
        img_tag=prerelease
        ;;
    force-release-foundation)
        img_tag=edge
        ;;
    force-release-sps)
        img_tag=current
        ;;
    *)
        exit 1
        ;;
esac

DOCKER_DEFAULT_PLATFORM=linux/amd64 docker build --push -t audius/audiusd:${img_tag} -f ./cmd/audiusd/Dockerfile ./
