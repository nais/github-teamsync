package githubauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const enterpriseInstallation = `{"id":84,"target_type":"Enterprise","account":{"slug":"enterprise"},"suspended_at":null}`

func TestEnterpriseAndOrganizationUseSeparateAppInstallations(t *testing.T) {
	var identities, teamReads, orgTokens, enterpriseTokens, enterpriseDiscoveries int
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/orgs/org/installation", "/enterprises/enterprise/installation":
			if req.Method != http.MethodGet || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ey") {
				t.Error("installation discovery must use the App JWT")
			}
			if req.URL.Path == "/enterprises/enterprise/installation" {
				enterpriseDiscoveries++
				return response(req, http.StatusOK, enterpriseInstallation), nil
			}
			return response(req, http.StatusOK, `{"id":42,"account":{"login":"org"}}`), nil
		case "/app/installations/42/access_tokens", "/app/installations/84/access_tokens":
			if req.Method != http.MethodPost || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ey") {
				t.Error("installation token creation must use the App JWT")
			}
			token := "org-token"
			expiry := time.Now().Add(time.Hour)
			if req.URL.Path == "/app/installations/84/access_tokens" {
				enterpriseTokens++
				token = fmt.Sprintf("enterprise-token-%d", enterpriseTokens)
				if enterpriseTokens == 1 {
					// Force a refresh on the next read, without sleeping or live services.
					expiry = time.Now().Add(30 * time.Second)
				}
			} else {
				orgTokens++
			}
			return response(req, http.StatusCreated, fmt.Sprintf(`{"token":%q,"expires_at":%q}`, token, expiry.UTC().Format(time.RFC3339))), nil
		case "/orgs/org/teams":
			teamReads++
			if req.Header.Get("Authorization") != "token org-token" {
				t.Error("organization operations must use the organization installation token")
			}
			return response(req, http.StatusOK, `[]`), nil
		case "/graphql":
			identities++
			if req.Header.Get("Authorization") != fmt.Sprintf("token enterprise-token-%d", enterpriseTokens) {
				t.Error("enterprise identity operations must use the current enterprise installation token")
			}
			return response(req, http.StatusOK, `{"data":{}}`), nil
		default:
			t.Errorf("unexpected request: %s", req.URL.Path)
			return response(req, http.StatusNotFound, `{}`), nil
		}
	})
	cfg := appConfig(t)
	cfg.GitHubEnterprise = "enterprise"
	app, err := New(t.Context(), cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	enterprise, err := NewEnterpriseIdentityClient(t.Context(), cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		client       *http.Client
		method, path string
	}{
		{app, http.MethodGet, "/orgs/org/teams"},
		{enterprise, http.MethodPost, "/graphql"},
		{app, http.MethodGet, "/orgs/org/teams"},
		{enterprise, http.MethodPost, "/graphql"},
		{enterprise, http.MethodPost, "/graphql"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), request.method, "https://api.github.com"+request.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := request.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if req.Header.Get("Authorization") != "" {
			t.Error("authentication must not mutate the caller's request")
		}
	}
	if identities != 3 || teamReads != 2 || orgTokens != 1 || enterpriseTokens != 2 || enterpriseDiscoveries != 1 {
		t.Errorf("identity reads, team reads, org tokens, enterprise tokens, enterprise discoveries = %d, %d, %d, %d, %d; want 3, 2, 1, 2, 1", identities, teamReads, orgTokens, enterpriseTokens, enterpriseDiscoveries)
	}
}

