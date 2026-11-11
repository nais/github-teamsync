package github_team_reconciler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shurcooL/githubv4"
)

func TestIdentityLookupWireFormatAndMissingProvider(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		wantUser    bool
		wantMissing bool
	}{
		{"linked", `{"data":{"organization":{"samlIdentityProvider":{"externalIdentities":{"nodes":[{"user":{"login":"github-user"},"samlIdentity":{"username":"user@example.com"}}]}}}}}`, true, false},
		{"unlinked", `{"data":{"organization":{"samlIdentityProvider":{"externalIdentities":{"nodes":[]}}}}}`, false, false},
		{"no provider", `{"data":{"organization":{"samlIdentityProvider":null}}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			r := &githubTeamReconciler{org: "org", graphClient: githubv4.NewEnterpriseClient(server.URL, server.Client())}
			username, err := r.getGitHubUsernameFromEmail(t.Context(), "user@example.com")
			if tc.wantUser {
				if err != nil {
					t.Fatal(err)
				}
				if username == nil || *username != "github-user" {
					t.Errorf("username = %v, want github-user", username)
				}
			} else if tc.wantMissing {
				if err == nil || !strings.Contains(err.Error(), "refusing membership changes") {
					t.Fatalf("expected membership safeguard error, got %v", err)
				}
			} else {
				if !errors.Is(err, errGitHubUserNotFound) {
					t.Errorf("error = %v, want %v", err, errGitHubUserNotFound)
				}
			}
		})
	}
}
