#!/usr/bin/env bash
#MISE description="Run govulncheck"
#MISE wait_for=["fix", "fmt", "generate"]

set -euo pipefail

go tool golang.org/x/vuln/cmd/govulncheck ./...
