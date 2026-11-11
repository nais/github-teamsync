package config

import (
	"maps"
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
		NaisAPIURL: "https://console.example.com/graphql", NaisServiceAccountTokenPath: "/var/run/secrets/nais.io/token",
		GitHubOrganization: "example", GitHubAppID: 123, GitHubPrivateKeyFile: "/run/secrets/app.pem",
		ListenAddress: "127.0.0.1:3105", SyncInterval: 30 * time.Minute, SyncTimeout: 30 * time.Minute, TeamTimeout: 2 * time.Minute,
		SyncWorkers: 5, LogFormat: "json", LogLevel: "info",
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("config mismatch (-want +got):\n%s", diff)
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
	for _, key := range []string{"NAIS_API_URL", "NAIS_SERVICE_ACCOUNT_TOKEN_PATH", "GITHUB_ORG", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE"} {
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
		{"NAIS_API_URL", "http://remote.example.com/graphql"},
		{"NAIS_API_URL", "https://user:secret@example.com/graphql"},
		{"NAIS_SERVICE_ACCOUNT_TOKEN_PATH", " \t"},
		{"GITHUB_ORG", "bad/org"},
		{"SYNC_INTERVAL", "0s"},
		{"SYNC_TIMEOUT", "-1s"},
		{"TEAM_TIMEOUT", "invalid"},
		{"SYNC_WORKERS", "0"},
		{"SYNC_WORKERS", "-1"},
		{"SYNC_WORKERS", "invalid"},
		{"DRY_RUN", "invalid"},
		{"LISTEN_ADDRESS", ""},
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

func TestLocalAPIProxy(t *testing.T) {
	env := validEnv()
	env["NAIS_API_URL"] = "http://127.0.0.1:3000/graphql"
	delete(env, "NAIS_SERVICE_ACCOUNT_TOKEN_PATH")
	env["WEBHOOK_SOURCE"] = "https://example.com/api"
	env["WEBHOOK_SECRET"] = "random-test-secret-with-at-least-32-bytes"
	_, err := New(t.Context(), envconfig.MapLookuper(env))
	if err != nil {
		t.Fatal(err)
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
		{"http://remote.example.com/graphql", false},
		{"ftp://console.example.com/graphql", false},
		{"https://user:secret@console.example.com/graphql", false},
		{"https://console.example.com/graphql?token=secret", false},
		{"https://console.example.com/graphql#fragment", false},
		{"https:///graphql", false},
		{"%invalid", false},
		{"", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			if got := validAPIURL(tc.url); got != tc.valid {
				t.Errorf("validAPIURL(%q) = %t, want %t", tc.url, got, tc.valid)
			}
		})
	}
}
