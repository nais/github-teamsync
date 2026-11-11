package github_team_reconciler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-github/v93/github"
)

// ManagedTeamSlugs lists only direct children of the dedicated parent team.
// Teams elsewhere in the organization are never candidates for orphan cleanup.
func (r *githubTeamReconciler) ManagedTeamSlugs(ctx context.Context) ([]string, error) {
	parent, resp, err := r.teamsService.GetTeamBySlug(ctx, r.org, parentTeamSlug)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err := httpError(http.StatusOK, resp, err); err != nil {
		return nil, fmt.Errorf("get managed parent: %w", err)
	}
	if parent.GetID() <= 0 {
		return nil, fmt.Errorf("managed parent has no valid GitHub ID")
	}
	var slugs []string
	opts := &github.ListOptions{PerPage: 100}
	for {
		teams, resp, err := r.teamsService.ListChildTeamsByParentSlug(ctx, r.org, parentTeamSlug, opts)
		if err := httpError(http.StatusOK, resp, err); err != nil {
			return nil, fmt.Errorf("list managed teams: %w", err)
		}
		for _, team := range teams {
			if team.GetSlug() == "" {
				return nil, fmt.Errorf("GitHub inventory contains a team without a slug")
			}
			// GitHub can return descendants; require explicit direct ownership.
			if team.GetParent().GetID() == parent.GetID() && team.GetSlug() != parentTeamSlug {
				slugs = append(slugs, team.GetSlug())
			}
		}
		if resp.NextPage == 0 {
			return slugs, nil
		}
		opts.Page = resp.NextPage
	}
}
