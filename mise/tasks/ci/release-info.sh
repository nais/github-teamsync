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
  preview='No Conventional Commits to release. Use Conventional Commit titles to include changes in the next release.'
else
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'git-cliff did not produce a valid release version.' >&2; exit 1; }
  preview="$notes"
  {
    echo "tag=$tag"
    echo "version=${tag#v}"
  } >> "${GITHUB_OUTPUT:?}"
fi

printf '%s\n' "$preview" >> "${GITHUB_STEP_SUMMARY:?}"

if [[ "${GITHUB_EVENT_NAME:-}" == pull_request && "${IS_FORK:-true}" == false && "${GITHUB_ACTOR:-}" != 'dependabot[bot]' ]]; then
  {
    printf '# Release preview\n\n'
    printf 'Only [Conventional Commits](https://www.conventionalcommits.org/) are included in release notes.\n\n'
    printf '%s\n' "$preview"
  } | gh pr comment "${PR_NUMBER:?}" \
    --repo "$GITHUB_REPOSITORY" \
    --edit-last --create-if-none \
    --body-file -
fi
