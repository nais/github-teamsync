package config

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"
)

type Config struct {
	NaisAPIURL                  string        `env:"NAIS_API_URL,required"`
	NaisServiceAccountTokenPath string        `env:"NAIS_SERVICE_ACCOUNT_TOKEN_PATH"`
	GitHubOrganization          string        `env:"GITHUB_ORG,required"`
	GitHubAppID                 int64         `env:"GITHUB_APP_ID,required"`
	GitHubPrivateKeyFile        string        `env:"GITHUB_APP_PRIVATE_KEY_FILE,required"`
	ListenAddress               string        `env:"LISTEN_ADDRESS,default=127.0.0.1:3105"`
	SyncInterval                time.Duration `env:"SYNC_INTERVAL,default=30m"`
	SyncTimeout                 time.Duration `env:"SYNC_TIMEOUT,default=30m"`
	TeamTimeout                 time.Duration `env:"TEAM_TIMEOUT,default=2m"`
	SyncWorkers                 int           `env:"SYNC_WORKERS,default=5"`
	DryRun                      bool          `env:"DRY_RUN,default=false"`
	LogFormat                   string        `env:"LOG_FORMAT,default=json"`
	LogLevel                    string        `env:"LOG_LEVEL,default=info"`
	WebhookSecret               string        `env:"WEBHOOK_SECRET"`
	WebhookSource               string        `env:"WEBHOOK_SOURCE"`
}

func New(ctx context.Context, lookuper envconfig.Lookuper) (*Config, error) {
	cfg := &Config{}
	if err := envconfig.ProcessWith(ctx, &envconfig.Config{Target: cfg, Lookuper: lookuper}); err != nil {
		return nil, err
	}
	if !validAPIURL(cfg.NaisAPIURL) {
		return nil, fmt.Errorf("NAIS_API_URL must be an HTTPS GraphQL URL (HTTP allowed only on loopback), without credentials, query or fragment")
	}
	u, _ := url.Parse(cfg.NaisAPIURL) // Already validated, including loopback-only HTTP.
	if cfg.NaisServiceAccountTokenPath == "" && u.Scheme != "http" {
		return nil, fmt.Errorf("NAIS_SERVICE_ACCOUNT_TOKEN_PATH is required for Nais API workload authentication; use nais api proxy on loopback HTTP for local development")
	}
	if cfg.NaisServiceAccountTokenPath != "" && strings.TrimSpace(cfg.NaisServiceAccountTokenPath) == "" {
		return nil, fmt.Errorf("NAIS_SERVICE_ACCOUNT_TOKEN_PATH must not be blank")
	}
	if cfg.ListenAddress == "" || cfg.SyncInterval <= 0 || cfg.SyncTimeout <= 0 || cfg.TeamTimeout <= 0 {
		return nil, fmt.Errorf("listen address and positive sync intervals/timeouts are required")
	}
	if cfg.SyncWorkers <= 0 {
		return nil, fmt.Errorf("SYNC_WORKERS must be positive")
	}
	if (cfg.WebhookSecret == "") != (cfg.WebhookSource == "") {
		return nil, fmt.Errorf("WEBHOOK_SECRET and WEBHOOK_SOURCE must be configured together")
	}
	if cfg.WebhookSecret != "" && len(cfg.WebhookSecret) < 32 {
		return nil, fmt.Errorf("WEBHOOK_SECRET must be at least 32 bytes")
	}
	if cfg.GitHubOrganization == "" || strings.TrimSpace(cfg.GitHubOrganization) != cfg.GitHubOrganization || strings.ContainsAny(cfg.GitHubOrganization, "/\\ \t\r\n,") {
		return nil, fmt.Errorf("GITHUB_ORG must name a single GitHub organization")
	}
	if cfg.GitHubAppID <= 0 {
		return nil, fmt.Errorf("GITHUB_APP_ID must be positive")
	}
	if strings.TrimSpace(cfg.GitHubPrivateKeyFile) == "" {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE must not be blank")
	}
	return cfg, nil
}

func validAPIURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
	}
	return false
}
