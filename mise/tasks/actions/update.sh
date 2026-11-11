#!/usr/bin/env bash
#MISE description="Update GitHub actions within their pinned version tags"
set -euo pipefail

ratchet update .github/workflows/*.yaml examples/nais/deploy.yaml
