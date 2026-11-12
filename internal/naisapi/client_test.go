package naisapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer workload-identity-token" {
			t.Error("missing workload identity bearer authentication")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return New(server.URL, testTokenFile(t, "workload-identity-token\n"), server.Client())
}

func testTokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workload-token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTeamsPaginationUsesNaisSlugs(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Query         string
			OperationName string
			Variables     map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(req.Query, "deletionInProgress") {
			t.Error("query must include deletionInProgress")
		}
		if strings.Contains(req.Query, "externalResources") || strings.Contains(req.Query, "gitHubTeam") {
			t.Error("query must not request external GitHub team references")
		}
		if req.OperationName != "Teams" {
			t.Errorf("operation = %q, want Teams", req.OperationName)
		}
		if calls == 1 {
			if req.Variables["after"] != nil {
				t.Errorf("first page cursor = %v, want nil", req.Variables["after"])
			}
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"slug":"one","purpose":"purpose","deletionInProgress":false}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-one"}}}}`)
		} else {
			if got := req.Variables["after"]; got != "cursor-one" {
				t.Errorf("cursor = %v, want cursor-one", got)
			}
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"slug":"two","deletionInProgress":true}],"pageInfo":{"hasNextPage":false,"endCursor":"cursor-two"}}}}`)
		}
	})
	teams, err := c.Teams(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []*Team{
		{Slug: "one", Purpose: "purpose"},
		{Slug: "two", DeletionInProgress: true},
	}
	if diff := cmp.Diff(want, teams); diff != "" {
		t.Errorf("teams mismatch (-want +got):\n%s", diff)
	}
	if calls != 2 {
		t.Errorf("requests = %d, want 2", calls)
	}
}

func TestTeamBySlug(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       *Team
	}{
		{"active", `{"data":{"team":{"slug":"one","purpose":"purpose","deletionInProgress":false}}}`, &Team{Slug: "one", Purpose: "purpose"}},
		{"deleting", `{"data":{"team":{"slug":"one","purpose":"purpose","deletionInProgress":true}}}`, &Team{Slug: "one", Purpose: "purpose", DeletionInProgress: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					OperationName string
					Query         string
					Variables     map[string]any
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.OperationName != "TeamBySlug" || strings.Contains(req.Query, "teams(") {
					t.Errorf("expected a single-team query, got %s", req.Query)
				}
				if strings.Contains(req.Query, "externalResources") || strings.Contains(req.Query, "gitHubTeam") {
					t.Error("query must not request external GitHub team references")
				}
				if diff := cmp.Diff(map[string]any{"slug": "one"}, req.Variables); diff != "" {
					t.Errorf("variables mismatch (-want +got):\n%s", diff)
				}
				_, _ = io.WriteString(w, tc.body)
			})
			team, err := c.Team(t.Context(), "one")
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, team); diff != "" {
				t.Errorf("team mismatch (-want +got):\n%s", diff)
			}
			if calls != 1 {
				t.Errorf("requests = %d, want 1", calls)
			}
		})
	}
}

func TestFailedTeamLookupNeverReturnsDesiredState(t *testing.T) {
	for _, body := range []string{
		`{"data":{"team":null}}`,
		`{"data":{}}`,
		`{"data":{"team":{"slug":""}}}`,
		`{"data":{"team":{"slug":"other","deletionInProgress":true}}}`,
		`{"data":{"team":{"slug":"one","deletionInProgress":true}},"errors":[{"message":"access denied"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			team, err := c.Team(t.Context(), "one")
			if err == nil || team != nil {
				t.Fatalf("failed lookup must return nil and an error, got %v, %v", team, err)
			}
		})
	}
}

func TestMembersPagination(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			OperationName string
			Variables     map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if got := req.Variables["slug"]; got != "team" {
			t.Errorf("slug = %v, want team", got)
		}
		if req.OperationName != "Members" {
			t.Errorf("operation = %q, want Members", req.OperationName)
		}
		if calls == 1 {
			_, _ = io.WriteString(w, `{"data":{"team":{"members":{"nodes":[{"user":{"email":"one@example.com"}}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}}`)
		} else {
			if got := req.Variables["after"]; got != "next" {
				t.Errorf("cursor = %v, want next", got)
			}
			_, _ = io.WriteString(w, `{"data":{"team":{"members":{"nodes":[{"user":{"email":"two@example.com"}}],"pageInfo":{"hasNextPage":false}}}}}`)
		}
	})
	members, err := c.Members(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	want := []*Member{{Email: "one@example.com"}, {Email: "two@example.com"}}
	if diff := cmp.Diff(want, members); diff != "" {
		t.Errorf("members mismatch (-want +got):\n%s", diff)
	}
}

