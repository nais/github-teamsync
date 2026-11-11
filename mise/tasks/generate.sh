#!/usr/bin/env bash
#MISE description="Generate Nais GraphQL queries and GitHub client mocks"
#MISE wait_for=["fix", "fmt"]

set -euo pipefail

go tool github.com/Khan/genqlient .configs/genqlient.yaml
go tool mvdan.cc/gofumpt -w internal/naisapi/generated.go
go tool github.com/vektra/mockery/v3 --config ./.configs/mockery.yaml
find internal -type f -name "mock_*.go" -exec go tool mvdan.cc/gofumpt -w {} \;
