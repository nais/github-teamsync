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

func TestEnterprisePATIsSeparateFromOrganizationAppAuthentication(t *testing.T) {
	var identities, teamReads, tokens int
	transport := transportFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/orgs/org/installation":
			if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ey") {
				t.Error("installation discovery must use the App JWT")
			}
			return response(req, http.StatusOK, `{"id":42,"account":{"login":"org"}}`), nil
		case "/app/installations/42/access_tokens":
			tokens++
			return response(req, http.StatusCreated, fmt.Sprintf(`{"token":"installation-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))), nil
		case "/orgs/org/teams":
			teamReads++
			if req.Header.Get("Authorization") != "token installation-token" {
				t.Error("organization operations must use the App installation token")
			}
			return response(req, http.StatusOK, `[]`), nil
		case "/graphql":
			identities++
			if req.Header.Get("Authorization") != "Bearer test-enterprise-pat" {
				t.Error("enterprise identity operations must use the PAT")
			}
			return response(req, http.StatusOK, `{"data":{}}`), nil
		default:
			t.Errorf("unexpected request: %s", req.URL.Path)
			return response(req, http.StatusNotFound, `{}`), nil
		}
	})
	app, err := New(t.Context(), appConfig(t), transport)
	if err != nil {
		t.Fatal(err)
	}
	enterprise, err := NewEnterpriseIdentityClient("test-enterprise-pat", transport)
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
	if identities != 1 || teamReads != 2 || tokens != 1 {
		t.Errorf("identity reads, team reads, App tokens = %d, %d, %d; want 1, 2, 1", identities, teamReads, tokens)
	}
}

func TestEnterpriseIdentityClientRejectsInvalidPAT(t *testing.T) {
	for _, token := range []string{"", " ", " leading", "trailing ", "embedded\nnewline", "embedded\ttab"} {
		client, err := NewEnterpriseIdentityClient(token, http.DefaultTransport)
		if err == nil || client != nil {
			t.Fatal("expected invalid PAT error")
		}
		if strings.Contains(err.Error(), "embedded") {
			t.Fatal("error must not include the PAT")
		}
	}
}

func TestEnterpriseIdentityClientRestrictsCredentialDestination(t *testing.T) {
	client, err := NewEnterpriseIdentityClient("test-enterprise-pat", transportFunc(func(req *http.Request) (*http.Response, error) {
		t.Error("disallowed request must not reach transport")
		return response(req, http.StatusOK, `{}`), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, url string }{
		{http.MethodPost, "http://api.github.com/graphql"},
		{http.MethodPost, "https://other.example.com/graphql"},
		{http.MethodPost, "https://api.github.com/orgs/org/teams"},
		{http.MethodGet, "https://api.github.com/graphql"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("expected credential destination error")
		}
		if strings.Contains(err.Error(), "test-enterprise-pat") {
			t.Fatal("error must not include the PAT")
		}
	}
}

func TestEnterpriseIdentityClientDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	client, err := NewEnterpriseIdentityClient("test-enterprise-pat", transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		resp := response(req, http.StatusTemporaryRedirect, "")
		resp.Header.Set("Location", "https://other.example.com/graphql")
		return resp, nil
	}))
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
	if requests != 1 || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatal("enterprise identity client must not follow redirects")
	}
}

func TestEnterpriseIdentityClientPropagatesCancellation(t *testing.T) {
	client, err := NewEnterpriseIdentityClient("test-enterprise-pat", transportFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
