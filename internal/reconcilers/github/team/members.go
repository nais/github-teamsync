package github_team_reconciler

import (
	"context"
	"fmt"

	"github.com/google/go-github/v93/github"
	"github.com/shurcooL/githubv4"
)

type teamMembersQuery struct {
	Organization *struct {
		Team *struct {
			Members *struct {
				Nodes    []*GitHubUser
				PageInfo *struct {
					HasNextPage *bool
					EndCursor   githubv4.String
				}
			} `graphql:"members(first: 100, after: $after, membership: IMMEDIATE)"`
		} `graphql:"team(slug: $slug)"`
	} `graphql:"organization(login: $org)"`
}

func (r *githubTeamReconciler) getTeamMembers(ctx context.Context, slug string) ([]*github.User, error) {
	// A team created only in a dry-run plan has no remote memberships to query.
	if preview, ok := r.teamsService.(*previewTeamsService); ok && preview.created[slug] != nil {
		return nil, ctx.Err()
	}
	variables := map[string]any{
		"org": githubv4.String(r.org), "slug": githubv4.String(slug),
		"after": (*githubv4.String)(nil),
	}
	var allMembers []*github.User
	seenCursors := make(map[githubv4.String]bool)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var query teamMembersQuery
		if err := r.graphClient.Query(ctx, &query, variables); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if query.Organization == nil || query.Organization.Team == nil || query.Organization.Team.Members == nil {
			return nil, fmt.Errorf("incomplete GitHub team membership response")
		}
		members := query.Organization.Team.Members
		if members.Nodes == nil || members.PageInfo == nil || members.PageInfo.HasNextPage == nil {
			return nil, fmt.Errorf("incomplete GitHub team membership response")
		}
		for _, member := range members.Nodes {
			if member == nil || member.Login == "" {
				return nil, fmt.Errorf("GitHub team membership response contains a user without a login")
			}
			allMembers = append(allMembers, &github.User{Login: new(string(member.Login))})
		}
		if !*members.PageInfo.HasNextPage {
			return allMembers, nil
		}
		cursor := members.PageInfo.EndCursor
		if cursor == "" || seenCursors[cursor] {
			return nil, fmt.Errorf("invalid GitHub team membership pagination cursor")
		}
		seenCursors[cursor] = true
		variables["after"] = new(cursor)
	}
}
