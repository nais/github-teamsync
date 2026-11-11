package githubauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nais/nais-github-teamsync/internal/config"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func appConfig(t *testing.T) *config.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &config.Config{GitHubOrganization: "org", GitHubAppID: 123, GitHubPrivateKeyFile: file}
}

func TestAppInstallationDiscoveryAndTokenRefreshTransport(t *testing.T) {
	cfg := appConfig(t)
	discoveries, tokens, requests := 0, 0, 0
	client, err := New(t.Context(), cfg, transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/orgs/org/installation":
			discoveries++
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Error("expected bearer JWT authentication")
			}
			return response(r, http.StatusOK, `{"id":42,"account":{"login":"org"},"suspended_at":null}`), nil
		case "/app/installations/42/access_tokens":
			tokens++
			if r.Method != http.MethodPost {
				t.Errorf("method = %q, want POST", r.Method)
			}
			return response(r, http.StatusCreated, fmt.Sprintf(`{"token":"installation-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))), nil
		case "/orgs/org/teams":
			requests++
			if got := r.Header.Get("Authorization"); got != "token installation-token" {
				t.Errorf("authorization = %q, want token installation-token", got)
			}
			return response(r, http.StatusOK, `[]`), nil
		default:
			t.Errorf("unexpected GitHub request %s", r.URL.Path)
			return response(r, http.StatusNotFound, `{}`), nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/orgs/org/teams", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if discoveries != 1 || tokens != 1 || requests != 2 {
		t.Errorf("discoveries, tokens, requests = %d, %d, %d; want 1, 1, 2", discoveries, tokens, requests)
	}
}

func TestMissingAppInstallationFailsBeforeSync(t *testing.T) {
	_, err := New(t.Context(), appConfig(t), transportFunc(func(r *http.Request) (*http.Response, error) {
		return response(r, http.StatusNotFound, `{"message":"Not Found"}`), nil
	}))
	if err == nil || !strings.Contains(err.Error(), "must install the App") {
		t.Fatalf("expected missing installation error, got %v", err)
	}
}

func TestInvalidAppKeyFailsBeforeDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, contents, want string
	}{
		{"missing", "", "read GitHub App key"},
		{"empty", "", "parse GitHub App key"},
		{"invalid", "not a private key", "parse GitHub App key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.pem")
			if tc.name != "missing" {
				if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{GitHubOrganization: "org", GitHubAppID: 123, GitHubPrivateKeyFile: path}
			client, err := New(t.Context(), cfg, transportFunc(func(r *http.Request) (*http.Response, error) {
				t.Error("invalid App key must fail before making HTTP requests")
				return response(r, http.StatusInternalServerError, `{}`), nil
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if client != nil {
				t.Error("invalid App key must not return a client")
			}
		})
	}
}

func TestInvalidAppInstallationFailsBeforeTokenCreation(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"missing ID", `{"account":{"login":"org"}}`, "missing or suspended"},
		{"suspended", `{"id":42,"account":{"login":"org"},"suspended_at":"2026-01-01T00:00:00Z"}`, "missing or suspended"},
		{"different account", `{"id":42,"account":{"login":"other"}}`, "different account"},
		{"missing account", `{"id":42}`, "different account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(t.Context(), appConfig(t), transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/orgs/org/installation" {
					t.Errorf("invalid installation must not create access tokens: %s", r.URL.Path)
				}
				return response(r, http.StatusOK, tc.body), nil
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if client != nil {
				t.Error("invalid installation must not return a client")
			}
		})
	}
}
