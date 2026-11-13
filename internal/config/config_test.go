package config

import (
	"bytes"
	"log/slog"
	"maps"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/sethvargo/go-envconfig"
)

func validEnv() map[string]string {
	return map[string]string{"NAIS_API_URL": "https://console.example.com/graphql", "NAIS_SERVICE_ACCOUNT_TOKEN_PATH": "/var/run/secrets/nais.io/token", "GITHUB_ORG": "example", "GITHUB_APP_ID": "123", "GITHUB_APP_PRIVATE_KEY_FILE": "/run/secrets/app.pem"}
}

func TestAppDefaults(t *testing.T) {
	cfg, err := New(t.Context(), envconfig.MapLookuper(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		NaisAPIURL: &url.URL{Scheme: "https", Host: "console.example.com", Path: "/graphql"}, NaisServiceAccountTokenPath: "/var/run/secrets/nais.io/token",
		GitHubOrganization: "example", GitHubAppID: 123, GitHubPrivateKeyFile: "/run/secrets/app.pem",
		GitHubIdentityProvider: IdentityProviderOrganizationSAML,
		ListenAddress:          "127.0.0.1:3105", SyncInterval: 30 * time.Minute, SyncTimeout: 30 * time.Minute, TeamTimeout: 2 * time.Minute,
		SyncWorkers: 5, LogFormat: "json", LogLevel: slog.LevelInfo,
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
	}
}

func TestEnterpriseIdentityConfig(t *testing.T) {
	for _, provider := range []IdentityProvider{IdentityProviderEnterpriseSAML, IdentityProviderEnterpriseOIDC} {
		t.Run(string(provider), func(t *testing.T) {
			env := validEnv()
			env["GITHUB_IDENTITY_PROVIDER"] = string(provider)
			env["GITHUB_ENTERPRISE"] = "example-enterprise"
			env["GITHUB_ENTERPRISE_TOKEN"] = "test-enterprise-pat"
			cfg, err := New(t.Context(), envconfig.MapLookuper(env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.GitHubIdentityProvider != provider || cfg.GitHubEnterprise != "example-enterprise" || cfg.GitHubEnterpriseToken != "test-enterprise-pat" {
				t.Fatal("enterprise identity configuration was not preserved")
			}
			for _, key := range []string{"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE", "GITHUB_ENTERPRISE", "GITHUB_ENTERPRISE_TOKEN"} {
				t.Run("missing "+key, func(t *testing.T) {
					missing := maps.Clone(env)
					delete(missing, key)
					if _, err := New(t.Context(), envconfig.MapLookuper(missing)); err == nil {
						t.Fatal("expected missing configuration error")
					}
				})
			}
		})
	}
}

func TestInvalidIdentityConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"unknown provider", map[string]string{"GITHUB_IDENTITY_PROVIDER": "automatic"}},
		{"empty provider", map[string]string{"GITHUB_IDENTITY_PROVIDER": ""}},
		{"unused enterprise", map[string]string{"GITHUB_ENTERPRISE": "example"}},
		{"unused PAT", map[string]string{"GITHUB_ENTERPRISE_TOKEN": "test-enterprise-pat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			maps.Copy(env, tc.env)
			if _, err := New(t.Context(), envconfig.MapLookuper(env)); err == nil {
				t.Fatal("expected identity configuration error")
			}
		})
	}
	for _, key := range []string{"GITHUB_ENTERPRISE", "GITHUB_ENTERPRISE_TOKEN"} {
		values := []string{"", " ", " leading", "trailing ", "has space", "line\nbreak", "tab\tvalue"}
		if key == "GITHUB_ENTERPRISE" {
			values = append(values, "bad/slug", `bad\slug`, "one,two")
		}
		for _, value := range values {
			t.Run(key+value, func(t *testing.T) {
				env := validEnv()
				env["GITHUB_IDENTITY_PROVIDER"] = "enterprise-oidc"
				env["GITHUB_ENTERPRISE"] = "example"
				env["GITHUB_ENTERPRISE_TOKEN"] = "test-enterprise-pat"
				env[key] = value
				if _, err := New(t.Context(), envconfig.MapLookuper(env)); err == nil {
					t.Fatal("expected identity configuration error")
				}
			})
		}
	}
}

func TestSyncWorkers(t *testing.T) {
	for _, count := range []int{1, 5, 10} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			env := validEnv()
			env["SYNC_WORKERS"] = strconv.Itoa(count)
			cfg, err := New(t.Context(), envconfig.MapLookuper(env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SyncWorkers != count {
				t.Fatalf("workers = %d, want %d", cfg.SyncWorkers, count)
			}
		})
	}
}