func TestRejectsPartialOrMalformedInventory(t *testing.T) {
	for _, body := range []string{`{`, `{"data":null}`, `{"data":{"teams":null}}`, `{"data":{"teams":{"nodes":[],"pageInfo":null}}}`, `{"data":{"teams":{"nodes":null,"pageInfo":{"hasNextPage":false}}}}`, `{"data":{"teams":{"nodes":[null],"pageInfo":{"hasNextPage":false}}}}`, `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":true}}}}`, `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}},"errors":[{"message":"access denied"}]}`} {
		t.Run(body, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			teams, err := c.Teams(t.Context())
			if err == nil {
				t.Fatal("expected inventory error")
			}
			if teams != nil {
				t.Errorf("failed inventory must be nil, got %v", teams)
			}
		})
	}
}

func TestFailureOnLaterPageDiscardsPartialResults(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"slug":"one"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}`)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	teams, err := c.Teams(t.Context())
	if err == nil {
		t.Fatal("expected later page error")
	}
	if teams != nil {
		t.Errorf("partial inventory must be nil, got %v", teams)
	}
}

func TestMembershipErrorsCannotBecomeEmptyMembership(t *testing.T) {
	for _, body := range []string{`{"data":{"team":null}}`, `{"data":{"team":{"members":null}}}`, `{"data":{"team":{"members":{"nodes":[],"pageInfo":null}}}}`, `{"data":{"team":{"members":{"nodes":null,"pageInfo":{"hasNextPage":false}}}}}`, `{"data":{"team":{"members":{"nodes":[null],"pageInfo":{"hasNextPage":false}}}}}`, `{"data":{"team":{"members":{"nodes":[{"user":null}],"pageInfo":{"hasNextPage":false}}}}}`, `{"data":null,"errors":[{"message":"not found"}]}`} {
		c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		members, err := c.Members(t.Context(), "team")
		if err == nil {
			t.Fatal("expected membership error")
		}
		if members != nil {
			t.Errorf("failed membership inventory must be nil, got %v", members)
		}
	}
}

func TestEmptyInventoryAndAuthenticationFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
	})
	teams, err := c.Teams(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 0 {
		t.Errorf("expected empty inventory, got %v", teams)
	}
	c = testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "sensitive-server-response")
	})
	_, err = c.Teams(t.Context())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected HTTP 401 error, got %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-server-response") {
		t.Error("error must not expose upstream response")
	}
}

func TestDuplicateSlugsAndTrailingJSONAreRejected(t *testing.T) {
	for _, body := range []string{
		`{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}}} {}`,
		`{"data":{"teams":{"nodes":[{"slug":"one"},{"slug":"one"}],"pageInfo":{"hasNextPage":false}}}}`,
		`{"data":{"teams":{"nodes":[{"slug":"one"},{"slug":"ONE"}],"pageInfo":{"hasNextPage":false}}}}`,
	} {
		c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		teams, err := c.Teams(t.Context())
		if err == nil {
			t.Fatal("expected inventory error")
		}
		if teams != nil {
			t.Errorf("failed inventory must be nil, got %v", teams)
		}
	}
}

func TestRedirectsCannotForwardServiceAccountCredentials(t *testing.T) {
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c := New(server.URL, testTokenFile(t, "workload-identity-token"), server.Client())
	_, err := c.Teams(t.Context())
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("expected HTTP 307 error, got %v", err)
	}
	if forwarded {
		t.Error("redirect must not forward credentials")
	}
}

func TestWorkloadTokenIsReopenedForEveryRequestAndPage(t *testing.T) {
	path := testTokenFile(t, "token-before-client-construction")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			if got := r.Header.Get("Authorization"); got != "Bearer first-request-token" {
				t.Errorf("authorization = %q, want Bearer first-request-token", got)
			}
			// Simulate a projected token being replaced while a listing is paginated.
			replacement := path + ".new"
			if err := os.WriteFile(replacement, []byte("second-page-token\n"), 0o600); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Error(err)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"slug":"one"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}`)
		case 2:
			if got := r.Header.Get("Authorization"); got != "Bearer second-page-token" {
				t.Errorf("authorization = %q, want Bearer second-page-token", got)
			}
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"slug":"two"}],"pageInfo":{"hasNextPage":false}}}}`)
		case 3:
			if got := r.Header.Get("Authorization"); got != "Bearer membership-request-token" {
				t.Errorf("authorization = %q, want Bearer membership-request-token", got)
			}
			_, _ = io.WriteString(w, `{"data":{"team":{"members":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`)
		case 4:
			if got := r.Header.Get("Authorization"); got != "Bearer targeted-request-token" {
				t.Errorf("authorization = %q, want Bearer targeted-request-token", got)
			}
			_, _ = io.WriteString(w, `{"data":{"team":{"slug":"one"}}}`)
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	c := New(server.URL, path, server.Client())
	if err := os.WriteFile(path, []byte("first-request-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	teams, err := c.Teams(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 2 {
		t.Fatalf("teams = %d, want 2", len(teams))
	}
	// Simulate in-place rotation before a different GraphQL operation.
	if err := os.WriteFile(path, []byte("membership-request-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = c.Members(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("targeted-request-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Team(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if team, err := c.Team(t.Context(), "one"); err == nil || team != nil {
		t.Fatalf("missing token must fail closed, got %v, %v", team, err)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("requests = %d, want 4; token failure must not make a request", got)
	}
}

func TestTokenReadFailuresNeverReusePreviousCredentials(t *testing.T) {
	for _, state := range []string{"missing", "empty", "invalid whitespace", "directory"} {
		t.Run(state, func(t *testing.T) {
			path := testTokenFile(t, "initial-token")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if call == 1 {
					if got := r.Header.Get("Authorization"); got != "Bearer initial-token" {
						t.Errorf("authorization = %q, want Bearer initial-token", got)
					}
				} else {
					if got := r.Header.Get("Authorization"); got != "Bearer restored-token" {
						t.Errorf("authorization = %q, want Bearer restored-token", got)
					}
				}
				_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
			}))
			defer server.Close()
			c := New(server.URL, path, server.Client())
			_, err := c.Teams(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "invalid whitespace":
				if err := os.WriteFile(path, []byte("sensitive-token\ninjected-header"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			teams, err := c.Teams(t.Context())
			if err == nil {
				t.Fatal("expected token read error")
			}
			if strings.Contains(err.Error(), "sensitive-token") {
				t.Error("error must not expose token")
			}
			if teams != nil {
				t.Errorf("failed inventory must be nil, got %v", teams)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("authentication failure must not make an HTTP request: requests = %d, want 1", got)
			}
			if state == "directory" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("restored-token"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = c.Teams(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := calls.Load(); got != 2 {
				t.Errorf("requests = %d, want 2", got)
			}
		})
	}
}

func TestLocalAPIProxyNeedsNoTokenFileOrAuthorizationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("nais api proxy supplies its own authentication")
		}
		_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
	}))
	defer server.Close()
	c := New(server.URL, "", server.Client())
	_, err := c.Teams(t.Context())
	if err != nil {
		t.Fatal(err)
	}
}

func TestNonProxyEndpointsRequireWorkloadToken(t *testing.T) {
	for _, endpoint := range []string{"https://console.example.com/graphql", "https://localhost/graphql", "http://remote.example.com/graphql"} {
		t.Run(endpoint, func(t *testing.T) {
			// Authentication fails before any network request is attempted.
			c := New(endpoint, "", &http.Client{})
			teams, err := c.Teams(t.Context())
			if err == nil || !strings.Contains(err.Error(), "workload identity token path is required") {
				t.Fatalf("expected missing workload token error, got %v", err)
			}
			if teams != nil {
				t.Errorf("failed inventory must be nil, got %v", teams)
			}
		})
	}
}

func TestMissingTokenOnLaterPageDiscardsPartialInventory(t *testing.T) {
	path := testTokenFile(t, "workload-token")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if err := os.Remove(path); err != nil {
			t.Error(err)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"slug":"partial"}],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}`)
	}))
	defer server.Close()
	c := New(server.URL, path, server.Client())
	teams, err := c.Teams(t.Context())
	if err == nil || !strings.Contains(err.Error(), "read Nais workload identity token") {
		t.Fatalf("expected token read error, got %v", err)
	}
	if teams != nil {
		t.Errorf("partial results must never be treated as the full desired inventory, got %v", teams)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestGraphQLErrorsAreSanitized(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[],"pageInfo":{"hasNextPage":false}}},"errors":[{"message":"sensitive-member-data","extensions":{"credential":"sensitive-token"}}]}`)
	})
	teams, err := c.Teams(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refusing partial data") {
		t.Fatalf("expected partial response error, got %v", err)
	}
	if strings.Contains(err.Error(), "sensitive-") {
		t.Error("GraphQL error must not expose upstream messages or extensions")
	}
	if teams != nil {
		t.Errorf("partial inventory must be nil, got %v", teams)
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", (16<<20)+1))
	})
	teams, err := c.Teams(t.Context())
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected response size limit error, got %v", err)
	}
	if teams != nil {
		t.Errorf("oversized inventory must be nil, got %v", teams)
	}
}

func TestRequestsHonorCancellation(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("canceled request must not reach the server")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	teams, err := c.Teams(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if teams != nil {
		t.Errorf("canceled inventory must be nil, got %v", teams)
	}
}
