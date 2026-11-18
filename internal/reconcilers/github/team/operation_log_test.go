package github_team_reconciler

import (
	"net/http"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/stretchr/testify/mock"
)

func TestLiveGitHubWriteLogs(t *testing.T) {
	ctx := t.Context()
	teams := NewMockTeamsService(t)
	graph := NewMockGraphClient(t)
	parent := &github.Team{ID: new(int64(123)), Slug: new(parentTeamSlug)}
	child := &github.Team{Slug: new("child"), Privacy: new("closed"), Parent: parent}
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).Return(nil, previewResponse(http.StatusNotFound), nil).Once()
	teams.EXPECT().CreateTeam(ctx, "org", mock.MatchedBy(func(req github.CreateTeamRequest) bool {
		return req.Name == parentTeamSlug
	})).Return(parent, previewResponse(http.StatusCreated), nil).Once()
	teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(nil, previewResponse(http.StatusNotFound), nil).Once()
	teams.EXPECT().CreateTeam(ctx, "org", mock.MatchedBy(func(req github.CreateTeamRequest) bool {
		return req.Name == "child"
	})).Return(child, previewResponse(http.StatusCreated), nil).Once()
	teams.EXPECT().UpdateTeamBySlug(ctx, "org", "child", mock.Anything).
		Return(child, previewResponse(http.StatusOK), nil).Once()
	expectDirectMembers(t, graph, "child", "old-user")
	expectPreviewIdentity(graph, 1)
	teams.EXPECT().RemoveTeamMembershipBySlug(ctx, "org", "child", "old-user").Return(previewResponse(http.StatusNoContent), nil).Once()
	teams.EXPECT().AddTeamMembershipBySlug(ctx, "org", "child", "new-user", mock.Anything).
		Return(&github.Membership{}, previewResponse(http.StatusOK), nil).Once()
	teams.EXPECT().GetTeamBySlug(ctx, "org", parentTeamSlug).Return(parent, previewResponse(http.StatusOK), nil).Once()
	teams.EXPECT().GetTeamBySlug(ctx, "org", "child").Return(child, previewResponse(http.StatusOK), nil).Once()
	teams.EXPECT().DeleteTeamBySlug(ctx, "org", "child").Return(previewResponse(http.StatusNoContent), nil).Once()

	r := New("org", teams, graph)
	log, output := operationLog()
	source := &previewSource{members: []*naisapi.Member{{Email: "new@example.com"}}}
	team := &naisapi.Team{Slug: "child", Purpose: "new purpose"}
	if err := r.Reconcile(ctx, source, team, log); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, source, team, log); err != nil {
		t.Fatal(err)
	}
	operations := []string{"create_team", "create_team", "update_team", "remove_team_member", "add_team_member", "delete_team"}
	records := checkOperations(t, output, false, operations)
	if len(records) != len(operations) {
		t.FailNow()
	}
	for i, slug := range []string{parentTeamSlug, "child", "child", "child", "child", "child"} {
		if records[i]["github_team"] != slug {
			t.Errorf("expected GitHub team %q: %v", slug, records[i])
		}
	}
	if records[3]["username"] != "old-user" || records[4]["username"] != "new-user" {
		t.Errorf("missing membership usernames: %v", records[3:5])
	}
}
