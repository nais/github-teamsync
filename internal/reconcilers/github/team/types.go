package github_team_reconciler

import (
	"context"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
	"github.com/shurcooL/githubv4"
)

type GraphClient interface {
	Query(ctx context.Context, q any, variables map[string]any) error
}

type TeamsService interface {
	ListChildTeamsByParentSlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error)
	AddTeamMembershipBySlug(ctx context.Context, org, slug, user string, opts github.AddTeamMembershipRequest) (*github.Membership, *github.Response, error)
	CreateTeam(ctx context.Context, org string, team github.CreateTeamRequest) (*github.Team, *github.Response, error)
	GetTeamBySlug(ctx context.Context, org, slug string) (*github.Team, *github.Response, error)
	UpdateTeamBySlug(ctx context.Context, org, slug string, team github.UpdateTeamRequest) (*github.Team, *github.Response, error)
	ListTeamMembersBySlug(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error)
	RemoveTeamMembershipBySlug(ctx context.Context, org, slug, user string) (*github.Response, error)
	DeleteTeamBySlug(ctx context.Context, org, slug string) (*github.Response, error)
}

// githubTeamReconciler creates teams on GitHub and connects users to them.
type githubTeamReconciler struct {
	teamsService     TeamsService
	graphClient      GraphClient
	org              string
	identityProvider config.IdentityProvider
	enterprise       string
	dryRun           bool
}

type GitHubUser struct {
	Login githubv4.String
}

type ExternalIdentity struct {
	User GitHubUser
}

type ExternalIdentityProvider struct {
	ExternalIdentities *ExternalIdentityConnection `graphql:"externalIdentities(first: 1, userName: $username)"`
}

type ExternalIdentityConnection struct {
	Nodes []ExternalIdentity
}

type LookupGitHubSamlUserByEmail struct {
	Organization struct {
		SamlIdentityProvider *ExternalIdentityProvider
	} `graphql:"organization(login: $org)"`
}

type LookupEnterpriseSAMLUserByEmail struct {
	Enterprise struct {
		OwnerInfo struct {
			SamlIdentityProvider *ExternalIdentityProvider
		}
	} `graphql:"enterprise(slug: $enterprise)"`
}

type LookupEnterpriseOIDCUserByEmail struct {
	Enterprise struct {
		OwnerInfo struct {
			OIDCProvider *ExternalIdentityProvider `graphql:"oidcProvider"`
		}
	} `graphql:"enterprise(slug: $enterprise)"`
}