func TestInvalidEnterpriseInstallationFailsBeforeTokenCreation(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"missing", `{"message":"Not Found"}`, "an enterprise owner must install the App", http.StatusNotFound},
		{"forbidden", `{"message":"Forbidden"}`, "find App installation for enterprise", http.StatusForbidden},
		{"missing ID", `{"target_type":"Enterprise","account":{"slug":"enterprise"}}`, "missing or suspended", http.StatusOK},
		{"suspended", `{"id":84,"target_type":"Enterprise","account":{"slug":"enterprise"},"suspended_at":"2026-01-01T00:00:00Z"}`, "missing or suspended", http.StatusOK},
		{"different account", `{"id":84,"target_type":"Enterprise","account":{"slug":"other"}}`, "different enterprise", http.StatusOK},
		{"missing account", `{"id":84,"target_type":"Enterprise"}`, "different enterprise", http.StatusOK},
		{"missing slug", `{"id":84,"target_type":"Enterprise","account":{"login":"enterprise"}}`, "different enterprise", http.StatusOK},
		{"organization target", `{"id":84,"target_type":"Organization","account":{"slug":"enterprise"}}`, "different enterprise", http.StatusOK},
		{"missing target type", `{"id":84,"account":{"slug":"enterprise"}}`, "different enterprise", http.StatusOK},
		{"malformed", `invalid json`, "find App installation for enterprise", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := appConfig(t)
			cfg.GitHubEnterprise = "enterprise"
			client, err := NewEnterpriseIdentityClient(t.Context(), cfg, transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/enterprises/enterprise/installation" {
					t.Errorf("invalid enterprise installation must not create tokens or fall back: %s", req.URL.Path)
				}
				return response(req, tc.status, tc.body), nil
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if client != nil {
				t.Error("invalid enterprise installation must not return a client")
			}
		})
	}
}

func TestEnterpriseTokenFailureDoesNotFallBack(t *testing.T) {
	cfg := appConfig(t)
	cfg.GitHubEnterprise = "enterprise"
	client, err := NewEnterpriseIdentityClient(t.Context(), cfg, transportFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/enterprises/enterprise/installation":
			return response(req, http.StatusOK, enterpriseInstallation), nil
		case "/app/installations/84/access_tokens":
			return response(req, http.StatusForbidden, `{"message":"Forbidden"}`), nil
		default:
			t.Errorf("token failure must not make identity requests or fall back: %s", req.URL.Path)
			return response(req, http.StatusNotFound, `{}`), nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.github.com/graphql", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected enterprise installation token error")
	}
}

func TestEnterpriseIdentityClientDoesNotFollowRedirects(t *testing.T) {
	for _, discoveryRedirect := range []bool{false, true} {
		t.Run(fmt.Sprintf("discovery=%t", discoveryRedirect), func(t *testing.T) {
			cfg := appConfig(t)
			cfg.GitHubEnterprise = "enterprise"
			redirects := 0
			client, err := NewEnterpriseIdentityClient(t.Context(), cfg, transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "api.github.com" {
					t.Error("must not follow redirects to other hosts")
				}
				if req.URL.Path == "/enterprises/enterprise/installation" && !discoveryRedirect {
					return response(req, http.StatusOK, enterpriseInstallation), nil
				}
				if req.URL.Path == "/app/installations/84/access_tokens" {
					return response(req, http.StatusCreated, fmt.Sprintf(`{"token":"enterprise-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))), nil
				}
				redirects++
				resp := response(req, http.StatusTemporaryRedirect, "")
				resp.Header.Set("Location", "https://other.example.com/graphql")
				return resp, nil
			}))
			if discoveryRedirect {
				if err == nil || client != nil || redirects != 1 {
					t.Fatal("discovery redirect must fail without following it")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.github.com/graphql", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if redirects != 1 || resp.StatusCode != http.StatusTemporaryRedirect {
				t.Fatal("enterprise identity client must not follow redirects")
			}
		})
	}
}

func TestEnterpriseInstallationDiscoveryPropagatesCancellation(t *testing.T) {
	cfg := appConfig(t)
	cfg.GitHubEnterprise = "enterprise"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client, err := NewEnterpriseIdentityClient(ctx, cfg, transportFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	}))
	if !errors.Is(err, context.Canceled) || client != nil {
		t.Fatalf("expected canceled discovery, got client=%v, error=%v", client, err)
	}
}
