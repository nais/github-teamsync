package github_team_reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/mock"
)

var identityModes = []struct {
	provider                                   config.IdentityProvider
	responseFormat, queryPath, variable, scope string
}{
	{config.IdentityProviderOrganizationSAML, `{"organization":{"samlIdentityProvider":%s}}`, `organization(login: $org){samlIdentityProvider`, "org", "org"},
	{config.IdentityProviderEnterpriseSAML, `{"enterprise":{"ownerInfo":{"samlIdentityProvider":%s}}}`, `enterprise(slug: $enterprise){ownerInfo{samlIdentityProvider`, "enterprise", "enterprise"},
	{config.IdentityProviderEnterpriseOIDC, `{"enterprise":{"ownerInfo":{"oidcProvider":%s}}}`, `enterprise(slug: $enterprise){ownerInfo{oidcProvider`, "enterprise", "enterprise"},
}

func TestIdentityLookupWireFormatAndMissingProvider(t *testing.T) {
	for _, mode := range identityModes {
		t.Run(string(mode.provider), func(t *testing.T) {
			for _, tc := range []struct {
				name, provider        string
				wantUser, wantMissing bool
			}{
				{"linked", `{"externalIdentities":{"nodes":[{"user":{"login":"github-user"}}]}}`, true, false},
				{"unlinked", `{"externalIdentities":{"nodes":[]}}`, false, false},
				{"unclaimed", `{"externalIdentities":{"nodes":[{"user":null}]}}`, false, false},
				{"empty login", `{"externalIdentities":{"nodes":[{"user":{"login":""}}]}}`, false, false},
				{"no provider", `null`, false, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
						var request struct {
							Query     string
							Variables map[string]string
						}
						if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
							t.Error(err)
						}
						if !strings.Contains(request.Query, mode.queryPath) || !strings.Contains(request.Query, `externalIdentities(first: 1, userName: $username){nodes{user{login}}}`) {
							t.Errorf("unexpected query: %s", request.Query)
						}
						if len(request.Variables) != 2 || request.Variables[mode.variable] != mode.scope || request.Variables["username"] != "user@example.com" {
							t.Errorf("unexpected variables: %v", request.Variables)
						}
						_, _ = fmt.Fprintf(w, `{"data":%s}`, fmt.Sprintf(mode.responseFormat, tc.provider))
					}))
					defer server.Close()
					r := &githubTeamReconciler{org: "org", enterprise: "enterprise", identityProvider: mode.provider, identityClient: githubv4.NewEnterpriseClient(server.URL, server.Client())}
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
					} else if !errors.Is(err, errGitHubUserNotFound) {
						t.Errorf("error = %v, want %v", err, errGitHubUserNotFound)
					}
				})
			}
		})
	}
}

