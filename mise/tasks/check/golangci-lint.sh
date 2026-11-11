#!/usr/bin/env bash
#MISE description="Run golangci-lint"
#MISE wait_for=["fix", "fmt", "generate"]
set -euo pipefail

golangci-lint run
