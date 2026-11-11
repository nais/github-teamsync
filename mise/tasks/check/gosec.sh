#!/usr/bin/env bash
#MISE description="Run gosec"
#MISE wait_for=["fix", "fmt", "generate"]

set -euo pipefail

go tool github.com/securego/gosec/v2/cmd/gosec --exclude-generated -terse ./...
