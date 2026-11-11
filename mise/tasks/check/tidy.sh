#!/usr/bin/env bash
#MISE description="Check Go module tidiness"
#MISE wait_for=["fix", "fmt", "generate"]
set -euo pipefail
go mod tidy -diff
