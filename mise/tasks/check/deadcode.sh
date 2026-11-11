#!/usr/bin/env bash
#MISE description="Run deadcode"
#MISE wait_for=["fix", "fmt", "generate"]

set -euo pipefail

go tool golang.org/x/tools/cmd/deadcode -test ./...
