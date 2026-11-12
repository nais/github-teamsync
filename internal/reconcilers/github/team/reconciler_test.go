package github_team_reconciler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	github_team_reconciler "github.com/nais/nais-github-teamsync/internal/reconcilers/github/team"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/mock"
)

func newTestSource(t *testing.T, slug string, emails ...string) *naisapi.Client {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if r.Method != http.MethodPost || !strings.Contains(req.Query, "query Members") {
			t.Errorf("unexpected Nais GraphQL request: %s %s", r.Method, req.Query)
		}
		if got := req.Variables["slug"]; got != slug {
			t.Errorf("member query slug = %v, want %s", got, slug)
		}
		if req.Variables["after"] != nil {
			t.Errorf("unexpected pagination cursor: %v", req.Variables["after"])
		}
		nodes := make([]map[string]any, 0, len(emails))
		for _, email := range emails {
			nodes = append(nodes, map[string]any{"user": map[string]any{"email": email}})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"team": map[string]any{"members": map[string]any{
				"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false},
			}}},
		}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		if got := calls.Load(); got != 1 {
			t.Errorf("Nais GraphQL requests = %d, want 1", got)
		}
	})
	return naisapi.New(server.URL, "", server.Client())
}

func expectParentTeam(t *testing.T, teamsService *github_team_reconciler.MockTeamsService, org string) {
	t.Helper()
	teamsService.EXPECT().
		GetTeamBySlug(mock.Anything, org, "nais-teams").
		Return(&github.Team{ID: new(int64(123)), Slug: new("nais-teams")}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).
		Once()
}

func expectOwnedTeam(t *testing.T, teamsService *github_team_reconciler.MockTeamsService, ctx context.Context, org, slug string) {
	t.Helper()
	expectParentTeam(t, teamsService, org)
	teamsService.EXPECT().GetTeamBySlug(ctx, org, slug).
		Return(&github.Team{Slug: new(slug), Parent: &github.Team{ID: new(int64(123))}}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).
		Once()
}

