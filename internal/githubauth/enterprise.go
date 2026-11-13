package githubauth

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewEnterpriseIdentityClient uses a PAT only for enterprise identity GraphQL reads.
// Organization operations must continue to use the App installation client.
func NewEnterpriseIdentityClient(token string, transport http.RoundTripper) (*http.Client, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("enterprise identity PAT must be nonblank and contain no whitespace")
	}
	return &http.Client{
		Transport:     otelhttp.NewTransport(&enterpriseTransport{token: token, base: transport}),
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

type enterpriseTransport struct {
	token string
	base  http.RoundTripper
}

func (t *enterpriseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.github.com" || req.URL.Path != "/graphql" || req.Method != http.MethodPost {
		return nil, fmt.Errorf("enterprise identity PAT is restricted to GitHub GraphQL requests")
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
