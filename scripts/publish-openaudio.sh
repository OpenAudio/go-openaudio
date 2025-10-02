#!/bin/bash

set -eo pipefail

COMMIT_SHA="${CIRCLE_SHA1:-$(git rev-parse HEAD)}"

binaries=(bin/audiusd-{amd64,arm64})
for bin in "${binaries[@]}"; do
    if ! [ -f "$bin" ]; then
        echo "Binary $bin not found. Please build binaries first."
        exit 1
    fi
done

echo "## Docker Images" > .release_notes.md
echo "- \`audius/audiusd:${COMMIT_SHA}\` (multi-arch)" >> .release_notes.md
echo "- \`audius/audiusd:${COMMIT_SHA}-amd64\` (amd64)" >> .release_notes.md
echo "- \`audius/audiusd:${COMMIT_SHA}-arm64\` (arm64)" >> .release_notes.md
echo "" >> .release_notes.md

gh release create "audiusd@${COMMIT_SHA}" \
    --title "audiusd ${COMMIT_SHA}" \
    --notes-file .release_notes.md \
    bin/audiusd-amd64 \
    bin/audiusd-arm64

rm .release_notes.md
