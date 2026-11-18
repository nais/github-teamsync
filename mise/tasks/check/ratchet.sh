#!/usr/bin/env bash
#MISE description="Verify all GitHub actions are pinned"
set -euo pipefail

ratchet lint .github/workflows/*.yaml
