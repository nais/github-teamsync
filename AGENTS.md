# Nais GitHub Team Sync

Guidelines for agents working on this standalone Go service.
The service has not been deployed; do not add migration paths, legacy settings,
or backward-compatibility aliases.
Keep `README.md` concise and consumer-focused. Contributor workflows and engineering
practices belong in `DEVELOPMENT.md`; detailed deployment/webhook instructions are
in `docs/OPERATIONS.md`.

## Structure

- Entry point: `main.go` at the project root.
- Startup, HTTP routes, and metrics: `internal/cmd/reconciler/`.
- Environment configuration: `internal/config/`.
- Read-only Nais GraphQL client: `internal/naisapi/`.
  Queries in `queries.graphql` and the checked-in `schema.graphql` generate
  `generated.go` using genqlient. Use `mise run generate`; do not hand-edit generated
  code or the schema snapshot. Refresh the schema with
  `mise run update:nais-schema` (`nais api schema`), not the `nais/api` repository.
- GitHub App organization/enterprise installation authentication: `internal/githubauth/`.
- GitHub reconciliation and ownership checks: `internal/reconcilers/github/team/`.
- Single-organization polling and orphan cleanup: `internal/syncer/`.
- Signed Nais CloudEvents receiver: `internal/webhook/`.
- Consumer-owned Nais manifest/workflow examples: `examples/nais/`; tasks: `mise/tasks/`.

## Commands

```sh
mise install
mise run fix ::: fmt ::: generate
mise run test
mise run test --coverage
mise run check:golangci-lint
mise run check
mise run check ::: build ::: test
```

`fmt:ci` fixes, formats, regenerates, and checks tracked Git differences. It expects
a clean, committed working tree in CI; untracked files have no committed baseline.
Go tools are declared in `go.mod`; Go and golangci-lint are pinned in
`mise/config.toml`. Use mise directly; there is no Makefile.
Mise loads the optional local `.env`; the service reads only process environment
variables and must not load dotenv files at runtime.

## Workflow

- Check the branch and working tree before editing. Use a descriptive task branch
  rather than a default/protected branch, and preserve unrelated changes.
- Do not commit or push unless explicitly requested.
- Use Conventional Commit messages, such as `refactor: use typed envconfig decoding`.
- Read the implementation and its tests before changing behavior.
- After mocked interfaces change, run `mise run generate`. Do not edit generated
  `mock_*.go` files by hand.
- After Go changes, run formatting, race tests, golangci-lint, and relevant checks.
- Before delivery, review generated files and `git status --short`. Report any
  checks that could not be run.

## Safety and behavior

- At runtime, Nais API is the only source of desired state and is accessed
  read-only. Do not introduce runtime Nais mutations, Pub/Sub dependencies, or
  shared lifecycle management. Consumers deploy the image from their own repositories
  and supply their own `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY_FILE`; no
  centrally owned App or static-token authentication is provided. Organization
  operations use the organization App installation. Enterprise SAML/OIDC identity
  reads require `GITHUB_ENTERPRISE` and a separate enterprise installation of the
  same App with read-only enterprise SSO permission. Never use the enterprise
  token for organization operations or fall back between installations/providers.
- Nais API authentication uses a workload binding. Reopen the file identified by
  `NAIS_SERVICE_ACCOUNT_TOKEN_PATH` before every request, including pagination;
  never cache its contents or fall back to static/previous credentials on failure.
  Local development uses `nais api proxy`; credential-free requests are permitted
  only to loopback HTTP endpoints. Never put workload tokens in examples or Secrets.
- Webhooks queue the validated `teamSlug` for a fresh single-team GraphQL state and
  membership read, including member-added/removed events. Never apply payloads as
  direct GitHub mutations or infer deletion from a missing lookup. Preserve
  signature, source, timestamp, and size validation.
- Complete successful inventories are required before inferring deletions. Errors
  and partial responses must never become empty team or membership inventories.
- Use the Nais team slug as the only GitHub team slug; do not query or honor an
  external GitHub team reference. Preserve direct parent ownership checks and
  reserved-parent protection for reconciliation and deletion, including orphan
  cleanup. A matching slug alone does not authorize adoption or deletion.
- Orphan cleanup runs only after successful full syncs, never targeted webhook work.
  `DRY_RUN=true` must perform the
  same reads and safety checks while only logging proposed GitHub team/membership/
  IdP writes. Do not send those writes in dry-run or retain simulated state across
  reconciliations. App token acquisition is still required for authenticated reads.
- Support one required `GITHUB_ORG` per deployment with one scheduler and a bounded
  pool of team workers. Never process the same team concurrently; coalesce events
  during processing into one pending follow-up. Bound waiting work separately from
  active workers and reject overflow without deduplicating rejected deliveries.
  A due full poll must stop new targeted work and wait for active work before its
  inventory. Cleanup must not overlap team reconciliation and requires all full-sync
  teams to succeed. Webhook traffic must not postpone full polls. There is no
  distributed lock; deployments must not overlap on the same organization.
- Preserve SSO lookup safeguards. An inaccessible provider must not trigger
  membership removals.
- Keep operations idempotent, propagate cancellation, and never log credentials.
- Tests must remain independent of live services and credentials. Use mocks,
  `httptest`, and fake transports. Do not mutate live resources without explicit
  authorization.
- Workflow permissions default to read-only; publishing is limited to pushes to
  `main` or manual dispatch and requires successful checks and builds.
  This repository publishes images, not deployments. Consumer deployment workflows
  live under examples.
- Releases use the pinned git-cliff tool, `cliff.toml`, and Conventional Commits.
  Generate release previews offline without credentials; posting PR comments uses
  the workflow token with `pull-requests: write` only in the release-info job.
  Update the existing preview comment; fork and Dependabot PRs use job summaries only.
  Stable tags are `vMAJOR.MINOR.PATCH`; pushes to `main` and manual dispatch use
  git-cliff's next version (initially `v0.1.0`) and skip publishing without unreleased
  Conventional Commits. Publish the versioned image before creating the GitHub
  release and its tag at the checked commit, and confine `contents: write`
  to the release job. Keep release instructions in `DEVELOPMENT.md`.
- Validate customized consumer manifests with `nais apply --dry-run`, selecting
  the target team/environment and supplying an image. Keep secrets out of manifests,
  keep one replica with `Recreate`, and explicitly configure network access. Refer
  to docs.nais.io for Application specification and deployment rules.
