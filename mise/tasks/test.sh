#!/usr/bin/env bash
#MISE description="Test all code"
#MISE wait_for=["fix", "fmt", "generate"]
#USAGE flag "--coverage" help="Generate a coverage profile"

set -euo pipefail

# shellcheck disable=SC2154
if [ "${usage_coverage:-false}" = "true" ]; then
	go test \
	  -coverprofile=coverage.out \
	  -coverpkg github.com/nais/nais-github-teamsync/... \
	  -v \
	  -race \
	  ./...
else
	go test -cover -race ./...
fi
