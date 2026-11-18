#!/usr/bin/env bash
#MISE description="Upgrade GitHub actions to their latest versions"
set -euo pipefail

ratchet upgrade .github/workflows/*.yaml
