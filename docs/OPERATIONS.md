# Deployment and operations

See [README.md](../README.md) for a quick start and the configuration reference.
This guide covers consumer-owned Nais deployments and optional webhook setup.
Contributor workflows are documented in [DEVELOPMENT.md](../DEVELOPMENT.md).

## Deployment ownership and images

Run the published image from **your own repository**. This project does not deploy
a shared service or provide GitHub App credentials. Use one replica with
`Recreate`, disable autoscaling, and do not overlap deployments on an organization:
there is no distributed lock.

Select a published image accessible to your environment, preferably pinned by
digest, such as `ghcr.io/nais/nais-github-teamsync@sha256:<published-digest>`.
Releases use semantic versions: `v1.2.3` produces image tag `1.2.3`, including
manually dispatched releases. See [GitHub releases](https://github.com/nais/nais-github-teamsync/releases)
for release notes and image references. Package visibility/access must allow your
environment to pull the image.
If your platform requires images in your team's Nais registry, mirror the image
following the [third-party image guide](https://docs.nais.io/workloads/how-to/upload-image/).

## Nais API access

Create a service account that can list **all** relevant teams and read their
purpose, deletion status, and member email addresses. In Nais
Console, open the account, choose **Add workload binding**, and select this
application's team/environment. If the workload does not exist yet, deploy it
first and add the binding afterward. Failed polls retry; restart for an immediate sync.

Nais injects a short-lived token file and `NAIS_SERVICE_ACCOUNT_TOKEN_PATH`. Do not
override the path, mount your own Kubernetes service-account token, or copy token
contents into a Secret. The client reopens the file for every request/page and
never falls back to cached credentials. No write permission or internal gRPC access
is needed.

An incomplete/filtered team inventory is unsafe for cleanup. Verify that the
service account sees the full inventory before setting `DRY_RUN=false`.

## GitHub authentication and SSO access

Create your own App with **Organization permissions → Members: Read and write**,
install it in `GITHUB_ORG`, and keep the installation unsuspended. Repository
permissions are not required. Validate access to team CRUD, memberships, IdP group
connections, and, when selected, organization SAML external identities. Permission
changes may require an organization owner to approve the updated grant.

The service discovers the organization installation and refreshes installation
access tokens automatically. Supply the App ID and a readable PEM private-key
file, not installation IDs or OAuth client secrets. Team, membership, and IdP
group operations always use the organization installation token. The key is read
at startup; restart after rotating it.

### Identity provider selection

Select the provider explicitly with `GITHUB_IDENTITY_PROVIDER`:

| Value | Identity query | Authentication |
| --- | --- | --- |
| `organization-saml` (default) | `organization.samlIdentityProvider.externalIdentities` | Organization App installation token. |
| `enterprise-saml` | `enterprise.ownerInfo.samlIdentityProvider.externalIdentities` | Enterprise App installation token. |
| `enterprise-oidc` | `enterprise.ownerInfo.oidcProvider.externalIdentities` | Enterprise App installation token. |

All modes filter `externalIdentities` by `userName`, using the Nais member email,
and read the linked GitHub login. SSO usernames/NameIDs must match Nais emails;
public profile emails and organization-verified-domain emails are not used.
Users do not need to register their work email as a GitHub account email.

For enterprise SAML/OIDC:

1. Add **Enterprise permissions → Enterprise single sign-on: Read-only** to the
   same App used for organization operations. No enterprise write or SCIM
   permissions are needed for these queries.
2. Install the App on the enterprise **and** the target organization. Registering
   or owning the App at enterprise level is not the same as installing it there.
   An enterprise installation does not grant organization access, and vice versa.
3. Configure the provider and enterprise slug:

   ```sh
   GITHUB_IDENTITY_PROVIDER=enterprise-saml # or enterprise-oidc
   GITHUB_ENTERPRISE=<enterprise-slug>
   ```

Both installations use the same `GITHUB_APP_ID` and
`GITHUB_APP_PRIVATE_KEY_FILE`. The service discovers the enterprise installation
through `/enterprises/{enterprise}/installation`, verifies its enterprise slug,
target type, and suspension status, then obtains and refreshes a separate
installation token for identity queries. Missing, suspended, or mismatched
installations fail startup. Neither client follows API redirects.

`GITHUB_ENTERPRISE` is required for enterprise modes and must be unset in
organization mode. There is no automatic provider detection or credential
fallback. Invalid combinations fail startup. A missing/
inaccessible provider or failed query stops that team's membership changes.
A user with no linked identity is skipped with a warning; their existing GitHub
membership can be removed if that login is not in the
resolved desired set. Test this in a non-production organization.

See [GitHub's enterprise SSO App permissions announcement](https://github.blog/changelog/2025-07-01-enterprise-level-access-for-github-apps-and-installation-automation-apis/#enterprise-sso-and-scim-management)
and [enterprise App installation guide](https://docs.github.com/en/enterprise-cloud@latest/apps/using-github-apps/installing-a-github-app-on-your-enterprise).

## Manifests, secrets, and network access

1. Copy [`examples/nais/app.yaml`](../examples/nais/app.yaml) to `.nais/app.yaml` in
   your repository. Set the namespace/team labels and select an image.
2. Create a Nais Secret named **`nais-github-teamsync`**, exposed through `envFrom`.
   Supply `NAIS_API_URL`, `GITHUB_ORG`, `GITHUB_APP_ID`, and
    `GITHUB_APP_PRIVATE_KEY_FILE`, plus enterprise identity settings when selected.
    Start with `DRY_RUN=true`. Other optional
   configuration can also be Secret keys; omit both webhook keys for polling only.
3. Create **`nais-github-teamsync-github-app`** with a `private-key.pem` key. Copy
   [`app.example.yaml`](../examples/nais/app.example.yaml) to
   `.nais/app.<environment>.yaml` and customize it. Its `filesFrom` entry mounts
   the key read-only at `/run/secrets/github-app/private-key.pem`, readable by the
   non-root process. Set `GITHUB_APP_PRIVATE_KEY_FILE` to that path.
4. Allow outbound HTTPS to `api.github.com` and the host in `NAIS_API_URL`. For
   webhook delivery, allow inbound traffic from the Nais API application and its
   namespace/cluster; its outbound policy must also allow this workload.

Never put private keys, workload tokens, or secret values in checked-in manifests.
The base example provides health probes, internal service, Prometheus scraping,
resource defaults, and UID/GID 65532. It deliberately omits an image and public ingress.

`nais apply --environment <environment>` automatically merges an adjacent mixin.
Maps/scalars are merged but **lists concatenate**: avoid duplicate environment
variables and mounts. Replace all example hosts, teams, and namespaces.

Validate and preview the customized manifest before deploying:

```sh
nais apply .nais/app.yaml --team <team> --environment <environment> \
  --set metadata.namespace=<team> --set metadata.labels.team=<team> \
  --set spec.image=<published-image-reference> --dry-run
```

Replace `--dry-run` with `--wait` to deploy. This Nais CLI flag previews manifest
deployment; the service's **`DRY_RUN=true` environment setting** previews GitHub
operations after the application starts. Keep both distinctions clear.

The first deployment requires a published image. Later configuration-only deploys
may omit the image override to retain the running image if the manifest does not
pin another one.

For manual GitHub Actions deployments, copy
[`examples/nais/deploy.yaml`](../examples/nais/deploy.yaml) into your repository's
`.github/workflows/`, set `NAIS_TEAM`/`NAIS_ENVIRONMENT` repository variables,
authorize that repository in Nais Console, and select an image at dispatch. Your
repository owns approval and rollout rules; this project does not run that workflow.

## Dry-run, scheduling, and deletion safety

`DRY_RUN=true` logs proposed writes at INFO with `dry_run=true`, `operation`, and
the affected team/user. Use `LOG_LEVEL=info` or `debug`. Reads and ownership/SSO
checks still run. The service continues full polling and handling targeted webhooks;
temporary App installation tokens are still obtained. No team, membership, or IdP
mapping writes are sent to GitHub. Simulated missing teams do not persist between
reconciliations. Nais API is read-only in both modes.

The service performs a full sync at startup. When it finishes, successfully or not,
the next full poll becomes due after `SYNC_INTERVAL` (default: 30 minutes). This is
a delay from completion, not a fixed wall-clock schedule. Between polls, validated
webhooks queue only their `teamSlug`. The worker fetches that team's current
GraphQL state and complete membership list before reconciling GitHub. This applies
to member-added/removed/role events as well as team events;
the email, role, or deletion action in the payload is never applied directly.

One scheduler manages up to `SYNC_WORKERS` concurrent team syncs (default: 5;
positive integers only). Set `SYNC_WORKERS=1` for serial processing. The same team
never runs concurrently with itself. This limits concurrent teams, not API requests
per minute; lower it if GitHub rate limiting becomes a problem.

Webhooks do not reset the full-poll deadline or require an empty queue before
polling. When a poll is due, workers stop taking new targeted jobs and finish all
active jobs before the full inventory is fetched. The full sync processes teams
with the same concurrency limit, waits for all results, and runs serial cleanup
only if every team succeeded. Targeted work resumes after the full cycle returns;
webhooks can continue queueing throughout. Cleanup never overlaps reconciliation.

`SYNC_TIMEOUT` bounds the entire full cycle, including cleanup. Each full-sync team
and orphan deletion is also bounded by `TEAM_TIMEOUT`. Targeted work shares one
`TEAM_TIMEOUT` across its state lookup and reconciliation.

A failed team does not block later teams; failed full reconciliation suppresses
orphan cleanup for that cycle. Failed targeted work is retried by a later event or
the periodic poll. Shutdown cancels and waits for active work without draining the
queue. There is no durable queue, distributed lock, or per-team backoff.

GitHub team slugs always match Nais team slugs; external GitHub team references are
not queried or used. Existing teams must be direct children of `nais-teams` before
they can be reconciled or deleted. For a still-present team, `deletionInProgress`
triggers deletion of the GitHub team with the same slug, after verifying ownership.
After every successful full sync, cleanup lists direct children of `nais-teams`,
refreshes the complete Nais inventory, and deletes children whose slugs do not
match a current Nais slug. Ownership is rechecked before each deletion. The reserved
parent, teams elsewhere in the organization, and nested descendants are not cleanup
candidates. A cleanup deletion failure or timeout stops further deletions for that cycle.

**All direct children of `nais-teams` must belong exclusively to this service.**
An unrelated manually created child is considered an orphan. A legitimately empty
Nais inventory removes all managed direct children. Errors and partial inventories
do not become empty inventories. There is no transaction spanning the two APIs:
avoid concurrent team creation, renames, or hierarchy edits during a sync. Audit
ownership and dry-run output before enabling writes.

## Optional webhooks

Polling is sufficient without webhooks. Hooks request a fresh sync of only the
team identified by `data.teamSlug`; they do not scan the organization or prune
orphans. **Payloads never become desired state or direct deletion commands**.
A missing, inaccessible, or partial single-team response makes no changes. If a
team has already disappeared, only a successful full inventory can establish
orphan cleanup; the webhook alone is not evidence that a team should be deleted.

The receiver implements the experimental
[`nais/api` contract at `b8beefa436dcbf1e0f2cd7d3149d50ae7e728b51`](https://github.com/nais/api/tree/b8beefa436dcbf1e0f2cd7d3149d50ae7e728b51/internal/activitylog/webhook).
Check that contract against the API version you deploy.

Set both:

- `WEBHOOK_SECRET`: the signing secret managed by the webhook provider.
- `WEBHOOK_SOURCE`: the exact expected CloudEvents source,
  `https://<TENANT_DOMAIN>/api`. It identifies the event source, not the receiver URL.

Register a **global** subscription as a Nais API admin, omitting `teamSlug`:

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

Use a reachable URL ending in `/webhooks/nais` and the same signing secret as the
receiver. Creation and deletion-confirmation events are global-only in that API
branch. A reachable internal URL is
`http://nais-github-teamsync.<team>/webhooks/nais`. If needed, use the example
ingress restricted to that path; keep health/metrics internal and use HTTPS
outside a trusted private network. HMAC authenticates but does not encrypt payloads.

### Delivery contract and troubleshooting

- CloudEvents 1.0 JSON, `Content-Type: application/cloudevents+json`.
- `X-Webhook-Signature: sha256=<hex HMAC-SHA256 of the exact request body>`.
- Constant-time signature verification, exact source match, 1 MiB body limit,
  and event `time` within five minutes of the receiver's clock. Synchronize clocks.
- Supported types: `io.nais.team.created`, `io.nais.team.updated`,
  `io.nais.team.confirm.delete.key`, `io.nais.team.member.added`,
  `io.nais.team.member.removed`, and `io.nais.team.member.set.role`.
- Relevant events must include a non-empty `data.teamSlug` matching the TEAM
  resource name. Missing or inconsistent team identifiers are rejected.

| Status | Meaning |
| --- | --- |
| `202` | Team accepted into the in-memory queue, not completed GitHub work. |
| `204` | Signed ping, duplicate delivery, or unrelated valid event. |
| `400` | Malformed or stale event. |
| `401` | Invalid signature. |
| `403` | Unexpected source. |
| `413` | Oversized body. |
| `415` | Unsupported content type. |
| `503` | Receiver disabled or the pending-team queue is full; retry delivery. |

A queue holds up to 1024 distinct waiting teams, separate from active workers, and
coalesces events for the same waiting team. Workers take the oldest ready team;
blocked follow-ups do not prevent unrelated teams from running. A new event for a
team currently being processed queues one follow-up read that becomes eligible
after processing finishes. If full, the receiver returns `503` without recording
the rejected delivery ID, so a retry can enqueue it.

Accepted delivery IDs are deduplicated for up to 24 hours with a 4096-entry cache;
eviction/restart can cause another idempotent sync. The experimental API generates
a fresh timestamp per retry while keeping the delivery ID. Startup and periodic
full polling recover from missed hooks and process failures, including queued
work lost on restart.

The API branch emits pending-deletion confirmation, not a final `TEAM_DELETED`
event. Orphan cleanup handles records already gone before processing. Monitor
subscription delivery history: repeated failures can disable the subscription.
Automatic subscription registration, dual-secret rotation, and durable webhook
storage are not implemented.

## Monitoring and references

- `GET /healthz`: process liveness, not proof of upstream access or successful sync.
- `GET /metrics`: polling-cycle counts/durations labeled by organization and error.
- Review sync errors and webhook delivery history; a healthy process or accepted
  webhook does not mean reconciliation succeeded.
- Nais API owns overall team deletion and sync status. Console's reconciler
  enable/disable setting does not control this service.

Nais documentation: [Application specification](https://docs.nais.io/workloads/application/reference/spec/),
[secrets](https://docs.nais.io/services/secrets/how-to/workload/),
[access policies](https://docs.nais.io/workloads/how-to/access-policies/),
[environment mixins](https://docs.nais.io/build/explanations/environment-mixins/),
and [GitHub Actions deployment](https://docs.nais.io/build/how-to/build-and-deploy/).