func TestIdentityFailuresPreventMembershipChanges(t *testing.T) {
	for _, mode := range identityModes {
		for _, dryRun := range []bool{false, true} {
			for _, tc := range []struct {
				name, body string
				status     int
			}{
				{"no provider", fmt.Sprintf(`{"data":%s}`, fmt.Sprintf(mode.responseFormat, "null")), http.StatusOK},
				{"missing connection", fmt.Sprintf(`{"data":%s}`, fmt.Sprintf(mode.responseFormat, `{}`)), http.StatusOK},
				{"null connection", fmt.Sprintf(`{"data":%s}`, fmt.Sprintf(mode.responseFormat, `{"externalIdentities":null}`)), http.StatusOK},
				{"missing nodes", fmt.Sprintf(`{"data":%s}`, fmt.Sprintf(mode.responseFormat, `{"externalIdentities":{}}`)), http.StatusOK},
				{"null nodes", fmt.Sprintf(`{"data":%s}`, fmt.Sprintf(mode.responseFormat, `{"externalIdentities":{"nodes":null}}`)), http.StatusOK},
				{"no root", `{"data":null}`, http.StatusOK},
				{"no owner info", `{"data":{"enterprise":{"ownerInfo":null}}}`, http.StatusOK},
				{"unauthorized", `{"message":"Bad credentials"}`, http.StatusUnauthorized},
				{"forbidden", `{"message":"Forbidden"}`, http.StatusForbidden},
				{"partial response", fmt.Sprintf(`{"data":%s,"errors":[{"message":"Access denied"}]}`, fmt.Sprintf(mode.responseFormat, `{"externalIdentities":{"nodes":[{"user":{"login":"github-user"}}]}}`)), http.StatusOK},
				{"malformed response", `invalid json`, http.StatusOK},
			} {
				if tc.name == "no owner info" && mode.provider == config.IdentityProviderOrganizationSAML {
					continue
				}
				t.Run(fmt.Sprintf("%s/dry_run=%t/%s", mode.provider, dryRun, tc.name), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(tc.status)
						_, _ = io.WriteString(w, tc.body)
					}))
					defer server.Close()
					teams := NewMockTeamsService(t)
					graph := NewMockGraphClient(t)
					expectDirectMembers(t, graph, "child", "old-user")
					r := New("org", teams, graph, WithDryRun(dryRun), WithIdentityProvider(mode.provider, "enterprise", githubv4.NewEnterpriseClient(server.URL, server.Client()))).(*githubTeamReconciler)
					log, output := operationLog()
					r = r.withWritePreview(log)
					err := r.connectUsers(t.Context(), &previewSource{members: []*naisapi.Member{{Email: "user@example.com"}}}, "child", &github.Team{Slug: new("child")}, log)
					if err == nil || errors.Is(err, errGitHubUserNotFound) {
						t.Fatalf("expected identity failure, got %v", err)
					}
					if output.Len() != 0 {
						t.Errorf("failed identity lookup must not plan membership changes: %s", output)
					}
				})
			}
		}
	}
}

func TestIdentityLookupPropagatesCancellation(t *testing.T) {
	for _, mode := range identityModes {
		t.Run(string(mode.provider), func(t *testing.T) {
			graph := NewMockGraphClient(t)
			graph.EXPECT().Query(mock.Anything, mock.Anything, mock.Anything).Return(context.Canceled).Once()
			r := New("org", nil, nil, WithIdentityProvider(mode.provider, "enterprise", graph)).(*githubTeamReconciler)
			if _, err := r.getGitHubUsernameFromEmail(t.Context(), "user@example.com"); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestIdentityLookupDrivesMembershipChanges(t *testing.T) {
	for _, mode := range identityModes {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry_run=%t", mode.provider, dryRun), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprintf(w, `{"data":%s}`, fmt.Sprintf(mode.responseFormat, `{"externalIdentities":{"nodes":[{"user":{"login":"new-user"}}]}}`))
				}))
				defer server.Close()
				teams := NewMockTeamsService(t)
				graph := NewMockGraphClient(t)
				expectDirectMembers(t, graph, "child", "old-user")
				if !dryRun {
					teams.EXPECT().RemoveTeamMembershipBySlug(mock.Anything, "org", "child", "old-user").
						Return(previewResponse(http.StatusNoContent), nil).Once()
					teams.EXPECT().AddTeamMembershipBySlug(mock.Anything, "org", "child", "new-user", github.AddTeamMembershipRequest{}).
						Return(&github.Membership{}, previewResponse(http.StatusOK), nil).Once()
				}
				r := New("org", teams, graph, WithDryRun(dryRun), WithIdentityProvider(mode.provider, "enterprise", githubv4.NewEnterpriseClient(server.URL, server.Client()))).(*githubTeamReconciler)
				log, output := operationLog()
				r = r.withWritePreview(log)
				if err := r.connectUsers(t.Context(), &previewSource{members: []*naisapi.Member{{Email: "user@example.com"}}}, "child", &github.Team{Slug: new("child")}, log); err != nil {
					t.Fatal(err)
				}
				if dryRun {
					checkPreviewOperations(t, output, []string{"remove_team_member", "add_team_member"})
				}
			})
		}
	}
}
