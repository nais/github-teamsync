// Package naisapi reads desired team state from the authenticated Nais GraphQL API.
package naisapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Khan/genqlient/graphql"
)

type Client struct {
	graphql graphql.Client
}

// workloadClient applies rotating authentication and validates responses before
// genqlient decodes them. Upstream bodies/errors must not leak into logs, and
// GraphQL errors or incomplete envelopes must never become empty inventories.
type workloadClient struct {
	tokenPath string
	http      *http.Client
}

// New uses a rotating workload identity token file. An empty path is supported
// only for a loopback HTTP nais api proxy, which supplies its own authentication.
func New(endpoint, tokenPath string, client *http.Client) *Client {
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{graphql: graphql.NewClient(endpoint, &workloadClient{tokenPath: tokenPath, http: &copyClient})}
}

func (c *workloadClient) Do(req *http.Request) (*http.Response, error) {
	if err := c.authenticate(req); err != nil {
		return nil, err
	}
	// #nosec G704 -- genqlient builds this request from the operator-configured Nais endpoint, validated at startup; redirects are disabled.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Nais API: %w", err)
	}
	body := resp.Body
	defer func() { _ = body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nais API returned HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	// Avoid echoing raw upstream responses, which may contain member information.
	const maxResponseBytes = 16 << 20
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Nais API response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return nil, fmt.Errorf("nais API response exceeds size limit")
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, fmt.Errorf("decode Nais API response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("nais API GraphQL returned %d errors; refusing partial data", len(envelope.Errors))
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return nil, fmt.Errorf("nais API returned no data")
	}
	resp.Body = io.NopCloser(bytes.NewReader(responseBody))
	return resp, nil
}

func (c *workloadClient) authenticate(req *http.Request) error {
	if c.tokenPath == "" {
		host := req.URL.Hostname()
		localProxy := req.URL.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")
		if !localProxy {
			return fmt.Errorf("workload identity token path is required outside a loopback HTTP proxy")
		}
		return nil
	}
	// Reopen on every request, including every page: projected tokens rotate
	// through in-place updates or replacement files/symlinks. Never cache a token
	// or retain an open file descriptor across requests.
	// #nosec G304 -- the path is injected by Nais or supplied through trusted process configuration, not HTTP input.
	data, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return fmt.Errorf("read Nais workload identity token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return fmt.Errorf("nais workload identity token file is empty")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("nais workload identity token contains invalid whitespace")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func nextPage(page *InventoryPageInfo, after *string) (*string, error) {
	if !page.HasNextPage {
		return nil, nil
	}
	if page.EndCursor == nil || *page.EndCursor == "" || (after != nil && *after == *page.EndCursor) {
		return nil, fmt.Errorf("nais API returned invalid pagination cursor")
	}
	return page.EndCursor, nil
}

func teamFromState(state *TeamState) (*Team, error) {
	if state == nil || state.Slug == "" {
		return nil, fmt.Errorf("nais API returned incomplete team state")
	}
	return &Team{Slug: state.Slug, Purpose: state.Purpose, DeletionInProgress: state.DeletionInProgress}, nil
}

// Team reads current desired state for one team. Missing or inaccessible teams
// return an error, never a deletion instruction; full inventories drive pruning.
func (c *Client) Team(ctx context.Context, slug string) (*Team, error) {
	result, err := TeamBySlug(ctx, c.graphql, slug)
	if err != nil {
		return nil, err
	}
	team, err := teamFromState(result.Team)
	if err != nil {
		return nil, err
	}
	if team.Slug != slug {
		return nil, fmt.Errorf("nais API returned a different team than requested")
	}
	return team, nil
}

func (c *Client) Teams(ctx context.Context) ([]*Team, error) {
	var all []*Team
	seenSlugs := make(map[string]bool)
	var after *string
	for {
		result, err := Teams(ctx, c.graphql, after)
		if err != nil {
			return nil, err
		}
		if result.Teams == nil || result.Teams.PageInfo == nil || result.Teams.Nodes == nil {
			return nil, fmt.Errorf("nais API returned incomplete team inventory")
		}
		for _, node := range result.Teams.Nodes {
			team, err := teamFromState(node)
			if err != nil {
				return nil, err
			}
			// The Nais slug is also the GitHub target. Reject duplicates using
			// GitHub's case-insensitive comparison before exposing the inventory.
			slug := strings.ToLower(team.Slug)
			if seenSlugs[slug] {
				return nil, fmt.Errorf("nais API returned duplicate team slugs")
			}
			seenSlugs[slug] = true
			all = append(all, team)
		}
		after, err = nextPage(result.Teams.PageInfo, after)
		if err != nil {
			return nil, err
		}
		if after == nil {
			return all, nil
		}
	}
}

func (c *Client) Members(ctx context.Context, slug string) ([]*Member, error) {
	var all []*Member
	var after *string
	for {
		result, err := Members(ctx, c.graphql, slug, after)
		if err != nil {
			return nil, err
		}
		if result.Team == nil || result.Team.Members == nil || result.Team.Members.PageInfo == nil || result.Team.Members.Nodes == nil {
			return nil, fmt.Errorf("nais API returned incomplete team membership")
		}
		for _, member := range result.Team.Members.Nodes {
			if member == nil || member.User == nil || member.User.Email == "" {
				return nil, fmt.Errorf("nais API returned a member without an email")
			}
			all = append(all, &Member{Email: member.User.Email})
		}
		after, err = nextPage(result.Team.Members.PageInfo, after)
		if err != nil {
			return nil, err
		}
		if after == nil {
			return all, nil
		}
	}
}
