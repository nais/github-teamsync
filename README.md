# Nais GitHub team sync

Mirror Nais teams and memberships to **one GitHub organization**. The service
polls Nais GraphQL and uses your GitHub App to manage teams. Signed webhooks can
request a fresh sync of just the affected team. Nais API is read-only.

Run the published image from your own repository with your own manifests and
credentials. This project publishes images; it does not deploy a shared service.

## What it manages

- A closed `nais-teams` parent and closed child teams with descriptions from Nais.
- Memberships resolved from Nais emails through **organization-level GitHub SAML**.
- Disconnection of existing GitHub IdP group mappings.
- API-confirmed team deletions, plus orphan cleanup after successful full syncs.

GitHub team slugs always match Nais team slugs. Existing GitHub teams must be direct
children of `nais-teams`; a matching slug alone never authorizes adoption or deletion.

**Run one instance per organization, with one replica and `Recreate`.** All direct
children of `nais-teams` must belong to this service: orphan cleanup removes
children absent from the complete Nais inventory. An empty inventory removes all
managed children. Failed/partial inventories suppress cleanup.

## Get started on Nais

1. Create a GitHub App with **Organization permissions → Members: Read and write**
   and install it in the target organization. Organization-level SAML identities
   must match Nais member emails; enterprise-level SAML is not supported.
2. Create a Nais API service account with permission to read the **full** team
   inventory, deletion status, and member emails.
3. Copy [`examples/nais/app.yaml`](examples/nais/app.yaml) into your repository as
   `.nais/app.yaml`. Customize the team/namespace, select a published image
   (prefer a digest), and configure the [environment mixin](examples/nais/app.example.yaml)
   for the App key mount and network access to GitHub and your Nais API.
4. Create the runtime Secret `nais-github-teamsync` with:

   ```sh
   NAIS_API_URL=https://console.example.com/graphql
   GITHUB_ORG=your-github-organization
   GITHUB_APP_ID=123456
   GITHUB_APP_PRIVATE_KEY_FILE=/run/secrets/github-app/private-key.pem
   DRY_RUN=true
   ```

   Store `private-key.pem` in a separate `nais-github-teamsync-github-app` Secret
   and mount it read-only as shown in the mixin. Never commit secret values.
5. Preview the customized manifest, then deploy:

   ```sh
   nais apply .nais/app.yaml --team <team> --environment <environment> \
     --set spec.image=<published-image-reference> --dry-run
   ```

   Replace `--dry-run` with `--wait` to deploy. An optional
   [deployment workflow](examples/nais/deploy.yaml) can be copied to your repository.
6. In Nais Console, add a **workload binding** from the API service account to this
   application. Deploy first if needed to select the workload. Nais injects
   `NAIS_SERVICE_ACCOUNT_TOKEN_PATH`; do not override it or copy its token into a
   Secret. The service rereads that file before every API request/page.

Review dry-run logs, SAML mappings, and team ownership before setting
`DRY_RUN=false`. GitHub App installation tokens refresh automatically; restart
after rotating the App's private key.

See the [operations guide](docs/OPERATIONS.md) for deployment details and troubleshooting.

## Configuration

Configuration comes from environment variables, not command-line flags.

| Variable | Default | Purpose |
| --- | --- | --- |
| `NAIS_API_URL` | Required | HTTPS Nais GraphQL endpoint. |
| `NAIS_SERVICE_ACCOUNT_TOKEN_PATH` | Injected by Nais | Rotating workload-token file; do not set it in the manifest. |
| `GITHUB_ORG` | Required | Single target organization. |
| `GITHUB_APP_ID` | Required | Positive numeric App ID. |
| `GITHUB_APP_PRIVATE_KEY_FILE` | Required | Readable PEM private-key file. |
| `DRY_RUN` | `false` | Log proposed GitHub resource writes without applying them. |
| `LISTEN_ADDRESS` | `127.0.0.1:3105` | Use `0.0.0.0:3105` in a container. |
| `SYNC_INTERVAL` | `30m` | Delay between completed full syncs and the next full poll. |
| `SYNC_TIMEOUT` | `30m` | Complete-cycle deadline. |
| `SYNC_WORKERS` | `5` | Maximum concurrent team syncs; use `1` for serial processing. |
| `TEAM_TIMEOUT` | `2m` | Per-team operation deadline, including targeted webhook reads. |
| `LOG_FORMAT` | `json` | `json` or `text`. |
| `LOG_LEVEL` | `info` | Slog level: `debug`, `info`, `warn`, `error`, or an offset such as `INFO+2`. |
| `WEBHOOK_SECRET` | Unset | Signing secret, at least 32 bytes; set with `WEBHOOK_SOURCE`. |
| `WEBHOOK_SOURCE` | Unset | Exact expected CloudEvents source, e.g. `https://<tenant-domain>/api`. |

**Start with `DRY_RUN=true`.** It previews team creation/updates, membership changes,
IdP disconnection, and deletions at INFO with `dry_run=true` and an `operation`
field. Reads and safety checks still run; App authentication still obtains
temporary installation tokens. Unset or `false` enables writes, including cleanup.
This setting is separate from the Nais CLI's manifest-preview `--dry-run` flag.

## Webhooks and monitoring

Polling works without webhooks. To request targeted syncs, configure both webhook
settings and register a global Nais subscription targeting `/webhooks/nais`.
Events are signed and source/time/size-validated. Their `teamSlug` queues a fresh
read of that team's state and memberships, including member-added/removed events.
Startup and periodic full syncs still handle orphan cleanup; missing/failed
single-team lookups never infer deletion.
See [webhook setup](docs/OPERATIONS.md#optional-webhooks) for the experimental API contract.

Keep `/healthz` and `/metrics` internal. Health indicates process liveness, not
successful upstream sync. Metrics expose polling-cycle counts/durations labeled
by organization and error. Review sync logs and webhook delivery history.

An inaccessible SAML provider stops membership changes. A user without a linked
identity is skipped and can lose an existing membership if their login is absent
from the resolved desired set. Validate identity mapping before enabling writes.

## Further reading

- [Deployment, authentication, deletion safety, and webhooks](docs/OPERATIONS.md)
- [Development and contributing](DEVELOPMENT.md)
- [Nais API workload bindings](https://docs.nais.io/operate/console/api/#method-1-workload-binding)
- [Application specification](https://docs.nais.io/workloads/application/reference/spec/)
- [MIT license](LICENSE.md)
