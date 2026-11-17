package github_team_reconciler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/nais/nais-github-teamsync/internal/reconcilers"
)

var errGitHubUserNotFound = errors.New("GitHub user does not exist")

const (
	parentTeamSlug     = "nais-teams"
	parentTeamCacheTTL = 5 * time.Minute
)

type OptFunc func(*githubTeamReconciler)

func WithDryRun(dryRun bool) OptFunc {
	return func(r *githubTeamReconciler) {
		r.dryRun = dryRun
	}
}

func WithIdentityProvider(provider config.IdentityProvider, enterprise string) OptFunc {
	return func(r *githubTeamReconciler) {
		r.identityProvider = provider
		r.enterprise = enterprise
	}
}

func New(org string, teamsService TeamsService, graphClient GraphClient, opts ...OptFunc) reconcilers.Reconciler {
	r := &githubTeamReconciler{
		org:              org,
		teamsService:     teamsService,
		graphClient:      graphClient,
		identityProvider: config.IdentityProviderOrganizationSAML,
		parentTeamCache:  &parentTeamCache{gate: make(chan struct{}, 1)},
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

func (r *githubTeamReconciler) Reconcile(ctx context.Context, source reconcilers.TeamSource, naisTeam *naisapi.Team, log *slog.Logger) error {
	r = r.withWritePreview(log)
	parentTeam, err := r.getOrCreateParentTeam(ctx)
	if err != nil {
		return err
	}
	parentTeamID := parentTeam.GetID()
	githubTeam, err := r.getOrCreateTeam(ctx, naisTeam, parentTeamID)
	if err != nil {
		return fmt.Errorf("unable to get or create a GitHub team for team %q: %w", naisTeam.Slug, err)
	}

	if err := r.syncTeamInfo(ctx, naisTeam, githubTeam, parentTeamID); err != nil {
		return err
	}

	if err := r.connectUsers(ctx, source, naisTeam.Slug, githubTeam, log); err != nil {
		return err
	}

	return nil
}

func (r *githubTeamReconciler) Delete(ctx context.Context, _ reconcilers.TeamSource, naisTeam *naisapi.Team, log *slog.Logger) error {
	r = r.withWritePreview(log)
	slug := strings.ToLower(naisTeam.Slug)
	if slug == "" {
		return fmt.Errorf("refusing to delete a GitHub team without a slug")
	}
	if slug == parentTeamSlug {
		return fmt.Errorf("refusing to delete reserved GitHub parent team %q", parentTeamSlug)
	}
	parent, resp, err := r.teamsService.GetTeamBySlug(ctx, r.org, parentTeamSlug)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if err := httpError(http.StatusOK, resp, err); err != nil {
		return fmt.Errorf("verify parent before deleting %q: %w", slug, err)
	}
	team, resp, err := r.teamsService.GetTeamBySlug(ctx, r.org, slug)
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if err := httpError(http.StatusOK, resp, err); err != nil {
		return fmt.Errorf("verify ownership before deleting %q: %w", slug, err)
	}
	if parent.GetID() <= 0 || team.GetParent().GetID() != parent.GetID() {
		return fmt.Errorf("refusing to delete GitHub team %q outside the managed parent", slug)
	}

	resp, err = r.teamsService.DeleteTeamBySlug(ctx, r.org, slug)
	if resp != nil {
		if resp.Body != nil {
			defer func() { _ = resp.Body.Close() }()
		}

		if resp.StatusCode == http.StatusNotFound {
			// If not found, we consider the team already deleted
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("delete GitHub team %q for team %q: %w", slug, naisTeam.Slug, err)
	}
	if resp == nil {
		return fmt.Errorf("delete GitHub team %q: no response", slug)
	}

	if resp.StatusCode != http.StatusNoContent {
		var body []byte
		if resp.Body != nil {
			body, _ = io.ReadAll(resp.Body)
		}
		return fmt.Errorf("unexpected server response from GitHub: %q: %q", resp.Status, string(body))
	}

	return nil
}

func (r *githubTeamReconciler) syncTeamInfo(ctx context.Context, naisTeam *naisapi.Team, githubTeam *github.Team, parentTeamID int64) error {
	if gitHubTeamIsUpdated(naisTeam, githubTeam, parentTeamID) {
		return nil
	}

	slug := *githubTeam.Slug
	updatedGitHubTeam := github.UpdateTeamRequest{
		Name:         &slug,
		Description:  &naisTeam.Purpose,
		Privacy:      new("closed"),
		ParentTeamID: &parentTeamID,
	}

	_, resp, err := r.teamsService.UpdateTeamBySlug(ctx, r.org, slug, updatedGitHubTeam)

	if err := httpError(http.StatusOK, resp, err); err != nil {
		return fmt.Errorf("sync team info for GitHub team %q: %w", slug, err)
	}

	return nil
}

func (r *githubTeamReconciler) getOrCreateParentTeam(ctx context.Context) (*github.Team, error) {
	cache := r.parentTeamCache
	if cache != nil {
		select {
		case cache.gate <- struct{}{}:
			defer func() { <-cache.gate }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cache != nil && time.Now().Before(cache.expiresAt) {
		return cache.team, nil
	}
	team, err := r.resolveParentTeam(ctx)
	if err != nil {
		return nil, err
	}
	if team.GetID() <= 0 {
		return nil, fmt.Errorf("GitHub parent team has no valid ID")
	}
	if cache != nil {
		cache.team = team
		cache.expiresAt = time.Now().Add(parentTeamCacheTTL)
	}
	return team, nil
}

func (r *githubTeamReconciler) resolveParentTeam(ctx context.Context) (*github.Team, error) {
	team, resp, err := r.teamsService.GetTeamBySlug(ctx, r.org, parentTeamSlug)
	if resp == nil || resp.StatusCode != http.StatusNotFound {
		if err := httpError(http.StatusOK, resp, err); err != nil {
			return nil, fmt.Errorf("get GitHub parent team %q: %w", parentTeamSlug, err)
		}
		return team, nil
	}

	team, resp, err = r.teamsService.CreateTeam(ctx, r.org, github.CreateTeamRequest{
		Name:        parentTeamSlug,
		Description: new("Parent team for all teams synced by Nais."),
		Privacy:     new("closed"),
	})
	if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
		// Another reconciliation may have created the shared parent in the meantime.
		team, resp, err = r.teamsService.GetTeamBySlug(ctx, r.org, parentTeamSlug)
		if err := httpError(http.StatusOK, resp, err); err != nil {
			return nil, fmt.Errorf("get GitHub parent team %q after creation conflict: %w", parentTeamSlug, err)
		}
		return team, nil
	}
	if err := httpError(http.StatusCreated, resp, err); err != nil {
		return nil, fmt.Errorf("create GitHub parent team %q: %w", parentTeamSlug, err)
	}
	return team, nil
}

func (r *githubTeamReconciler) getOrCreateTeam(ctx context.Context, naisTeam *naisapi.Team, parentTeamID int64) (*github.Team, error) {
	desiredTeamSlug := strings.ToLower(naisTeam.Slug)
	if desiredTeamSlug == parentTeamSlug {
		return nil, fmt.Errorf("GitHub team slug %q is reserved for the parent team", parentTeamSlug)
	}
	existingTeam, resp, err := r.teamsService.GetTeamBySlug(ctx, r.org, desiredTeamSlug)
	if resp == nil && err != nil {
		return nil, fmt.Errorf("unable to fetch GitHub team %q: %w", desiredTeamSlug, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("unable to fetch GitHub team %q: no response from GitHub", desiredTeamSlug)
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		break
	case http.StatusOK:
		if err != nil {
			return nil, fmt.Errorf("fetch GitHub team %q: %w", desiredTeamSlug, err)
		}
		if existingTeam.GetSlug() == "" {
			return nil, fmt.Errorf("GitHub returned a team without a slug")
		}
		if existingTeam.GetParent().GetID() != parentTeamID {
			return nil, fmt.Errorf("refusing to adopt GitHub team %q outside the managed parent", desiredTeamSlug)
		}
		return existingTeam, nil
	default:
		var body []byte
		if resp.Body != nil {
			body, _ = io.ReadAll(resp.Body)
		}
		return nil, fmt.Errorf("server error from GitHub: %s: %s", resp.Status, string(body))
	}

	newTeam := github.CreateTeamRequest{
		Name:         desiredTeamSlug,
		Description:  &naisTeam.Purpose,
		Privacy:      new("closed"),
		ParentTeamID: &parentTeamID,
	}
	githubTeam, resp, err := r.teamsService.CreateTeam(ctx, r.org, newTeam)
	if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
		// Another attempt may have created the team since our lookup. Only
		// recover teams already under our parent.
		existing, getResp, getErr := r.teamsService.GetTeamBySlug(ctx, r.org, desiredTeamSlug)
		if getErr == nil && getResp != nil && getResp.StatusCode == http.StatusOK && existing.GetParent().GetID() == parentTeamID {
			return existing, nil
		}
	}
	err = httpError(http.StatusCreated, resp, err)
	if err != nil {
		return nil, fmt.Errorf("unable to create GitHub team: %w", err)
	}
	if !strings.EqualFold(githubTeam.GetSlug(), desiredTeamSlug) {
		return nil, fmt.Errorf("created GitHub team did not have the requested slug %q", desiredTeamSlug)
	}

	return githubTeam, nil
}

func (r *githubTeamReconciler) connectUsers(ctx context.Context, source reconcilers.TeamSource, teamSlug string, githubTeam *github.Team, log *slog.Logger) error {
	naisTeamMembers, err := source.Members(ctx, teamSlug)
	if err != nil {
		return err
	}

	membersAccordingToGitHub, err := r.getTeamMembers(ctx, *githubTeam.Slug)
	if err != nil {
		return fmt.Errorf("list existing members in GitHub team %q: %w", *githubTeam.Slug, err)
	}

	gitHubUsersToApiUsers, err := r.mapSSOUsers(ctx, naisTeamMembers, log)
	if err != nil {
		return err
	}

	membersToRemove := remoteOnlyMembers(membersAccordingToGitHub, gitHubUsersToApiUsers)
	var membershipErrors []error
	for _, gitHubUser := range membersToRemove {
		username := gitHubUser.GetLogin()
		resp, err := r.teamsService.RemoveTeamMembershipBySlug(ctx, r.org, *githubTeam.Slug, username)
		err = httpError(http.StatusNoContent, resp, err)
		if err != nil {
			log.WarnContext(ctx, "remove member from GitHub team", "error", err, "username", username, "github_team", *githubTeam.Slug)
			membershipErrors = append(membershipErrors, err)
			continue
		}

	}

	membersToAdd := localOnlyMembers(gitHubUsersToApiUsers, membersAccordingToGitHub)
	for username := range membersToAdd {
		_, resp, err := r.teamsService.AddTeamMembershipBySlug(ctx, r.org, *githubTeam.Slug, username, github.AddTeamMembershipRequest{})
		if err := httpError(http.StatusOK, resp, err); err != nil {
			log.WarnContext(ctx, "add member to GitHub team", "error", err, "username", username, "github_team", *githubTeam.Slug)
			membershipErrors = append(membershipErrors, err)
			continue
		}
	}

	return errors.Join(membershipErrors...)
}

// getTeamMembers Get all team members in a GitHub team using a paginated query
func (r *githubTeamReconciler) getTeamMembers(ctx context.Context, slug string) ([]*github.User, error) {
	const maxPerPage = 100
	opt := &github.TeamListTeamMembersOptions{
		PerPage: maxPerPage,
	}

	allMembers := make([]*github.User, 0)
	for {
		members, resp, err := r.teamsService.ListTeamMembersBySlug(ctx, r.org, slug, opt)
		err = httpError(http.StatusOK, resp, err)
		if err != nil {
			return nil, err
		}
		allMembers = append(allMembers, members...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allMembers, nil
}

// localOnlyMembers Given a mapping of GitHub usernames to Nais teams users, and a list of GitHub team members according
// to GitHub, return members only present in the mapping.
func localOnlyMembers(naisUsers map[string]*naisapi.Member, membersAccordingToGitHub []*github.User) map[string]*naisapi.Member {
	gitHubUsernameMap := make(map[string]*github.User)
	for _, gitHubUser := range membersAccordingToGitHub {
		gitHubUsernameMap[gitHubUser.GetLogin()] = gitHubUser
	}

	localOnly := make(map[string]*naisapi.Member)
	for gitHubUsername, naisUser := range naisUsers {
		if _, exists := gitHubUsernameMap[gitHubUsername]; !exists {
			localOnly[gitHubUsername] = naisUser
		}
	}
	return localOnly
}

// remoteOnlyMembers Given a list of GitHub team members and a mapping of known GitHub usernames to Nais teams users,
// return members not present in the mapping.
func remoteOnlyMembers(membersAccordingToGitHub []*github.User, naisUsers map[string]*naisapi.Member) []*github.User {
	unknownMembers := make([]*github.User, 0)
	for _, member := range membersAccordingToGitHub {
		if _, exists := naisUsers[member.GetLogin()]; !exists {
			unknownMembers = append(unknownMembers, member)
		}
	}
	return unknownMembers
}

// mapSSOUsers Return a mapping of GitHub usernames to Nais teams users. Nais teams users with no matching GitHub user
// will be ignored.
func (r *githubTeamReconciler) mapSSOUsers(ctx context.Context, members []*naisapi.Member, log *slog.Logger) (map[string]*naisapi.Member, error) {
	userMap := make(map[string]*naisapi.Member)
	for _, member := range members {
		githubUsername, err := r.getGitHubUsernameFromEmail(ctx, member.Email)
		if errors.Is(err, errGitHubUserNotFound) {
			log.WarnContext(ctx, "no GitHub user for email", "error", err, "email", member.Email)
			continue
		}
		if err != nil {
			return nil, err
		}
		userMap[*githubUsername] = member
	}

	return userMap, nil
}

// httpError Return an error if the response status code is not as expected, or if the passed err is already set to an
// error
func httpError(expected int, resp *github.Response, err error) error {
	if err != nil {
		return err
	}

	if resp == nil {
		return fmt.Errorf("no response")
	}

	if resp.StatusCode != expected {
		if resp.Body == nil {
			return errors.New("unknown error")
		}
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected response error %s: %s", resp.Status, string(body))
	}

	return nil
}

// gitHubTeamIsUpdated check if a GitHub team is updated compared to the Nais team
func gitHubTeamIsUpdated(naisTeam *naisapi.Team, gitHubTeam *github.Team, parentTeamID int64) bool {
	if naisTeam.Purpose != gitHubTeam.GetDescription() {
		return false
	}

	if gitHubTeam.GetPrivacy() != "closed" {
		return false
	}

	if gitHubTeam.GetParent().GetID() != parentTeamID {
		return false
	}

	return true
}
