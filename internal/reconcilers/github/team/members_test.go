package github_team_reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/mock"
)

func expectDirectMembers(t *testing.T, graph *MockGraphClient, slug string, logins ...string) {
	t.Helper()
	nodes := make([]map[string]string, 0, len(logins))
	for _, login := range logins {
		nodes = append(nodes, map[string]string{"login": login})
	}
	data, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	graph.EXPECT().Query(mock.Anything, mock.AnythingOfType("*github_team_reconciler.teamMembersQuery"), map[string]any{
		"org": githubv4.String("org"), "slug": githubv4.String(slug), "after": (*githubv4.String)(nil),
	}).Run(func(_ context.Context, query any, _ map[string]any) {
		if err := json.Unmarshal(fmt.Appendf(nil, `{"organization":{"team":{"members":{"nodes":%s,"pageInfo":{"hasNextPage":false}}}}}`, data), query); err != nil {
			t.Fatal(err)
		}
	}).Return(nil).Once()
}

type membersTransport func(*http.Request) (*http.Response, error)

func (f membersTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func membersClient(t *testing.T, responses ...string) GraphClient {
	t.Helper()
	calls := 0
	t.Cleanup(func() {
		if calls != len(responses) {
			t.Errorf("membership requests = %d, want %d", calls, len(responses))
		}
	})
	return githubv4.NewClient(&http.Client{Transport: membersTransport(func(req *http.Request) (*http.Response, error) {
		var request struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Query, `members(first: 100, after: $after, membership: IMMEDIATE)`) {
			t.Errorf("query must exclude inherited members: %s", request.Query)
		}
		if request.Variables["org"] != "org" || request.Variables["slug"] != "child" {
			t.Errorf("unexpected membership scope: %v", request.Variables)
		}
		var wantCursor any
		if calls > 0 {
			wantCursor = "next"
		}
		if request.Variables["after"] != wantCursor {
			t.Errorf("cursor = %v, want %v", request.Variables["after"], wantCursor)
		}
		if calls >= len(responses) {
			t.Fatal("unexpected extra membership query")
		}
		body := responses[calls]
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
}

func TestDirectTeamMembersPagination(t *testing.T) {
	r := New("org", nil, membersClient(t,
		`{"data":{"organization":{"team":{"members":{"nodes":[{"login":"direct-user"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[{"login":"direct-and-child-user"}],"pageInfo":{"hasNextPage":false}}}}}}`,
	)).(*githubTeamReconciler)
	members, err := r.getTeamMembers(t.Context(), "child")
	if err != nil {
		t.Fatal(err)
	}
	var logins []string
	for _, member := range members {
		logins = append(logins, member.GetLogin())
	}
	if !slices.Equal(logins, []string{"direct-user", "direct-and-child-user"}) {
		t.Errorf("unexpected direct members: %v", logins)
	}
}

func TestDirectTeamMemberFailuresPreventWrites(t *testing.T) {
	for _, body := range []string{
		`{"data":{"organization":null}}`,
		`{"data":{"organization":{"team":null}}}`,
		`{"data":{"organization":{"team":{"members":null}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":null,"pageInfo":{"hasNextPage":false}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[],"pageInfo":null}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[],"pageInfo":{}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[null],"pageInfo":{"hasNextPage":false}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[{"login":""}],"pageInfo":{"hasNextPage":false}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":""}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}}`,
		`{"data":{"organization":{"team":{"members":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}},"errors":[{"message":"forbidden"}]}`,
		`invalid json`,
	} {
		t.Run(body, func(t *testing.T) {
			graph := membersClient(t,
				`{"data":{"organization":{"team":{"members":{"nodes":[{"login":"old-user"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}}`,
				body,
			)
			r := New("org", NewMockTeamsService(t), graph).(*githubTeamReconciler)
			log, _ := operationLog()
			err := r.connectUsers(t.Context(), &previewSource{}, "child", &github.Team{Slug: new("child")}, log)
			if err == nil {
				t.Fatal("expected incomplete inventory to prevent membership writes")
			}
		})
	}
}

func TestReconcileOnlyDirectMemberships(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry_run=%t", dryRun), func(t *testing.T) {
			// Inherited-only users are excluded by IMMEDIATE; dual members remain direct.
			graph := membersClient(t, `{"data":{"organization":{"team":{"members":{"nodes":[{"login":"direct-and-child-user"}],"pageInfo":{"hasNextPage":false}}}}}}`)
			identity := NewMockGraphClient(t)
			identity.EXPECT().Query(mock.Anything, mock.AnythingOfType("*github_team_reconciler.LookupEnterpriseSAMLUserByEmail"), mock.Anything).
				Run(func(_ context.Context, q any, _ map[string]any) {
					query := q.(*LookupEnterpriseSAMLUserByEmail)
					query.Enterprise.OwnerInfo.SamlIdentityProvider = &ExternalIdentityProvider{
						ExternalIdentities: &ExternalIdentityConnection{Nodes: []ExternalIdentity{{User: GitHubUser{Login: "inherited-user"}}}},
					}
				}).Return(nil).Once()
			teams := NewMockTeamsService(t)
			if !dryRun {
				teams.EXPECT().RemoveTeamMembershipBySlug(mock.Anything, "org", "child", "direct-and-child-user").
					Return(previewResponse(http.StatusNoContent), nil).Once()
				teams.EXPECT().AddTeamMembershipBySlug(mock.Anything, "org", "child", "inherited-user", github.AddTeamMembershipRequest{}).
					Return(&github.Membership{}, previewResponse(http.StatusOK), nil).Once()
			}
			r := New("org", teams, graph, WithDryRun(dryRun), WithIdentityProvider(config.IdentityProviderEnterpriseSAML, "enterprise", identity)).(*githubTeamReconciler)
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

func TestDirectTeamMembersCancellation(t *testing.T) {
	graph := NewMockGraphClient(t)
	graph.EXPECT().Query(mock.Anything, mock.Anything, mock.Anything).Return(context.Canceled).Once()
	r := New("org", nil, graph).(*githubTeamReconciler)
	members, err := r.getTeamMembers(t.Context(), "child")
	if !errors.Is(err, context.Canceled) || members != nil {
		t.Fatalf("members = %v, error = %v, want cancellation without inventory", members, err)
	}
}
