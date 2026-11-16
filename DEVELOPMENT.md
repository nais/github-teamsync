# Development

This guide is for contributors and maintainers. For installation and runtime
configuration, see [README.md](README.md) and the [operations guide](docs/OPERATIONS.md).
The service has not been deployed; do not add migration paths or compatibility aliases.

## Tooling and repository layout

Install [mise](https://mise.jdx.dev/) and run:

```sh
mise install
```

Use Mise tasks directly; there is no Makefile. Tool versions belong in
[`mise/config.toml`](mise/config.toml) and the `tool` block in [`go.mod`](go.mod),
not in instructions for developers to install independently.

| Location | Responsibility |
| --- | --- |
| `main.go` | Process entry point; configuration comes from environment variables. |
| `internal/cmd/reconciler/` | Startup, HTTP routes, metrics, and shutdown. |
| `internal/config/` | Environment configuration and validation. |
| `internal/naisapi/` | Read-only Nais GraphQL client, queries, and schema snapshot. |
| `internal/githubauth/` | GitHub App organization/enterprise installation discovery and token refresh. |
| `internal/reconcilers/github/team/` | Team ownership checks, reconciliation, and dry-run write previews. |
| `internal/syncer/` | Full polls, a bounded/coalescing team queue, worker pools, and serial orphan cleanup. |
| `internal/webhook/` | Signed Nais CloudEvents receiver. |
| `internal/logger/` | Structured logging with `log/slog`. |
| `examples/nais/` | Consumer-owned deployment examples, not this project's deployment. |
| `mise/tasks/` | Build, generation, test, check, and update tasks. |

## Local development

Use a test GitHub organization and your own GitHub App. Install the
[Nais CLI](https://cli.nais.io), then start the authenticated API proxy in a
separate terminal:

```sh
nais api proxy
```

Configure and run the service:

```sh
cp .env.example .env
# Set your GitHub organization, App ID, and private-key file.
# Keep NAIS_API_URL=http://localhost:4242/graphql and DRY_RUN=true.
mise run local
```

Mise loads the optional `.env` for tasks and `mise exec` and overrides matching
shell environment variables. Values are not redacted from task output; avoid
printing credentials or sharing logs containing secrets. The service itself does
not read dotenv files. To override a dotenv value for one execution, set it after
Mise has constructed the environment:

```sh
mise exec -- env DRY_RUN=true go run .
```

Leave `NAIS_SERVICE_ACCOUNT_TOKEN_PATH` unset when using the loopback HTTP proxy;
the proxy supplies authentication. Remote or HTTPS endpoints require a workload
token file. Never copy a workload token into `.env`, a Secret, or test data.

`DRY_RUN=true` reads real upstream state and logs proposed writes at INFO without
changing GitHub teams, memberships, or IdP mappings. It still obtains temporary
GitHub App installation tokens. Setting `DRY_RUN=false` or leaving it unset
**changes real resources, including deleting orphans**. Do not run live mutation
tests against shared or production organizations.

## Change workflow

1. Check the branch and `git status --short` before editing. Work on a descriptive
   task branch and preserve unrelated staged, unstaged, and untracked work.
2. Read the implementation and its tests. Keep changes focused and consistent with
   surrounding code; add regression tests for changed behavior.
3. Regenerate affected code, format, and run checks:

   ```sh
   mise run fix ::: fmt ::: generate
   mise run test
   mise run check:golangci-lint
   mise run check ::: build ::: test
   ```

4. Review generated changes and `git diff --check`, then inspect the final working
   tree. Report checks that could not run. Do not commit or push on someone else's
   behalf without permission.

Mise orders `fix`, `fmt`, and `generate` before dependent checks when scheduled
together. `fmt:ci` runs normalization and rejects tracked Git differences: it is a
**clean, committed-tree CI check**, not an assertion that an in-progress working
tree is formatted. Untracked files have no committed baseline, so review their
generated content explicitly. Do not stage or discard work just to satisfy it.

## Testing and coding practices

- Use standard Go checks with `testing.T`; use `go-cmp` for complex structs and
  readable diffs. Use `slices`/`maps` helpers where appropriate. Do not add
  assertion-library calls. Testify is used by generated mocks.
- Tests must run without live services or credentials. Use `httptest`, fake
  transports, and mocks. Exercise wire formats, pagination, authentication, and
  error handling rather than only testing successful reconciliation.
- Use `t.Context()` and propagate cancellation. Bound tests that wait for work;
  do not rely on arbitrary sleeps. Use concurrency-safe state in HTTP handlers.
- Use structured `slog` attributes and contextual logging. Never log credentials,
  authorization headers, private keys, workload tokens, or raw upstream errors
  that could contain sensitive data.
- Keep operations idempotent and dependencies narrowly scoped. Avoid introducing
  abstraction layers or configuration options that are not needed by this service.
- Configuration is environment-only, with one required GitHub organization and
  GitHub App ID/private-key file. Do not add dotenv loading to the binary.

Useful focused checks:

| Command | Purpose |
| --- | --- |
| `mise run test` | Offline tests with coverage and the race detector. |
| `mise run test --coverage` | Also write `coverage.out`. |
| `mise run check:govet` | Go vet. |
| `mise run check:staticcheck` | Static analysis. |
| `mise run check:deadcode` | Unreachable-code analysis, including tests. |
| `mise run check:golangci-lint` | Configured Go lint checks. |
| `mise run check:gosec` | Security analysis. |
| `mise run check:govulncheck` | Dependency vulnerability analysis. |
| `mise run check:tidy` | Verify module tidiness. |
| `mise run check:ratchet` | Verify pinned GitHub Actions. |
| `mise run build` | Build `bin/nais-github-teamsync`. |

Tests are offline; installing tools, downloading modules, refreshing the schema,
and querying vulnerability databases can require network access.

## Reconciliation flow

`internal/syncer/` separates scheduling from execution:

| File | Responsibility |
| --- | --- |
| `queue.go` | A mutex-protected circular FIFO, pending set, and processing set; capped at 1024 distinct waiting slugs, separate from active claims. |
| `scheduler.go` | Alternate full cycles and targeted worker-pool phases, joining active work at every phase boundary. |
| `syncer.go` | Connect operations to the scheduler and log failures without stopping later work. |
| `team.go` | Fresh targeted lookup and reconciliation or API-confirmed deletion. |
| `full.go` | Complete inventory, bounded parallel per-team reconciliation, cycle metrics, and cleanup eligibility. |
| `cleanup.go` | Managed GitHub inventory, refreshed Nais inventory, and serial orphan deletion. |

The queue's buffered channel is only a wake-up notification; the FIFO stores work.
Taking a slug moves it from pending to processing, so an event during processing
can queue one follow-up. Workers skip blocked follow-ups without reordering them
or blocking unrelated teams. Every successful take requires one completion call,
including after failure or cancellation; completion releases the claim and wakes
a worker if a follow-up is waiting. Duplicate delivery IDs are handled separately
by the webhook receiver, and rejected deliveries are not remembered as accepted.

`SYNC_WORKERS` (default 5, positive integers only) bounds concurrent team operations
in both phases; `1` retains serial behavior. Targeted workers pull ready slugs from
the shared queue. Full-cycle workers pull inventory indexes from an unbuffered job
channel; each owns its result slot so errors can be joined in inventory order.

The next poll becomes due `SYNC_INTERVAL` after a full sync finishes, including a
failed cycle. Targeted workers check cancellation and that deadline before taking
the next team. When due, the scheduler stops new targeted work and joins every
active job before fetching the full inventory. It joins every full-cycle job before
cleanup; targeted workers resume only after the entire full cycle returns. The two
phases never overlap. Webhooks can still enqueue during either phase. Targeted work
never resets the deadline; active work is not preempted by a due poll.

`Sync` owns `SYNC_TIMEOUT` across inventory, reconciliation, and cleanup. Each team
and orphan deletion gets `TEAM_TIMEOUT`, bounded by the remaining cycle time.
`SyncTeam` uses one `TEAM_TIMEOUT` for its lookup and reconciliation together.
Any team failure suppresses cleanup for that cycle; an orphan deletion failure
stops further cleanup. Timeouts/cancellation count as failures even if an operation
returns no error after its context expires. Shutdown cancels active work without
draining or persisting queued slugs, and waits for active workers to return. There
is no immediate retry or API-request rate limiter; worker count limits concurrent
teams, not requests per minute.

Queue/scheduler tests cover ordering, concurrency, and cancellation; scheduler and
execution deadline tests use `testing/synctest` virtual time. Signed-webhook
integration tests exercise the real receiver, queue, and worker with fake upstreams.
`webhook_pool_integration_test.go` also uses the real Nais client against a local
GraphQL test server, checking concurrent fresh state/membership reads, same-team
follow-ups, and events queued during full reconciliation and cleanup.

## Safety invariants

These are correctness requirements, not optional optimizations:

- **Nais API is the only desired-state source and is read-only.** Use GraphQL
  queries, not runtime mutations, the internal gRPC client, Pub/Sub, or shared
  lifecycle management. Keep generated response types behind the Nais client.
- **Reopen the workload-token file before every request, including every page.**
  Never cache credentials or fall back to previous/static credentials. Only
  loopback HTTP proxy endpoints may omit authentication. Reject redirects.
- **Fail closed on incomplete inventories.** Errors, malformed responses, missing
  connections, and partial GraphQL data must not become empty inventories. Require
  successful membership pagination before membership changes. Refresh the full
  Nais inventory before pruning, and skip pruning after reconciliation failures.
- **Use the Nais slug as the GitHub team slug.** Do not query or honor external
  GitHub team references. A matching slug must not adopt or delete an unrelated
  team: verify direct parent ownership and protect the reserved `nais-teams` parent.
  Nested descendants are not orphans. Recheck ownership before each orphan deletion.
- **Preserve SSO safeguards.** An inaccessible provider or failed identity query
  must not cause membership removals. Enterprise SAML/OIDC identity queries use
  a separate enterprise App installation client; all organization reads/writes
  keep using the organization installation client. Both installations use the
  same App ID/key. Never fall back between providers or installations.
- **Dry-run must not forward resource writes.** All team, membership, and IdP
  writes must pass through the preview boundary. Keep its interface explicit so
  adding a new method requires a forwarding decision. Simulated state must stay
  local to a reconciliation, without mutating remote snapshots or persisting
  between polls. Test missing teams, deletion, ownership, SAML failure, and cancellation.
- **Webhooks select teams for fresh reads.** Use the validated `teamSlug` only to
  queue a targeted GraphQL lookup and a complete membership read. Member-added,
  member-removed, and other supported events never directly supply desired state
  or deletion commands. A failed/missing lookup must not infer deletion or trigger
  organization-wide pruning. Preserve HMAC, source, timestamp, and size validation.
- **Keep team concurrency bounded and phases separate.** Never process the same
  team concurrently. Coalesce pending work by team, retain follow-up events arriving
  during processing, and bound waiting work separately from active workers. Reject
  overflow without remembering the rejected delivery as accepted. A due poll must
  stop new targeted work and wait for active jobs. Cleanup is serial, never overlaps
  reconciliation, and requires every full-sync team to succeed. There is no
  distributed lock; deployments must not overlap on an organization.

## Generated code and schema updates

[`mise run generate`](mise/tasks/generate.sh) validates Nais queries with
**genqlient**, generates typed operations/responses, regenerates GitHub mocks with
**Mockery**, and formats the output.

- Edit `internal/naisapi/queries.graphql` and `.configs/genqlient.yaml`, then
  regenerate `internal/naisapi/generated.go`.
- Edit GitHub client interfaces and `.configs/mockery.yaml`, then regenerate
  `mock_*.go`. Do not modify generated files by hand.
- Keep nullable objects distinguishable from empty objects; generated types must
  not weaken the incomplete-inventory safeguards.
- Normal generation and schema-validation tests use checked-in files and do not
  contact a live Nais API.

To refresh the schema, install the Nais CLI and authenticate it against the Nais
API you are targeting, then run:

```sh
mise run update:nais-schema
mise run generate
mise run check ::: build ::: test
```

The refresh task captures `nais api schema` output and replaces the snapshot only
after a successful, non-empty fetch. It does not download the `nais/api` repository.
Review `internal/naisapi/schema.graphql` and generated Go changes together; do not
hand-edit the snapshot to make a query pass. Only schema refresh needs CLI access
to Nais API; normal generation and tests continue using the checked-in snapshot.

## Dependency maintenance

```sh
mise run update:tools
mise run actions:update        # refresh pins within declared version tags
mise run actions:upgrade       # upgrade Actions to their latest versions
```

Review dependency changes and upstream API compatibility; do not assume an update
command succeeding proves correctness. Resolve version constraints explicitly,
run `go mod tidy` through Mise, regenerate, and rerun checks. Update non-Go tool
pins in `mise/config.toml` deliberately. Keep Action references pinned and workflow
permissions read-only except where publishing requires more.

## Containers, CI, and publishing

For a local container test, build and run with a test App and host API proxy:

```sh
docker build --build-arg GO_VERSION="$(mise current go)" -t nais-github-teamsync:local .
# Linux host networking makes the host's loopback proxy reachable.
docker run --rm --network host --env-file .env \
  -e LISTEN_ADDRESS=127.0.0.1:3105 -e DRY_RUN=true \
  -v /path/to/app.pem:/run/secrets/github-app.pem:ro \
  nais-github-teamsync:local
```

Ensure the mount matches `GITHUB_APP_PRIVATE_KEY_FILE` and is readable by UID 65532.
The image uses distroless/base and runs as non-root; do not bake credentials into it.
The Dockerfile requires `GO_VERSION`, sourced from `mise/config.toml` via
`mise current go` for local and CI builds.

[The main workflow](.github/workflows/main.yaml) runs checks, race tests, and
binary/container builds. `branch-protection-checkpoint` requires all matrix checks
and builds to succeed, including failed/skipped prerequisites; use it as the
required branch-protection check.

### Releases

Releases use [git-cliff](https://git-cliff.org/), pinned in `mise/config.toml`, with
[`cliff.toml`](cliff.toml). Use Conventional Commit titles when squash-merging PRs:
`feat:` adds features, `fix:` fixes bugs, and `!` or a `BREAKING CHANGE:` footer marks
breaking changes. Other conventional types are included under miscellaneous changes;
unconventional messages are omitted. Git-cliff determines the next semantic version,
starting at `v0.1.0` when no version tags exist.

Every workflow run previews release notes in its job summary. Same-repository PRs
also get a release-preview comment, updated on subsequent pushes. When no
Conventional Commits remain, the comment explains that there is nothing to release.
Fork and Dependabot PRs use the job summary only, without attempting comment writes.
To preview locally:

```sh
mise exec -- git-cliff --offline --github-repo nais/nais-github-teamsync --bump --unreleased --strip all
```

Publishing runs only for stable version tags (`vMAJOR.MINOR.PATCH`) or manual
dispatch after checks/builds. To release the next git-cliff version, dispatch
**Check, build and publish** on `main`. Alternatively, push a stable version tag
on the commit to release; that version is used as-is. Manual dispatch with no
unreleased Conventional Commits does not publish. Ordinary pushes to `main` and
PRs only check, build, and preview; they do not publish or create tags.

The publisher repository must set `NAIS_TEAM`, optionally `NAIS_TENANT` (default
`nav`), and authorize the repository in Nais Console. `nais/docker-build-push`
creates the signed image and SBOM in the publisher's Nais registry; the workflow
then mirrors the image to `ghcr.io/<owner>/<repository>`. Do not claim mirroring
also copies signature/SBOM artifacts. Registry build auth uses OIDC; GHCR publishing
uses the workflow's `GITHUB_TOKEN`, not runtime App credentials.

The GHCR image tag and embedded binary version both omit the leading `v`. After
image publishing succeeds, the workflow creates a GitHub release with git-cliff
notes and the image reference. For manual dispatch, this also creates the version
tag at the checked commit. The workflow token's tag creation does not trigger a
second publishing run. No changelog file is committed. Release previews and notes
are generated offline and need no GitHub API credentials. PR comment updates and
release creation use the workflow token.

Keep `id-token: write` and `packages: write` confined to publishing, and
`contents: write` confined to GitHub release creation. Grant `pull-requests: write`
only to the release-info job for preview comments. This project
publishes images, not deployments. Consumer rollout workflows remain under
`examples/nais/`; changes to manifests should be checked with `nais apply --dry-run`
using consumer-specific values, without applying live resources.
