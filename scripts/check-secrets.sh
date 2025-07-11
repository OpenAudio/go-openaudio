#!/bin/bash

# scan the staged files
# shellcheck disable=SC2046
git secrets --scan --cached $(git diff --cached --name-only)
