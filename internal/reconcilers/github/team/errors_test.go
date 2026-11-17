package github_team_reconciler

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/stretchr/testify/mock"
)

func TestGitHubValidationErrorDetails(t *testing.T) {
	for _, parsed := range []bool{false, true} {
		name := "response body"
		if parsed {
			name = "parsed API error"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			teams := NewMockTeamsService(t)
			r := &githubTeamReconciler{org: "org", teamsService: teams}
			req, err := http.NewRequestWithContext(ctx, http.MethodPatch, "https://api.github.com/orgs/org/teams/team", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp := &github.Response{Response: &http.Response{
				Request:    req,
				StatusCode: http.StatusUnprocessableEntity,
				Status:     "422 Unprocessable Entity",
				Body:       io.NopCloser(strings.NewReader(`{"message":"Validation Failed","errors":[{"field":"parent_team_id","code":"invalid"}]}`)),
			}}
			var apiErr error
			if parsed {
				apiErr = &github.ErrorResponse{
					Response: resp.Response,
					Message:  "Validation Failed",
					Errors:   []github.Error{{Resource: "Team", Field: "parent_team_id", Code: "invalid"}},
				}
			}
			teams.EXPECT().UpdateTeamBySlug(ctx, "org", "team", mock.Anything).
				Return(nil, resp, apiErr).Once()
			err = r.syncTeamInfo(ctx, &naisapi.Team{Slug: "team"}, &github.Team{Slug: new("team")}, 123)
			if err == nil || !strings.Contains(err.Error(), `sync team info for GitHub team "team"`) {
				t.Fatalf("expected team sync error, got %v", err)
			}
			if !strings.Contains(err.Error(), "Validation Failed") || !strings.Contains(err.Error(), "parent_team_id") {
				t.Errorf("error lacks validation details: %v", err)
			}
			if parsed {
				if !errors.Is(err, apiErr) {
					t.Errorf("error = %v, want wrapped %v", err, apiErr)
				}
			}
		})
	}
}
