#!/usr/bin/env bash
#MISE description="Ensure all code is fixed, formatted, and generated"
#MISE depends=["fix", "fmt", "generate"]

set -euo pipefail

if ! git diff --exit-code --name-only; then
  echo 'Tracked files need fixing, formatting, or regeneration. Run mise run fix, mise run fmt, and mise run generate.' >&2
  exit 1
fi
