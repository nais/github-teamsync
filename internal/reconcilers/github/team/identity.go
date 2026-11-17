package github_team_reconciler

import (
	"context"
	"fmt"

	"github.com/nais/nais-github-teamsync/internal/config"
	"github.com/shurcooL/githubv4"
)

// getGitHubUsernameFromEmail resolves an SSO username/NameID, not a public profile email.
func (r *githubTeamReconciler) getGitHubUsernameFromEmail(ctx context.Context, email string) (*string, error) {
	variables := map[string]any{"username": githubv4.String(email)}
	var provider *ExternalIdentityProvider
	switch r.identityProvider {
	case config.IdentityProviderOrganizationSAML:
		var query LookupGitHubSamlUserByEmail
		variables["org"] = githubv4.String(r.org)
		if err := r.identityClient.Query(ctx, &query, variables); err != nil {
			return nil, err
		}
		provider = query.Organization.SamlIdentityProvider
	case config.IdentityProviderEnterpriseSAML:
		var query LookupEnterpriseSAMLUserByEmail
		variables["enterprise"] = githubv4.String(r.enterprise)
		if err := r.identityClient.Query(ctx, &query, variables); err != nil {
			return nil, err
		}
		provider = query.Enterprise.OwnerInfo.SamlIdentityProvider
	case config.IdentityProviderEnterpriseOIDC:
		var query LookupEnterpriseOIDCUserByEmail
		variables["enterprise"] = githubv4.String(r.enterprise)
		if err := r.identityClient.Query(ctx, &query, variables); err != nil {
			return nil, err
		}
		provider = query.Enterprise.OwnerInfo.OIDCProvider
	default:
		return nil, fmt.Errorf("unsupported identity provider; refusing membership changes")
	}
	if provider == nil {
		return nil, fmt.Errorf("no accessible %s identity provider for organization %q (enterprise %q); refusing membership changes", r.identityProvider, r.org, r.enterprise)
	}
	if provider.ExternalIdentities == nil || provider.ExternalIdentities.Nodes == nil {
		return nil, fmt.Errorf("incomplete %s external identity response; refusing membership changes", r.identityProvider)
	}
	nodes := provider.ExternalIdentities.Nodes
	if len(nodes) == 0 || nodes[0].User.Login == "" {
		return nil, errGitHubUserNotFound
	}
	username := string(nodes[0].User.Login)
	return &username, nil
}
