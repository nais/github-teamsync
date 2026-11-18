# Nais GitHub team sync

Mirror Nais teams and their direct memberships to one GitHub organization. The
service polls the Nais API for team data and only writes to GitHub. It matches
member emails to GitHub SSO identities and manages closed teams under
`nais-teams` using their Nais slugs.
[Optional signed webhooks](docs/OPERATIONS.md#optional-webhooks) trigger a fresh
sync of one team.

## Deploy on Nais

1. Install your own GitHub App in the organization with **Members: Read and
   write**. For enterprise SAML or OIDC, follow the
   [enterprise SSO setup](docs/OPERATIONS.md#identity-provider-selection).
2. Create a [Nais API service account and workload binding](docs/OPERATIONS.md#nais-api-access)
   with read access to the full team inventory, deletion status, and member emails.
3. Use the [deployment examples](https://github.com/navikt/nais-github-teamsync) and
   select an image from [GitHub releases](https://github.com/nais/nais-github-teamsync/releases).
   Follow the [deployment guide](docs/OPERATIONS.md#manifests-secrets-and-network-access)
   to configure secrets and network access, then validate the manifest.

Reserve all direct children of `nais-teams` for this service. After a successful full sync, cleanup
deletes direct children missing from the complete Nais inventory. An empty
inventory removes them all.

## More information

- [Configuration](docs/OPERATIONS.md#configuration) and [operations](docs/OPERATIONS.md)
- [Development and contributing](DEVELOPMENT.md)
- [MIT license](LICENSE.md)