func TestGitHubReconciler_getOrCreateTeam(t *testing.T) {
	ctx := context.Background()

	const (
		org         = "org"
		teamSlug    = "slug"
		teamPurpose = "purpose"
	)

	log := slog.New(slog.DiscardHandler)

	t.Run("github team not available", func(t *testing.T) {
		naisTeam := &naisapi.Team{
			Slug:    teamSlug,
			Purpose: teamPurpose,
		}

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		teamsService := github_team_reconciler.NewMockTeamsService(t)
		expectParentTeam(t, teamsService, org)
		teamsService.EXPECT().
			CreateTeam(ctx, org, github.CreateTeamRequest{Name: teamSlug, Description: new(teamPurpose), Privacy: new("closed"), ParentTeamID: new(int64(123))}).
			Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}}, nil).
			Once()
		teamsService.EXPECT().GetTeamBySlug(ctx, org, teamSlug).
			Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, nil).Times(2)

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Reconcile(ctx, nil, naisTeam, log); err == nil || !strings.Contains(err.Error(), "unable to create GitHub team") {
			t.Fatalf("expected error")
		}
	})

	t.Run("github team exists", func(t *testing.T) {
		naisTeam := &naisapi.Team{
			Slug:    teamSlug,
			Purpose: teamPurpose,
		}

		apiClient := newTestSource(t, teamSlug)

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		teamsService := github_team_reconciler.NewMockTeamsService(t)
		expectParentTeam(t, teamsService, org)
		teamsService.EXPECT().
			GetTeamBySlug(ctx, org, teamSlug).
			Return(&github.Team{Slug: new(teamSlug), Parent: &github.Team{ID: new(int64(123))}}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).
			Once()
		teamsService.EXPECT().
			ListTeamMembersBySlug(mock.Anything, org, teamSlug, mock.Anything).
			Return([]*github.User{}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).
			Once()
		teamsService.EXPECT().
			UpdateTeamBySlug(mock.Anything, org, teamSlug, github.UpdateTeamRequest{
				Name:         new(teamSlug),
				Description:  new(teamPurpose),
				Privacy:      new("closed"),
				ParentTeamID: new(int64(123)),
			}).
			Return(&github.Team{}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).
			Once()
		teamsService.EXPECT().
			CreateOrUpdateIDPGroupConnectionsBySlug(mock.Anything, org, teamSlug, github.IDPGroupList{Groups: []*github.IDPGroup{}}).
			Return(&github.IDPGroupList{}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).
			Once()

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Reconcile(ctx, apiClient, naisTeam, log); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestGitHubReconciler_Reconcile(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	const (
		org         = "my-organization"
		teamSlug    = "myteam"
		teamName    = "myteam"
		teamPurpose = "some purpose"
		createLogin = "should-create"
		createEmail = "should-create@example.com"
		keepLogin   = "should-keep"
		keepEmail   = "should-keep@example.com"
		removeLogin = "should-remove"
		removeEmail = "should-remove@example.com"
	)

	httpOk := &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}

	// Give the reconciler enough data to create an entire team from scratch,
	// remove members that shouldn't be present, and add members that should.
	t.Run("create everything from scratch", func(t *testing.T) {
		naisTeam := &naisapi.Team{
			Slug:    teamSlug,
			Purpose: teamPurpose,
		}

		apiClient := newTestSource(t, teamSlug, keepEmail, createEmail)

		teamsService := github_team_reconciler.NewMockTeamsService(t)
		expectParentTeam(t, teamsService, org)
		teamsService.EXPECT().GetTeamBySlug(ctx, org, teamSlug).
			Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, nil).Once()
		teamsService.EXPECT().
			CreateTeam(mock.Anything, org, github.CreateTeamRequest{
				Name:         teamName,
				Description:  new(teamPurpose),
				Privacy:      new("closed"),
				ParentTeamID: new(int64(123)),
			}).
			Return(
				&github.Team{Slug: new(teamName)},
				&github.Response{Response: &http.Response{StatusCode: http.StatusCreated}},
				nil,
			).
			Once()
		teamsService.EXPECT().
			UpdateTeamBySlug(mock.Anything, org, teamName, github.UpdateTeamRequest{
				Name:         new(teamName),
				Description:  new(teamPurpose),
				Privacy:      new("closed"),
				ParentTeamID: new(int64(123)),
			}).
			Return(
				&github.Team{},
				httpOk,
				nil,
			).Once()
		teamsService.EXPECT().
			ListTeamMembersBySlug(mock.Anything, org, teamName, mock.Anything).
			Return(
				[]*github.User{
					{Login: new(keepLogin)},
					{Login: new(removeLogin)},
				},
				httpOk,
				nil,
			).
			Once()
		teamsService.EXPECT().
			AddTeamMembershipBySlug(mock.Anything, org, teamName, createLogin, mock.Anything).
			Return(
				&github.Membership{
					User: &github.User{
						Login: new(createLogin),
					},
				},
				httpOk,
				nil,
			).
			Once()
		teamsService.EXPECT().
			RemoveTeamMembershipBySlug(mock.Anything, org, teamName, removeLogin).
			Return(
				&github.Response{
					Response: &http.Response{
						StatusCode: http.StatusNoContent,
					},
				},
				nil,
			).
			Once()
		teamsService.EXPECT().
			CreateOrUpdateIDPGroupConnectionsBySlug(mock.Anything, org, teamName, github.IDPGroupList{Groups: []*github.IDPGroup{}}).
			Return(
				&github.IDPGroupList{},
				httpOk,
				nil,
			).Once()

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		graphClient.EXPECT().
			Query(mock.Anything, mock.Anything, map[string]any{"org": githubv4.String(org), "username": githubv4.String(keepEmail)}).
			Run(func(_ context.Context, q any, v map[string]any) {
				query := q.(*github_team_reconciler.LookupGitHubSamlUserByEmail)
				query.Organization.SamlIdentityProvider = &github_team_reconciler.SamlIdentityProvider{}
				query.Organization.SamlIdentityProvider.ExternalIdentities.Nodes = []github_team_reconciler.ExternalIdentity{
					{User: github_team_reconciler.GitHubUser{Login: keepLogin}},
				}
			}).
			Once().
			Return(nil)
		graphClient.EXPECT().
			Query(mock.Anything, mock.Anything, map[string]any{"org": githubv4.String(org), "username": githubv4.String(createEmail)}).
			Run(func(_ context.Context, q any, v map[string]any) {
				query := q.(*github_team_reconciler.LookupGitHubSamlUserByEmail)
				query.Organization.SamlIdentityProvider = &github_team_reconciler.SamlIdentityProvider{}
				query.Organization.SamlIdentityProvider.ExternalIdentities.Nodes = []github_team_reconciler.ExternalIdentity{
					{User: github_team_reconciler.GitHubUser{Login: createLogin}},
				}
			}).
			Once().
			Return(nil)

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Reconcile(ctx, apiClient, naisTeam, log); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("GetTeamBySlug error", func(t *testing.T) {
		naisTeam := &naisapi.Team{
			Slug:    teamSlug,
			Purpose: teamPurpose,
		}

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		teamsService := github_team_reconciler.NewMockTeamsService(t)
		expectParentTeam(t, teamsService, org)
		teamsService.EXPECT().
			GetTeamBySlug(mock.Anything, org, teamSlug).
			Return(nil, &github.Response{
				Response: &http.Response{
					StatusCode: http.StatusTeapot,
					Status:     "418: I'm a teapot",
					Body:       io.NopCloser(strings.NewReader("this is a body")),
				},
			}, nil).
			Once()

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Reconcile(ctx, nil, naisTeam, log); err == nil || !strings.Contains(err.Error(), "server error from GitHub: 418") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestGitHubReconciler_Delete(t *testing.T) {
	ctx := context.Background()

	const (
		org      = "my-organization"
		teamSlug = "myteam"
	)

	log := slog.New(slog.DiscardHandler)

	t.Run("parent already absent", func(t *testing.T) {
		naisTeam := &naisapi.Team{
			Slug: teamSlug,
		}

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		teamsService := github_team_reconciler.NewMockTeamsService(t)
		teamsService.EXPECT().GetTeamBySlug(ctx, org, "nais-teams").
			Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, nil).Once()

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Delete(ctx, nil, naisTeam, log); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("GitHub API client fails", func(t *testing.T) {
		naisTeam := &naisapi.Team{Slug: teamSlug}

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		teamsService := github_team_reconciler.NewMockTeamsService(t)
		expectOwnedTeam(t, teamsService, ctx, org, teamSlug)
		teamsService.EXPECT().
			DeleteTeamBySlug(ctx, org, teamSlug).
			Return(nil, fmt.Errorf("some error")).
			Once()

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Delete(ctx, nil, naisTeam, log); err == nil || !strings.Contains(err.Error(), "delete GitHub team") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("unexpected response from GitHub API", func(t *testing.T) {
		naisTeam := &naisapi.Team{Slug: teamSlug}

		graphClient := github_team_reconciler.NewMockGraphClient(t)
		teamsService := github_team_reconciler.NewMockTeamsService(t)
		expectOwnedTeam(t, teamsService, ctx, org, teamSlug)
		teamsService.EXPECT().
			DeleteTeamBySlug(ctx, org, teamSlug).
			Return(
				&github.Response{
					Response: &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200: OK",
						Body:       io.NopCloser(strings.NewReader("body")),
					},
				},
				nil,
			).
			Once()

		reconciler := github_team_reconciler.New(org, teamsService, graphClient)

		if err := reconciler.Delete(ctx, nil, naisTeam, log); err == nil || !strings.Contains(err.Error(), "unexpected server response from GitHub") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
