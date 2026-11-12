package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/nais/nais-github-teamsync/internal/webhook"
)

// webhookPoolAPI serves real GraphQL requests from the concurrent Nais client.
// Desired state is immutable; only request counters need synchronization.
type webhookPoolAPI struct {
	mu               sync.Mutex
	teams            []string
	deletingFollowup string
	lists            int
	lookups          map[string]int
	members          map[string]int
}

func newWebhookPoolAPI(t *testing.T, teams []string, deletingFollowup string) (*naisapi.Client, *webhookPoolAPI) {
	t.Helper()
	api := &webhookPoolAPI{
		teams: teams, deletingFollowup: deletingFollowup,
		lookups: make(map[string]int), members: make(map[string]int),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OperationName string
			Variables     struct{ Slug string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var data any
		api.mu.Lock()
		switch req.OperationName {
		case "Teams":
			api.lists++
			nodes := make([]map[string]any, 0, len(api.teams))
			for _, slug := range api.teams {
				nodes = append(nodes, map[string]any{"slug": slug, "purpose": "fresh-" + slug, "deletionInProgress": false})
			}
			data = map[string]any{"teams": map[string]any{"nodes": nodes, "pageInfo": map[string]bool{"hasNextPage": false}}}
		case "TeamBySlug":
			slug := req.Variables.Slug
			api.lookups[slug]++
			data = map[string]any{"team": map[string]any{
				"slug": slug, "purpose": "fresh-" + slug,
				"deletionInProgress": slug == api.deletingFollowup && api.lookups[slug] > 1,
			}}
		case "Members":
			slug := req.Variables.Slug
			api.members[slug]++
			data = map[string]any{"team": map[string]any{"members": map[string]any{
				"nodes":    []any{map[string]any{"user": map[string]string{"email": slug + "@example.com"}}},
				"pageInfo": map[string]bool{"hasNextPage": false},
			}}}
		default:
			t.Errorf("unexpected GraphQL operation %q", req.OperationName)
		}
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return naisapi.New(server.URL, "", server.Client()), api
}

func (a *webhookPoolAPI) counts() (int, map[string]int, map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lists, maps.Clone(a.lookups), maps.Clone(a.members)
}

func readWebhookPoolMembers(ctx context.Context, client *naisapi.Client, team *naisapi.Team) error {
	if team.Purpose != "fresh-"+team.Slug {
		return fmt.Errorf("team state did not come from the API")
	}
	members, err := client.Members(ctx, team.Slug)
	if err != nil {
		return err
	}
	if len(members) != 1 || members[0].Email != team.Slug+"@example.com" {
		return fmt.Errorf("membership did not come from the API")
	}
	return nil
}

func TestSignedWebhooksDriveConcurrentAPIReadsAndOneDeletionFollowup(t *testing.T) {
	client, api := newWebhookPoolAPI(t, nil, "team-0")
	s, _, _ := newTestSyncer()
	s.Client, s.Queue, s.Workers = client, NewTeamQueue(), 5
	s.Timeout, s.TeamTimeout = 10*time.Second, 5*time.Second
	hooks := webhook.New([]byte(integrationWebhookSecret), integrationWebhookSource, s.Queue.Enqueue, s.Log)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	running := make(map[string]bool)
	reconciled := make(map[string]int)
	peak := 0
	var finished, deleted, inventories atomic.Int32
	started := make(chan string, 8)
	othersFinished := make(chan struct{}, 4)
	probeFinished := make(chan struct{})
	releaseTarget, releaseOthers := make(chan struct{}), make(chan struct{})
	begin := func(slug string) {
		mu.Lock()
		defer mu.Unlock()
		if running[slug] {
			t.Errorf("team %q is already being reconciled", slug)
		}
		running[slug] = true
		peak = max(peak, len(running))
	}
	end := func(slug string) {
		mu.Lock()
		delete(running, slug)
		mu.Unlock()
		if finished.Add(1) == 7 { // Five initial teams, a probe, and one follow-up.
			cancel()
		}
	}
	s.Reconciler = &poolReconciler{
		reconcile: func(ctx context.Context, team *naisapi.Team) error {
			begin(team.Slug)
			defer end(team.Slug)
			if err := readWebhookPoolMembers(ctx, client, team); err != nil {
				t.Error(err)
				return err
			}
			mu.Lock()
			reconciled[team.Slug]++
			mu.Unlock()
			started <- team.Slug
			if team.Slug == "probe" {
				close(probeFinished)
				return nil
			}
			release := releaseOthers
			if team.Slug == "team-0" {
				release = releaseTarget
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			if team.Slug != "team-0" {
				othersFinished <- struct{}{}
			}
			return nil
		},
		delete: func(_ context.Context, team *naisapi.Team) error {
			begin(team.Slug)
			defer end(team.Slug)
			if team.Slug != "team-0" || !team.DeletionInProgress {
				t.Errorf("deletion must use fresh confirmed state, got %+v", team)
			}
			deleted.Add(1)
			return nil
		},
		inventory: func(context.Context) ([]string, error) {
			inventories.Add(1)
			return nil, nil
		},
	}
	for i := range 5 {
		if status := deliverWebhook(t, hooks, fmt.Sprintf("initial-%d", i), "io.nais.team.member.removed", fmt.Sprintf("team-%d", i)); status != http.StatusAccepted {
			t.Fatalf("initial webhook status = %d, want 202", status)
		}
	}
	done := runIntegrationWorker(t, s, ctx, cancel)
	for range 5 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("the five workers did not reach reconciliation")
		}
	}
	for _, id := range []string{"followup-one", "followup-two"} {
		if status := deliverWebhook(t, hooks, id, "io.nais.team.confirm.delete.key", "team-0"); status != http.StatusAccepted {
			t.Fatalf("follow-up status = %d, want 202", status)
		}
	}
	if status := deliverWebhook(t, hooks, "followup-one", "io.nais.team.confirm.delete.key", "team-0"); status != http.StatusNoContent {
		t.Fatalf("duplicate delivery status = %d, want 204", status)
	}
	close(releaseOthers)
	for range 4 {
		select {
		case <-othersFinished:
		case <-ctx.Done():
			t.Fatal("unrelated workers did not finish")
		}
	}
	if status := deliverWebhook(t, hooks, "probe", "io.nais.team.updated", "probe"); status != http.StatusAccepted {
		t.Fatalf("probe status = %d, want 202", status)
	}
	select {
	case <-probeFinished:
	case <-ctx.Done():
		t.Fatal("a blocked follow-up prevented an unrelated team from running")
	}
	if _, lookups, _ := api.counts(); lookups["team-0"] != 1 {
		t.Fatal("follow-up lookup started before the first reconciliation finished")
	}
	close(releaseTarget)
	<-done
	lists, lookups, members := api.counts()
	if lists != 2 || inventories.Load() != 1 || finished.Load() != 7 || deleted.Load() != 1 || peak != 5 {
		t.Fatalf("lists=%d, inventories=%d, finished=%d, deleted=%d, peak=%d; want startup only, all jobs, one deletion, and five concurrent teams", lists, inventories.Load(), finished.Load(), deleted.Load(), peak)
	}
	for _, slug := range []string{"team-0", "team-1", "team-2", "team-3", "team-4", "probe"} {
		wantLookups := 1
		if slug == "team-0" {
			wantLookups = 2
		}
		if lookups[slug] != wantLookups || members[slug] != 1 || reconciled[slug] != 1 {
			t.Errorf("team %q: lookups=%d, membership reads=%d, reconciliations=%d", slug, lookups[slug], members[slug], reconciled[slug])
		}
	}
}

func TestSignedWebhooksWaitForFullPoolAndCleanup(t *testing.T) {
	client, api := newWebhookPoolAPI(t, []string{"full-one", "full-two"}, "")
	s, _, _ := newTestSyncer()
	s.Client, s.Queue, s.Workers = client, NewTeamQueue(), 2
	s.Timeout, s.TeamTimeout = 10*time.Second, 5*time.Second
	hooks := webhook.New([]byte(integrationWebhookSecret), integrationWebhookSource, s.Queue.Enqueue, s.Log)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var fullActive, targeted, deleted atomic.Int32
	var inCleanup atomic.Bool
	fullStarted, fullFinished := make(chan struct{}, 2), make(chan struct{}, 2)
	cleanupStarted := make(chan struct{})
	releaseOne, releaseTwo, releaseCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.Reconciler = &poolReconciler{
		reconcile: func(ctx context.Context, team *naisapi.Team) error {
			if err := readWebhookPoolMembers(ctx, client, team); err != nil {
				t.Error(err)
				return err
			}
			var release <-chan struct{}
			switch team.Slug {
			case "full-one":
				release = releaseOne
			case "full-two":
				release = releaseTwo
			}
			if release != nil {
				fullActive.Add(1)
				defer fullActive.Add(-1)
				fullStarted <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				fullFinished <- struct{}{}
				return nil
			}
			if fullActive.Load() != 0 || inCleanup.Load() || deleted.Load() != 1 {
				t.Error("targeted work ran before the full cycle and cleanup completed")
			}
			if targeted.Add(1) == 3 {
				cancel()
			}
			return nil
		},
		inventory: func(context.Context) ([]string, error) {
			if fullActive.Load() != 0 {
				t.Error("cleanup inventory overlapped full-sync jobs")
			}
			return []string{"orphan"}, nil
		},
		delete: func(ctx context.Context, team *naisapi.Team) error {
			if team.Slug != "orphan" || fullActive.Load() != 0 || targeted.Load() != 0 {
				t.Error("orphan cleanup overlapped reconciliation")
			}
			inCleanup.Store(true)
			defer inCleanup.Store(false)
			close(cleanupStarted)
			select {
			case <-releaseCleanup:
			case <-ctx.Done():
				return ctx.Err()
			}
			deleted.Add(1)
			return nil
		},
	}
	done := runIntegrationWorker(t, s, ctx, cancel)
	for range 2 {
		select {
		case <-fullStarted:
		case <-ctx.Done():
			t.Fatal("full-sync jobs did not start concurrently")
		}
	}
	for _, slug := range []string{"queued-one", "queued-two"} {
		if status := deliverWebhook(t, hooks, slug, "io.nais.team.updated", slug); status != http.StatusAccepted {
			t.Fatalf("webhook during full sync = %d, want 202", status)
		}
	}
	close(releaseOne)
	select {
	case <-fullFinished:
	case <-ctx.Done():
		t.Fatal("first full-sync job did not finish")
	}
	if _, lookups, _ := api.counts(); len(lookups) != 0 {
		t.Fatal("targeted reads started while a full-sync job was still active")
	}
	close(releaseTwo)
	select {
	case <-cleanupStarted:
	case <-ctx.Done():
		t.Fatal("cleanup did not start after the full pool drained")
	}
	if status := deliverWebhook(t, hooks, "during-cleanup", "io.nais.team.member.added", "queued-three"); status != http.StatusAccepted {
		t.Fatalf("webhook during cleanup = %d, want 202", status)
	}
	if _, lookups, _ := api.counts(); len(lookups) != 0 {
		t.Fatal("targeted reads started before cleanup finished")
	}
	close(releaseCleanup)
	<-done
	lists, lookups, members := api.counts()
	if lists != 2 || targeted.Load() != 3 || deleted.Load() != 1 || fullActive.Load() != 0 {
		t.Fatal("expected one successful full cycle followed by all three queued teams")
	}
	for _, slug := range []string{"queued-one", "queued-two", "queued-three"} {
		if lookups[slug] != 1 || members[slug] != 1 {
			t.Errorf("queued team %q must get one fresh state and membership read", slug)
		}
	}
}
