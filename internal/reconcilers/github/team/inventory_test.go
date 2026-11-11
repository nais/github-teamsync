package github_team_reconciler

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/stretchr/testify/mock"
)

func TestManagedTeamInventoryIsPaginatedAndScoped(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	r := &githubTeamReconciler{org: "org", teamsService: teams}
	parent := &github.Team{ID: new(int64(123)), Slug: new(parentTeamSlug)}
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).
		Return(parent, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
	teams.EXPECT().ListChildTeamsByParentSlug(ctx, "org", parentTeamSlug, &github.ListOptions{PerPage: 100}).
		Return([]*github.Team{
			{Slug: new("child"), Parent: parent},
			{Slug: new("unrelated"), Parent: &github.Team{ID: new(int64(999))}},
			{Slug: new("unknown-parent")},
		}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}, NextPage: 2}, nil).Once()
	teams.EXPECT().ListChildTeamsByParentSlug(ctx, "org", parentTeamSlug, &github.ListOptions{PerPage: 100, Page: 2}).
		Return([]*github.Team{{Slug: new("second-child"), Parent: parent}}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
	slugs, err := r.ManagedTeamSlugs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"child", "second-child"}; !slices.Equal(slugs, want) {
		t.Errorf("slugs = %v, want %v", slugs, want)
	}
}

func TestMissingParentHasNoManagedTeams(t *testing.T) {
	teams := NewMockTeamsService(t)
	r := &githubTeamReconciler{org: "org", teamsService: teams}
	teams.EXPECT().GetTeamBySlug(t.Context(), "org", parentTeamSlug).
		Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")).Once()
	slugs, err := r.ManagedTeamSlugs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(slugs) != 0 {
		t.Errorf("expected no managed teams, got %v", slugs)
	}
}

func TestDeleteProtectsReservedParentAndHandlesMissingResponses(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	teams := NewMockTeamsService(t)
	r := &githubTeamReconciler{org: "org", teamsService: teams}
	if err := r.Delete(t.Context(), nil, &naisapi.Team{Slug: parentTeamSlug}, log); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved parent error, got %v", err)
	}
	parent := &github.Team{ID: new(int64(123))}
	teams.EXPECT().GetTeamBySlug(t.Context(), "org", parentTeamSlug).
		Return(parent, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
	teams.EXPECT().GetTeamBySlug(t.Context(), "org", "child").
		Return(&github.Team{Slug: new("child"), Parent: parent}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
	teams.EXPECT().DeleteTeamBySlug(t.Context(), "org", "child").Return(nil, nil).Once()
	if err := r.Delete(t.Context(), nil, &naisapi.Team{Slug: "child"}, log); err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("expected missing response error, got %v", err)
	}
}

func TestCreateConflictOnlyRecoversOwnedTeams(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "unrelated"
		if owned {
			name = "owned"
		}
		t.Run(name, func(t *testing.T) {
			teams := NewMockTeamsService(t)
			r := &githubTeamReconciler{org: "org", teamsService: teams}
			teams.EXPECT().GetTeamBySlug(t.Context(), "org", "child").
				Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, nil).Once()
			teams.EXPECT().CreateTeam(t.Context(), "org", mock.Anything).
				Return(nil, &github.Response{Response: &http.Response{StatusCode: http.StatusUnprocessableEntity}}, errors.New("already exists")).Once()
			parentID := int64(999)
			if owned {
				parentID = 123
			}
			teams.EXPECT().GetTeamBySlug(t.Context(), "org", "child").
				Return(&github.Team{Slug: new("child"), Parent: &github.Team{ID: &parentID}}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
			team, err := r.getOrCreateTeam(t.Context(), &naisapi.Team{Slug: "child"}, 123)
			if owned {
				if err != nil {
					t.Fatal(err)
				}
				if team.GetSlug() != "child" {
					t.Errorf("slug = %q, want child", team.GetSlug())
				}
			} else {
				if err == nil {
					t.Fatal("expected ownership error")
				}
			}
		})
	}
}

func TestMatchingSlugCannotAdoptUnmanagedTeam(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent *github.Team
		owned  bool
	}{
		{"owned", &github.Team{ID: new(int64(123))}, true},
		{"unrelated", &github.Team{ID: new(int64(999))}, false},
		{"no parent", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			teams := NewMockTeamsService(t)
			r := &githubTeamReconciler{org: "org", teamsService: teams}
			teams.EXPECT().GetTeamBySlug(t.Context(), "org", "child").Return(&github.Team{Slug: new("child"), Parent: tc.parent}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
			team := &naisapi.Team{Slug: "child"}
			_, err := r.getOrCreateTeam(t.Context(), team, 123)
			if tc.owned {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
					t.Fatalf("expected adoption safeguard error, got %v", err)
				}
			}
		})
	}
}

func TestMatchingSlugDeletionRequiresManagedParent(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "unrelated"
		if owned {
			name = "owned"
		}
		t.Run(name, func(t *testing.T) {
			teams := NewMockTeamsService(t)
			r := &githubTeamReconciler{org: "org", teamsService: teams}
			log := slog.New(slog.DiscardHandler)
			teams.EXPECT().GetTeamBySlug(t.Context(), "org", parentTeamSlug).Return(&github.Team{ID: new(int64(123))}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
			parentID := int64(999)
			if owned {
				parentID = 123
			}
			teams.EXPECT().GetTeamBySlug(t.Context(), "org", "child").Return(&github.Team{Slug: new("child"), Parent: &github.Team{ID: &parentID}}, &github.Response{Response: &http.Response{StatusCode: http.StatusOK}}, nil).Once()
			if owned {
				teams.EXPECT().DeleteTeamBySlug(t.Context(), "org", "child").Return(&github.Response{Response: &http.Response{StatusCode: http.StatusNoContent}}, nil).Once()
			}
			err := r.Delete(t.Context(), nil, &naisapi.Team{Slug: "child"}, log)
			if owned {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "refusing to delete") {
					t.Fatalf("expected deletion safeguard error, got %v", err)
				}
			}
		})
	}
}
