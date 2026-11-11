#!/usr/bin/env bash
#MISE description="Build nais-github-teamsync"
#MISE wait_for=["fix", "fmt", "generate"]

set -euo pipefail

go build -trimpath -o bin/nais-github-teamsync .
