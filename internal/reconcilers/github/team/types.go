package github_team_reconciler

import (
	"context"

	"github.com/google/go-github/v93/github"
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
	CreateOrUpdateIDPGroupConnectionsBySlug(ctx context.Context, org, team string, opts github.IDPGroupList) (*github.IDPGroupList, *github.Response, error)
	DeleteTeamBySlug(ctx context.Context, org, slug string) (*github.Response, error)
}

// githubTeamReconciler creates teams on GitHub and connects users to them.
type githubTeamReconciler struct {
	teamsService TeamsService
	graphClient  GraphClient
	org          string
	dryRun       bool
}

type GitHubUser struct {
	Login githubv4.String
}

type ExternalIdentitySamlAttributes struct {
	Username githubv4.String
}

type ExternalIdentity struct {
	User         GitHubUser
	SamlIdentity ExternalIdentitySamlAttributes
}

type SamlIdentityProvider struct {
	ExternalIdentities struct {
		Nodes []ExternalIdentity
	} `graphql:"externalIdentities(first: 1, userName: $username)"`
}

type LookupGitHubSamlUserByEmail struct {
	Organization struct {
		SamlIdentityProvider *SamlIdentityProvider
	} `graphql:"organization(login: $org)"`
}
