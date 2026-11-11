package github_team_reconciler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
)

func TestGetOrCreateParentTeam(t *testing.T) {
	ctx := t.Context()
	parent := &github.Team{ID: new(int64(123)), Slug: new(parentTeamSlug)}
	response := func(code int) *github.Response {
		return &github.Response{Response: &http.Response{StatusCode: code}}
	}

	t.Run("reuse existing parent", func(t *testing.T) {
		teams := NewMockTeamsService(t)
		teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
			Return(parent, response(http.StatusOK), nil).Once()
		r := &githubTeamReconciler{org: "org", teamsService: teams}
		team, err := r.getOrCreateParentTeam(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if team != parent {
			t.Error("expected the existing parent team pointer")
		}
	})

	t.Run("create missing parent", func(t *testing.T) {
		teams := NewMockTeamsService(t)
		teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
			Return(nil, response(http.StatusNotFound), errors.New("not found")).Once()
		teams.EXPECT().CreateTeam(ctx, "org", github.CreateTeamRequest{Name: parentTeamSlug, Description: new("Parent team for all teams synced by Nais."), Privacy: new("closed")}).
			Return(parent, response(http.StatusCreated), nil).Once()
		r := &githubTeamReconciler{org: "org", teamsService: teams}
		team, err := r.getOrCreateParentTeam(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if team != parent {
			t.Error("expected the created parent team pointer")
		}
	})

	t.Run("concurrent creation", func(t *testing.T) {
		teams := NewMockTeamsService(t)
		teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
			Return(nil, response(http.StatusNotFound), errors.New("not found")).Once()
		teams.EXPECT().CreateTeam(ctx, "org", github.CreateTeamRequest{Name: parentTeamSlug, Description: new("Parent team for all teams synced by Nais."), Privacy: new("closed")}).
			Return(nil, response(http.StatusUnprocessableEntity), errors.New("already exists")).Once()
		teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
			Return(parent, response(http.StatusOK), nil).Once()
		r := &githubTeamReconciler{org: "org", teamsService: teams}
		team, err := r.getOrCreateParentTeam(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if team != parent {
			t.Error("expected the concurrently created parent team pointer")
		}
	})

	t.Run("lookup failure", func(t *testing.T) {
		teams := NewMockTeamsService(t)
		teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
			Return(nil, nil, errors.New("unavailable")).Once()
		r := &githubTeamReconciler{org: "org", teamsService: teams}
		_, err := r.getOrCreateParentTeam(ctx)
		if err == nil || !strings.Contains(err.Error(), "get GitHub parent team") {
			t.Fatalf("expected parent lookup error, got %v", err)
		}
	})

	t.Run("creation failure", func(t *testing.T) {
		teams := NewMockTeamsService(t)
		teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
			Return(nil, response(http.StatusNotFound), nil).Once()
		teams.EXPECT().CreateTeam(ctx, "org", github.CreateTeamRequest{Name: parentTeamSlug, Description: new("Parent team for all teams synced by Nais."), Privacy: new("closed")}).
			Return(nil, nil, errors.New("forbidden")).Once()
		r := &githubTeamReconciler{org: "org", teamsService: teams}
		_, err := r.getOrCreateParentTeam(ctx)
		if err == nil || !strings.Contains(err.Error(), "create GitHub parent team") {
			t.Fatalf("expected parent creation error, got %v", err)
		}
	})
}

func TestSyncTeamParent(t *testing.T) {
	ctx := t.Context()
	naisTeam := &naisapi.Team{Slug: "team", Purpose: "purpose"}
	const parentID int64 = 123

	for _, tt := range []struct {
		name   string
		parent *github.Team
	}{
		{name: "no parent"},
		{name: "wrong parent", parent: &github.Team{ID: new(int64(456))}},
		{name: "correct parent", parent: &github.Team{ID: new(parentID)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			teams := NewMockTeamsService(t)
			team := &github.Team{Slug: new("team"), Description: new("purpose"), Privacy: new("closed"), Parent: tt.parent}
			if tt.name != "correct parent" {
				teams.EXPECT().UpdateTeamBySlug(ctx, "org", "team", github.UpdateTeamRequest{
					Name: new("team"), Description: &naisTeam.Purpose, Privacy: new("closed"), ParentTeamID: new(parentID),
				}).Return(team, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
			}
			r := &githubTeamReconciler{org: "org", teamsService: teams}
			if err := r.syncTeamInfo(ctx, naisTeam, team, parentID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParentTeamSlugIsReserved(t *testing.T) {
	r := &githubTeamReconciler{}
	for _, team := range []*naisapi.Team{
		{Slug: parentTeamSlug},
		{Slug: strings.ToUpper(parentTeamSlug)},
	} {
		_, err := r.getOrCreateTeam(t.Context(), team, 123)
		if err == nil || !strings.Contains(err.Error(), "reserved for the parent team") {
			t.Fatalf("expected reserved parent error, got %v", err)
		}
	}
}

func TestReconcileStopsWithoutParent(t *testing.T) {
	for _, failure := range []string{"lookup", "creation"} {
		t.Run(failure, func(t *testing.T) {
			ctx := t.Context()
			log := slog.New(slog.DiscardHandler)
			teams := NewMockTeamsService(t)
			r := &githubTeamReconciler{org: "org", teamsService: teams}
			if failure == "lookup" {
				teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
					Return(nil, nil, errors.New("unavailable")).Once()
			} else {
				teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
					Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, nil).Once()
				teams.EXPECT().CreateTeam(ctx, "org", github.CreateTeamRequest{
					Name: parentTeamSlug, Description: new("Parent team for all teams synced by Nais."), Privacy: new("closed"),
				}).
					Return(nil, nil, errors.New("forbidden")).Once()
			}

			naisTeam := &naisapi.Team{Slug: "team", Purpose: "purpose"}

			err := r.Reconcile(ctx, nil, naisTeam, log)
			if failure == "lookup" {
				if err == nil || !strings.Contains(err.Error(), "get GitHub parent team") {
					t.Fatalf("expected parent lookup error, got %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "create GitHub parent team") {
					t.Fatalf("expected parent creation error, got %v", err)
				}
			}
		})
	}
}