func TestDryRun(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"false", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			env := validEnv()
			env["DRY_RUN"] = tc.value
			cfg, err := New(t.Context(), envconfig.MapLookuper(env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DryRun != tc.want {
				t.Errorf("dry run = %t, want %t", cfg.DryRun, tc.want)
			}
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, key := range []string{"NAIS_API_URL", "GITHUB_ORG"} {
		t.Run("missing "+key, func(t *testing.T) {
			env := validEnv()
			delete(env, key)
			_, err := New(t.Context(), envconfig.MapLookuper(env))
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
	for _, tc := range []struct{ key, value string }{
		{"NAIS_SERVICE_ACCOUNT_TOKEN_PATH", " \t"},
		{"TEAM_TIMEOUT", "invalid"},
		{"SYNC_WORKERS", "0"},
		{"SYNC_WORKERS", "-1"},
		{"SYNC_WORKERS", "invalid"},
		{"DRY_RUN", "invalid"},
		{"LOG_LEVEL", "invalid"},
		{"LOG_LEVEL", ""},
		{"WEBHOOK_SECRET", "secret-without-source"},
		{"WEBHOOK_SOURCE", "https://example.com/api"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := New(t.Context(), envconfig.MapLookuper(env))
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestListenAddressAndTimeoutValidation(t *testing.T) {
	for _, tc := range []struct{ key, value, wantError string }{
		{"LISTEN_ADDRESS", "", "LISTEN_ADDRESS must not be empty"},
		{"SYNC_INTERVAL", "0s", "sync intervals/timeouts must be positive"},
		{"SYNC_TIMEOUT", "-1s", "sync intervals/timeouts must be positive"},
		{"TEAM_TIMEOUT", "0s", "sync intervals/timeouts must be positive"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := New(t.Context(), envconfig.MapLookuper(env))
			if err == nil || err.Error() != tc.wantError {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestInvalidOrganization(t *testing.T) {
	for _, value := range []string{"", " ", " leading", "trailing ", "has space", "bad/org", `bad\org`, "one,two", "one\ntwo"} {
		t.Run(value, func(t *testing.T) {
			env := validEnv()
			env["GITHUB_ORG"] = value
			_, err := New(t.Context(), envconfig.MapLookuper(env))
			if err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestInvalidGitHubAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth map[string]string
	}{
		{"missing credentials", nil},
		{"missing private key", map[string]string{"GITHUB_APP_ID": "123"}},
		{"missing App ID", map[string]string{"GITHUB_APP_PRIVATE_KEY_FILE": "/keys/app.pem"}},
		{"negative App ID", map[string]string{"GITHUB_APP_ID": "-1", "GITHUB_APP_PRIVATE_KEY_FILE": "/keys/app.pem"}},
		{"zero App ID", map[string]string{"GITHUB_APP_ID": "0", "GITHUB_APP_PRIVATE_KEY_FILE": "/keys/app.pem"}},
		{"invalid App ID", map[string]string{"GITHUB_APP_ID": "invalid", "GITHUB_APP_PRIVATE_KEY_FILE": "/keys/app.pem"}},
		{"blank private key file", map[string]string{"GITHUB_APP_ID": "123", "GITHUB_APP_PRIVATE_KEY_FILE": " \t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			delete(env, "GITHUB_APP_ID")
			delete(env, "GITHUB_APP_PRIVATE_KEY_FILE")
			maps.Copy(env, tc.auth)
			if _, err := New(t.Context(), envconfig.MapLookuper(env)); err == nil {
				t.Fatal("expected GitHub authentication configuration error")
			}
		})
	}
}

func TestWebhookSecretPreservesRawBytes(t *testing.T) {
	for _, tc := range []struct{ name, secret string }{
		{"short", "x"},
		{"special bytes", " \tsecret,with:delimiters+/=\næøå "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["WEBHOOK_SECRET"] = tc.secret
			env["WEBHOOK_SOURCE"] = "https://example.com/api"
			cfg, err := New(t.Context(), envconfig.MapLookuper(env))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cfg.WebhookSecret, []byte(tc.secret)) {
				t.Fatal("webhook secret was not preserved")
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG+2", slog.LevelDebug + 2},
	} {
		t.Run(tc.value, func(t *testing.T) {
			env := validEnv()
			env["LOG_LEVEL"] = tc.value
			cfg, err := New(t.Context(), envconfig.MapLookuper(env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LogLevel != tc.want {
				t.Errorf("log level = %v, want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

func TestOnlyLoopbackHTTPAllowsMissingWorkloadToken(t *testing.T) {
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"http://localhost:4242/graphql", true},
		{"http://127.0.0.1:4242/graphql", true},
		{"http://[::1]:4242/graphql", true},
		{"https://localhost/graphql", false},
		{"https://console.example.com/graphql", false},
		{"http://remote.example.com/graphql", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			env := validEnv()
			env["NAIS_API_URL"] = tc.url
			delete(env, "NAIS_SERVICE_ACCOUNT_TOKEN_PATH")
			_, err := New(t.Context(), envconfig.MapLookuper(env))
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("expected configuration error")
				}
			}
		})
	}
}

func TestAPIURLValidation(t *testing.T) {
	for _, tc := range []struct {
		url   string
		valid bool
	}{
		{"https://console.example.com/graphql", true},
		{"http://localhost:3000/graphql", true},
		{"http://127.0.0.1:3000/graphql", true},
		{"http://[::1]:3000/graphql", true},
		{"http://remote.example.com/graphql", true},
		{"ftp://console.example.com/graphql", true},
		{"https://user:secret@console.example.com/graphql", true},
		{"https://console.example.com/graphql?option=value", true},
		{"https://console.example.com/graphql#fragment", true},
		{"https:///graphql", true},
		{"%invalid", false},
		{"", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			env := validEnv()
			env["NAIS_API_URL"] = tc.url
			cfg, err := New(t.Context(), envconfig.MapLookuper(env))
			if (err == nil) != tc.valid {
				t.Fatalf("New with NAIS_API_URL %q: error = %v, want valid %t", tc.url, err, tc.valid)
			}
			if tc.valid {
				if cfg.NaisAPIURL.String() != tc.url {
					t.Errorf("NAIS_API_URL = %q, want %q", cfg.NaisAPIURL, tc.url)
				}
			}
		})
	}
}
