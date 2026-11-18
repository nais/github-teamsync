# Deployment and operations

## Deployment ownership and images

Deploy the published image from your own repository using your own GitHub App.
Use one replica with `Recreate`, disable autoscaling, and never overlap deployments
for the same organization: there is no distributed lock.

Choose a pullable image from [GitHub releases](https://github.com/nais/nais-github-teamsync/releases),
preferably pinned by digest. If your environment requires a team registry, follow
the [third-party image guide](https://docs.nais.io/workloads/how-to/upload-image/).

## Nais API access

Create a read-only service account that can list **all** relevant teams and read
their purpose, deletion status, and member emails. In Nais Console, add a workload
binding for the application's team and environment. If the workload does not yet
exist, deploy with dry-run enabled and add the binding afterward. Failed polls
retry; restart for an immediate sync.

Nais injects a rotating token and `NAIS_SERVICE_ACCOUNT_TOKEN_PATH`. Do not override
it or copy tokens into Secrets. The client rereads the token on each request and
page. Verify that the account sees the full inventory before enabling writes;
a permission-filtered inventory can cause unwanted deletion.

## GitHub authentication and SSO access

Install your App in `GITHUB_ORG` with **Organization permissions → Members: Read
and write**. No repository permissions are needed. Set `GITHUB_APP_ID` and
`GITHUB_APP_PRIVATE_KEY_FILE`. The service discovers the installation and refreshes
tokens automatically. Restart after rotating the private key. Permission changes
may require approval from an organization owner.

### Identity provider selection

`GITHUB_IDENTITY_PROVIDER` defaults to `organization-saml`, which uses the
organization installation. For `enterprise-saml` or `enterprise-oidc`:

1. Grant the same App **Enterprise permissions → Enterprise single sign-on:
   Read-only**. No SCIM or enterprise write permissions are needed.
2. Install the App on both the enterprise and the target organization. Enterprise
   ownership of the App is not an installation.
3. Set `GITHUB_ENTERPRISE` to the enterprise slug. Leave it unset in organization mode.

Enterprise identity reads use a separate installation token; team and membership
operations always use the organization token. There is no fallback. Missing,
suspended, or mismatched installations fail startup. See GitHub's
[enterprise installation guide](https://docs.github.com/en/enterprise-cloud@latest/apps/using-github-apps/installing-a-github-app-on-your-enterprise).

SSO usernames or NameIDs must match Nais member emails. GitHub account and public
profile emails are not used. An inaccessible provider or failed query stops that
team's membership changes. A user with no linked identity is skipped with a
warning, and their existing direct membership can be removed if the GitHub login
is not in the resolved desired set. Test identity resolution before enabling writes.

### External IdP team sync

Use one membership authority per team. This service neither reads nor disconnects
IdP group mappings, and does not skip linked teams or merge memberships. GitHub may
reject writes to linked teams, or the two syncers may undo each other's changes.
Dry-run cannot verify write acceptance. Disconnect external mappings before handing
over membership, or keep external teams outside `nais-teams` with non-conflicting
slugs. An IdP link does not exempt a managed team from deletion.

## Manifests, secrets, and network access

1. Start with the [consumer deployment examples](https://github.com/navikt/nais-github-teamsync).
   Set your team, namespace, and image. If using environment mixins, avoid duplicate
   environment variables and mounts: lists concatenate rather than replace.
2. Supply environment settings through a Nais Secret using `envFrom` and start
   with `DRY_RUN=true`. Mount the PEM key read-only with `filesFrom`, make it
   readable by the non-root process, and set `GITHUB_APP_PRIVATE_KEY_FILE` to match.
   Keep credentials out of checked-in manifests.
3. Allow outbound HTTPS to `api.github.com` and the Nais API host. For webhooks,
   allow inbound traffic from the Nais API workload and ensure its outbound policy
   permits delivery. Configure health probes and Prometheus scraping; keep those
   endpoints internal.
4. Validate with `nais apply --dry-run`, selecting your team and environment and
   supplying the image. Replace `--dry-run` with `--wait` to deploy. The CLI flag
   previews deployment; `DRY_RUN=true` previews GitHub writes at runtime.

For GitHub Actions deployments, authorize your consumer repository in Nais Console.
That repository owns approvals and rollout. See the Nais guides for
[Applications](https://docs.nais.io/workloads/application/reference/spec/),
[secrets](https://docs.nais.io/services/secrets/how-to/workload/), and
[access policies](https://docs.nais.io/workloads/how-to/access-policies/).

## Configuration

Configuration comes from environment variables, not command-line flags.

| Variable | Default | Purpose |
| --- | --- | --- |
| `NAIS_API_URL` | Required | Nais GraphQL endpoint. |
| `NAIS_SERVICE_ACCOUNT_TOKEN_PATH` | Injected by Nais | Rotating workload-token file; do not set it in the manifest. |
| `GITHUB_ORG` | Required | Single target organization. |
| `GITHUB_IDENTITY_PROVIDER` | `organization-saml` | `organization-saml`, `enterprise-saml`, or `enterprise-oidc`. |
| `GITHUB_ENTERPRISE` | Unset | Enterprise slug; required for enterprise identity lookup. |
| `GITHUB_APP_ID` | Required | Positive numeric App ID. |
| `GITHUB_APP_PRIVATE_KEY_FILE` | Required | Readable PEM private-key file. |
| `DRY_RUN` | `false` | Log proposed GitHub writes without applying them. |
| `LISTEN_ADDRESS` | `127.0.0.1:3105` | Use `0.0.0.0:3105` in a container. |
| `SYNC_INTERVAL` | `30m` | Delay from full-sync completion to the next poll. |
| `SYNC_TIMEOUT` | `30m` | Full-sync deadline, including cleanup. |
| `SYNC_WORKERS` | `5` | Maximum concurrent team syncs; use `1` for serial processing. |
| `TEAM_TIMEOUT` | `2m` | Per-team deadline, including targeted reads; also applies to each orphan deletion. |
| `LOG_FORMAT` | `json` | `json` or `text`. |
| `LOG_LEVEL` | `info` | Slog level, such as `debug`, `info`, `warn`, `error`, or `INFO+2`. |
| `WEBHOOK_SECRET` | Unset | Signing secret; set with `WEBHOOK_SOURCE`. |
| `WEBHOOK_SOURCE` | Unset | Exact expected CloudEvents source. |

## Dry-run, scheduling, and deletion safety

Dry-run performs the same reads and ownership checks, obtains App installation
tokens, and logs proposed writes at INFO with `dry_run=true`. It sends no team or
membership writes and does not retain simulated state between reconciliations.

Only direct memberships are reconciled, after complete pagination. An inherited-only
Nais member gets direct membership; removing a direct membership does not revoke
inherited access. Incomplete reads stop membership changes.

A full poll runs at startup. The next is due `SYNC_INTERVAL` after the previous
cycle finishes, even if it failed. When due, the scheduler stops starting targeted
work and waits for active jobs before fetching inventory. Targeted work resumes
after the full cycle, including cleanup. Webhooks can still queue work in the
meantime. Workers limit concurrent teams, not API requests per minute.

**Reserve every direct child of `nais-teams` for this service.** Teams use their
Nais slugs, and an existing team must be a direct child to be reconciled or deleted.
Nais `deletionInProgress` can trigger deletion after an ownership check. Only after
all full-sync teams succeed does cleanup refresh the complete Nais inventory and
delete direct children whose slugs are missing, rechecking ownership before each
deletion. The parent, nested descendants, and teams elsewhere are excluded. A
cleanup failure stops further deletions for that cycle.

An unrelated direct child counts as an orphan, and a genuinely empty Nais inventory
removes all managed direct children. Errors and partial inventories do not count
as empty. Audit ownership and dry-run output before enabling writes, and avoid
concurrent team or hierarchy edits during a sync.

## Optional webhooks

Polling is sufficient without webhooks. A valid event queues only `data.teamSlug`
for a fresh team and membership read. The payload is never desired state or a
deletion command. A missing or incomplete lookup makes no changes; only a
successful full cycle can clean up orphans.

Set `WEBHOOK_SECRET` to the provider's signing secret and `WEBHOOK_SOURCE` to the
exact CloudEvents source (typically `https://<TENANT_DOMAIN>/api`, not the receiver
URL). This integration uses an [experimental Nais API contract](https://github.com/nais/api/tree/b8beefa436dcbf1e0f2cd7d3149d50ae7e728b51/internal/activitylog/webhook);
verify compatibility with the API version you deploy.

As a Nais API admin, register a **global** subscription (omit `teamSlug`) using a
reachable receiver URL ending in `/webhooks/nais` and the shared signing secret.
Use HTTPS outside a trusted private network; HMAC does not encrypt payloads.

```graphql
mutation CreateTeamsyncWebhook($url: String!, $secret: String!) {
  createWebhook(input: {
    url: $url
    secret: $secret
    eventTypes: [
      "TEAM_CREATED", "TEAM_UPDATED", "TEAM_CONFIRM_DELETE_KEY",
      "TEAM_MEMBER_ADDED", "TEAM_MEMBER_REMOVED", "TEAM_MEMBER_SET_ROLE"
    ]
  }) {
    webhook { id url enabled }
  }
}
```

### Delivery and troubleshooting

- Send CloudEvents 1.0 JSON as `application/cloudevents+json`, with
  `X-Webhook-Signature: sha256=<hex HMAC-SHA256 of the exact request body>`.
- The receiver requires an exact source match, a body no larger than 1 MiB, and
  an event time within five minutes of its clock. For team events, `data.teamSlug`
  must match the TEAM resource name.

| Status | Meaning |
| --- | --- |
| `202` | Queued, not reconciled. |
| `204` | Signed ping, duplicate, or unrelated valid event. |
| `400` | Malformed or stale event. |
| `401` | Invalid signature. |
| `403` | Unexpected source. |
| `413` | Oversized body. |
| `415` | Unsupported content type. |
| `503` | Receiver disabled or queue full. Retry after resolving the cause. |

The in-memory queue holds up to 1024 waiting teams, separate from active workers.
Events coalesce by team, with at most one follow-up during active work. A rejected
delivery is not deduplicated; accepted delivery IDs are cached for up to 24 hours
(4096 entries). Full polls recover missed deliveries and work lost on restart.
Monitor delivery history: repeated failures can disable the subscription.

## Monitoring

- `GET /healthz` reports process liveness, not sync success or upstream access.
- `GET /metrics` reports full-sync counts and durations by organization and error.
- Review sync errors and webhook delivery history. Nais owns overall team deletion
  and sync status; the Console reconciler toggle does not control this service.
