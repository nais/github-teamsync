package githubauth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// New discovers an organization installation using the App's JWT, then uses
// automatically refreshed, installation-scoped tokens.
func New(ctx context.Context, cfg *config.Config, transport http.RoundTripper) (*http.Client, error) {
	discovery, key, err := newAppClient(cfg, transport)
	if err != nil {
		return nil, err
	}
	installation, _, err := discovery.Apps.GetOrganizationInstallation(ctx, cfg.GitHubOrganization)
	if err != nil {
		return nil, fmt.Errorf("find App installation for %q (an organization owner must install the App): %w", cfg.GitHubOrganization, err)
	}
	if installation.GetID() <= 0 || !installation.GetSuspendedAt().IsZero() {
		return nil, fmt.Errorf("app installation for %q is missing or suspended", cfg.GitHubOrganization)
	}
	if !strings.EqualFold(installation.GetAccount().GetLogin(), cfg.GitHubOrganization) {
		return nil, fmt.Errorf("GitHub returned an installation for a different account")
	}
	return newInstallationClient(cfg.GitHubAppID, installation.GetID(), key, transport)
}

func newAppClient(cfg *config.Config, transport http.RoundTripper) (*github.Client, []byte, error) {
	key, err := os.ReadFile(cfg.GitHubPrivateKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read GitHub App key: %w", err)
	}
	apps, err := ghinstallation.NewAppsTransport(transport, cfg.GitHubAppID, key)
	if err != nil {
		return nil, nil, fmt.Errorf("parse GitHub App key: %w", err)
	}
	discovery, err := github.NewClient(github.WithHTTPClient(&http.Client{
		Transport: apps, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
	if err != nil {
		return nil, nil, err
	}
	return discovery, key, nil
}

func newInstallationClient(appID, installationID int64, key []byte, transport http.RoundTripper) (*http.Client, error) {
	authenticated, err := ghinstallation.New(transport, appID, installationID, key)
	if err != nil {
		return nil, fmt.Errorf("create installation transport: %w", err)
	}
	return &http.Client{
		Transport: otelhttp.NewTransport(authenticated), Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
