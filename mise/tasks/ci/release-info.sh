#!/usr/bin/env bash
#MISE description="Preview git-cliff release notes and select the release version"
set -euo pipefail

args=(--offline --github-repo "${GITHUB_REPOSITORY:?}" --strip all)
if [[ "${GITHUB_REF_TYPE:-}" == tag ]]; then
  tag="${GITHUB_REF_NAME:?}"
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Release tags must have the form vMAJOR.MINOR.PATCH.' >&2; exit 1; }
  args+=(--current)
else
  args+=(--bump --unreleased)
  tag="$(git-cliff "${args[@]}" --bumped-version)"
fi

notes="$(git-cliff "${args[@]}")"
if [[ -z "$notes" ]]; then
  echo 'No Conventional Commits to release.' >> "${GITHUB_STEP_SUMMARY:?}"
  exit 0
fi

[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'git-cliff did not produce a valid release version.' >&2; exit 1; }
printf '%s\n' "$notes" >> "${GITHUB_STEP_SUMMARY:?}"
{
  echo "tag=$tag"
  echo "version=${tag#v}"
} >> "${GITHUB_OUTPUT:?}"
