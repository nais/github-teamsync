package github_team_reconciler

import (
	"context"
	"log/slog"
	"math"
	"net/http"

	"github.com/google/go-github/v93/github"
)

func (r *githubTeamReconciler) withWritePreview(log *slog.Logger) *githubTeamReconciler {
	if !r.dryRun {
		return r
	}
	preview := *r
	// Each preview must resolve its own parent, which might only exist in its plan.
	preview.parentTeamCache = nil
	preview.teamsService = &previewTeamsService{
		base:    r.teamsService,
		log:     log.With("dry_run", true),
		created: make(map[string]*github.Team),
	}
	return &preview
}

// previewTeamsService forwards reads and logs writes instead of forwarding them.
// Its temporary state lets reconciliation continue when a parent or child would
// be created. It is scoped to one reconciliation, never retained across polls.
type previewTeamsService struct {
	// Do not embed TeamsService: adding any method must require an explicit
	// decision about whether it is safe to forward in dry-run mode.
	base    TeamsService
	log     *slog.Logger
	created map[string]*github.Team
}

var _ TeamsService = (*previewTeamsService)(nil)

func previewResponse(status int) *github.Response {
	return &github.Response{Response: &http.Response{StatusCode: status}}
}

func (s *previewTeamsService) record(ctx context.Context, operation, org, slug string, attrs ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.log.InfoContext(ctx, "would write GitHub team state", append([]any{
		"operation", operation, "organization", org, "github_team", slug,
	}, attrs...)...)
	return nil
}

func (s *previewTeamsService) GetTeamBySlug(ctx context.Context, org, slug string) (*github.Team, *github.Response, error) {
	if team := s.created[slug]; team != nil {
		return team, previewResponse(http.StatusOK), ctx.Err()
	}
	team, resp, err := s.base.GetTeamBySlug(ctx, org, slug)
	if team != nil && s.created[parentTeamSlug] != nil {
		// A real team cannot belong to a parent that only exists in this plan.
		// Do not let a synthetic ID accidentally authorize implicit adoption.
		copy := *team
		copy.Parent = nil
		team = &copy
	}
	return team, resp, err
}

func (s *previewTeamsService) ListTeamMembersBySlug(ctx context.Context, org, slug string, opts *github.TeamListTeamMembersOptions) ([]*github.User, *github.Response, error) {
	if s.created[slug] != nil {
		return []*github.User{}, previewResponse(http.StatusOK), ctx.Err()
	}
	return s.base.ListTeamMembersBySlug(ctx, org, slug, opts)
}

func (s *previewTeamsService) ListChildTeamsByParentSlug(ctx context.Context, org, slug string, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
	return s.base.ListChildTeamsByParentSlug(ctx, org, slug, opts)
}

func (s *previewTeamsService) CreateTeam(ctx context.Context, org string, req github.CreateTeamRequest) (*github.Team, *github.Response, error) {
	attrs := []any{"description", req.GetDescription(), "privacy", req.GetPrivacy()}
	if req.ParentTeamID != nil {
		attrs = append(attrs, "parent_team", parentTeamSlug)
	}
	if err := s.record(ctx, "create_team", org, req.Name, attrs...); err != nil {
		return nil, nil, err
	}
	team := &github.Team{
		ID: new(int64(math.MaxInt64) - int64(len(s.created))), Slug: new(req.Name),
		Name: new(req.Name), Description: req.Description, Privacy: req.Privacy,
	}
	if req.ParentTeamID != nil {
		team.Parent = &github.Team{ID: req.ParentTeamID, Slug: new(parentTeamSlug)}
	}
	s.created[req.Name] = team
	return team, previewResponse(http.StatusCreated), nil
}

func (s *previewTeamsService) UpdateTeamBySlug(ctx context.Context, org, slug string, req github.UpdateTeamRequest) (*github.Team, *github.Response, error) {
	if err := s.record(ctx, "update_team", org, slug,
		"name", req.GetName(), "description", req.GetDescription(), "privacy", req.GetPrivacy(), "parent_team", parentTeamSlug); err != nil {
		return nil, nil, err
	}
	return &github.Team{Slug: new(slug)}, previewResponse(http.StatusOK), nil
}

func (s *previewTeamsService) AddTeamMembershipBySlug(ctx context.Context, org, slug, user string, _ github.AddTeamMembershipRequest) (*github.Membership, *github.Response, error) {
	if err := s.record(ctx, "add_team_member", org, slug, "username", user); err != nil {
		return nil, nil, err
	}
	return &github.Membership{}, previewResponse(http.StatusOK), nil
}

func (s *previewTeamsService) RemoveTeamMembershipBySlug(ctx context.Context, org, slug, user string) (*github.Response, error) {
	if err := s.record(ctx, "remove_team_member", org, slug, "username", user); err != nil {
		return nil, err
	}
	return previewResponse(http.StatusNoContent), nil
}

func (s *previewTeamsService) DeleteTeamBySlug(ctx context.Context, org, slug string) (*github.Response, error) {
	if err := s.record(ctx, "delete_team", org, slug); err != nil {
		return nil, err
	}
	return previewResponse(http.StatusNoContent), nil
}
