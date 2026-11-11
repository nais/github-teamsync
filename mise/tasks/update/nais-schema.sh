#!/usr/bin/env bash
#MISE description="Fetch the Nais GraphQL schema using the Nais CLI"
set -euo pipefail

schema="$(mktemp "${TMPDIR:-/tmp}/nais-schema.XXXXXXXX")"
trap 'rm -f "$schema"' EXIT
nais api schema --confirm-login=false > "$schema"
if [[ ! -s "$schema" ]]; then
  echo "The Nais CLI returned an empty schema" >&2
  exit 1
fi
mv "$schema" internal/naisapi/schema.graphql
