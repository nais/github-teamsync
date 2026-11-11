#!/usr/bin/env bash
#MISE description="Run govet"
#MISE wait_for=["fix", "fmt", "generate"]

set -euo pipefail

go vet ./...
