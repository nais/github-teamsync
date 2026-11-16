package githubauth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
)

// NewEnterpriseIdentityClient discovers the enterprise installation of the same
// App used for organization operations and refreshes its installation tokens.
func NewEnterpriseIdentityClient(ctx context.Context, cfg *config.Config, transport http.RoundTripper) (*http.Client, error) {
	discovery, key, err := newAppClient(cfg, transport)
	if err != nil {
		return nil, err
	}
	req, err := discovery.NewRequest(ctx, http.MethodGet, fmt.Sprintf("enterprises/%s/installation", cfg.GitHubEnterprise), nil)
	if err != nil {
		return nil, err
	}
	// go-github's Installation.Account is a User, which discards the enterprise
	// slug. Decode the enterprise account explicitly so we can verify its scope.
	var installation struct {
		ID          int64              `json:"id"`
		Account     *github.Enterprise `json:"account"`
		TargetType  string             `json:"target_type"`
		SuspendedAt *github.Timestamp  `json:"suspended_at"`
	}
	if _, err := discovery.Do(req, &installation); err != nil {
		return nil, fmt.Errorf("find App installation for enterprise %q (an enterprise owner must install the App): %w", cfg.GitHubEnterprise, err)
	}
	if installation.ID <= 0 || (installation.SuspendedAt != nil && !installation.SuspendedAt.IsZero()) {
		return nil, fmt.Errorf("app installation for enterprise %q is missing or suspended", cfg.GitHubEnterprise)
	}
	if installation.TargetType != "Enterprise" || !strings.EqualFold(installation.Account.GetSlug(), cfg.GitHubEnterprise) {
		return nil, fmt.Errorf("GitHub returned an installation for a different enterprise")
	}
	return newInstallationClient(cfg.GitHubAppID, installation.ID, key, transport)
}
