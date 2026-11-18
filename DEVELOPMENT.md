# Development

For deployment and runtime configuration, see the [operations guide](docs/OPERATIONS.md).
The service has not been deployed; do not add migration code or compatibility aliases.

## Local development

Install [mise](https://mise.jdx.dev/) and run `mise install` for the pinned tools.
Install the [Nais CLI](https://cli.nais.io) and start `nais api proxy` in another
terminal. Use a test GitHub organization and your own GitHub App. Set the required
GitHub environment variables, point `NAIS_API_URL` at `http://localhost:4242/graphql`,
leave `NAIS_SERVICE_ACCOUNT_TOKEN_PATH` unset, and run `mise run local` with
`DRY_RUN=true`. The proxy supplies Nais authentication; never copy workload tokens
into local configuration or test data.

Mise can load local dotenv settings, which override matching shell variables. The
service itself only reads the process environment. To override a dotenv value for
one run, use `mise exec -- env DRY_RUN=true go run .`. Keep credentials out of task
output. Without dry-run, the service can change real memberships and delete teams.

For container testing, pass `GO_VERSION` from `mise current go` at build time.
Use a read-only key mount readable by the non-root process (UID 65532); never bake
credentials into the image. On Linux, host networking can reach the loopback proxy.

## Change workflow

Work on a descriptive branch and preserve unrelated changes. Read the implementation
and tests before changing behavior, and add regression tests. Use Mise to normalize
Go changes and run the checks:

```sh
mise run fix ::: fmt ::: generate
mise run check ::: build ::: test
```

`mise run test` runs offline tests with coverage and the race detector; add
`--coverage` to save a report. `mise run check:golangci-lint` runs lint on its own.
`fmt:ci` compares normalized tracked content against a clean, committed tree, so
review working-tree and generated changes directly while editing. Run
`git diff --check`, report any checks you could not run, and do not commit or push
without permission.

- Use standard Go tests and `go-cmp` for structured diffs. Use mocks, `httptest`,
  and fake transports; cover pagination, authentication, and failures without live
  services or credentials.
- Propagate cancellation with `t.Context()` in tests. Bound waits and use virtual
  time for scheduler tests instead of arbitrary sleeps.
- Keep operations idempotent and dependencies narrow. Use structured `slog` logging
  and never log credentials or raw upstream errors that may contain them.

## Safety invariants

- **Nais is the sole source of desired state.** Use read-only GraphQL, not runtime
  mutations, Pub/Sub, or shared lifecycle management. Reopen the workload-token
  file for every request and page; never fall back to cached or static credentials.
  Only loopback HTTP proxy endpoints may omit authentication. Reject redirects.
- **Require complete reads before writing.** Errors and partial inventories must
  not become empty results. Finish direct membership pagination before changing
  memberships. Use Nais slugs only, verify direct parent ownership, protect the
  reserved parent, and recheck ownership before each orphan deletion.
- **Keep GitHub installations separate.** Organization operations use the
  organization installation. Enterprise SSO reads use a separate installation of
  the same App. Never fall back between providers or installations; an inaccessible
  provider or failed query must not lead to membership removals.
- **Dry-run still reads and validates.** Preview all GitHub team and membership
  writes without sending them. Keep simulated state local to one reconciliation.
- **Webhooks request fresh reads, not writes.** Validate signature, source,
  timestamp, and size. Queue only the team slug, then fetch its current state and
  complete membership. A missing lookup never implies deletion.
- **Bound concurrency and keep phases separate.** Limit waiting teams separately
  from active workers. Never run one team concurrently; coalesce events during work
  into one follow-up and do not deduplicate rejected deliveries. A due poll stops
  new targeted work and waits for active jobs. Cleanup is serial, never overlaps
  reconciliation, and requires all full-sync teams to succeed and a fresh complete
  inventory. Shutdown cancels active work without draining the queue.

## Generation and dependencies

Run `mise run generate` after changing Nais queries or mocked interfaces. It uses
genqlient for GraphQL and Mockery for GitHub mocks; do not edit generated output.
Keep missing GraphQL objects distinguishable from empty results.

To refresh the schema, authenticate the Nais CLI, run `mise run update:nais-schema`,
then regenerate and rerun checks. Refresh through `nais api schema`, not the Nais
API repository. Do not edit the schema snapshot by hand. Normal generation and
tests use the snapshot and do not require a live API.

Use `mise run update:tools` for Go tools, `mise run actions:update` to refresh
pinned Actions within their declared tags, and `mise run actions:upgrade` to
upgrade Actions. Review pins and compatibility, tidy modules through Mise,
regenerate, and rerun checks.

## CI and releases

Use `branch-protection-checkpoint` as the required check for the checks, race tests,
and binary/container builds. This repository publishes images, not deployments.

Use Conventional Commit titles when squash-merging. The pinned git-cliff tool
uses `feat:`, `fix:`, and breaking-change markers to determine the next version,
starting at `v0.1.0`. Unconventional commits are omitted. Preview notes locally
without credentials:

```sh
mise exec -- git-cliff --offline --github-repo nais/nais-github-teamsync --bump --unreleased --strip all
```

CI previews notes in the job summary and updates an existing preview comment on
same-repository PRs. Fork and Dependabot PRs use the summary only. PRs and tag
pushes do not publish. Pushes to `main` and manual runs of **Check, build and
publish** release the next version only after successful checks and builds, and
skip publishing if there are no unreleased Conventional Commits.

Publishing uses the Nais platform action and workload identity to publish and sign
the versioned image and generate its SBOM. After the image is published, CI creates
the GitHub release and `vMAJOR.MINOR.PATCH` tag at the checked commit. The image tag
includes the leading `v`; the embedded binary version does not.

Keep workflow permissions read-only by default: `id-token: write` only for
publishing, `contents: write` only for release creation, and
`pull-requests: write` only for the release-info job.
