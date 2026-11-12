package syncer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nais/nais-github-teamsync/internal/webhook"
)

const (
	integrationWebhookSecret = "offline-test-webhook-secret-at-least-32-bytes"
	integrationWebhookSource = "https://example.com/api"
)

func deliverWebhook(t *testing.T, handler http.Handler, id, eventType, slug string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "source": integrationWebhookSource,
		"type": eventType, "time": time.Now().UTC().Format(time.RFC3339),
		"datacontenttype": "application/json",
		"data": map[string]any{
			"resourceType": "TEAM", "resourceName": slug, "teamSlug": slug,
			// These fields must not become desired state or a deletion command.
			"data": map[string]any{"deletionInProgress": true, "userEmail": "payload-only@example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(integrationWebhookSecret))
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/nais", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/cloudevents+json")
	req.Header.Set("X-Webhook-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Code
}

func runIntegrationWorker(t *testing.T, s *Syncer, ctx context.Context, cancel context.CancelFunc) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.Run(ctx); err != nil {
			t.Errorf("worker failed: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return done
}

func TestSignedWebhookOverflowRemainsRetryableWithRealQueue(t *testing.T) {
	s, source, _ := newTestSyncer()
	s.Queue = NewTeamQueue()
	for i := range maxPendingTeams {
		if !s.Queue.Enqueue(fmt.Sprintf("team-%d", i)) {
			t.Fatalf("queue rejected team %d before capacity", i)
		}
	}
	hooks := webhook.New([]byte(integrationWebhookSecret), integrationWebhookSource, s.Queue.Enqueue, s.Log)
	for range 2 {
		if status := deliverWebhook(t, hooks, "retry", "io.nais.team.updated", "overflow"); status != http.StatusServiceUnavailable {
			t.Fatalf("overflow status = %d, want 503 on every rejected attempt", status)
		}
	}
	if status := deliverWebhook(t, hooks, "pending", "io.nais.team.updated", "team-0"); status != http.StatusAccepted {
		t.Fatalf("already-pending team status = %d, want 202 even when full", status)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	source.onTeam = func(ctx context.Context, _ string) {
		close(started)
		<-ctx.Done() // Hold one active read while checking the freed queue slot.
	}
	done := runIntegrationWorker(t, s, ctx, cancel)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("worker did not take queued work")
	}
	if status := deliverWebhook(t, hooks, "retry", "io.nais.team.updated", "overflow"); status != http.StatusAccepted {
		t.Fatalf("retry after capacity is available = %d, want 202", status)
	}
	if status := deliverWebhook(t, hooks, "retry", "io.nais.team.updated", "overflow"); status != http.StatusNoContent {
		t.Fatalf("accepted retry duplicate = %d, want 204", status)
	}
	cancel()
	<-done
	if !slices.Equal(source.teamCalls, []string{"team-0"}) {
		t.Fatalf("reads = %v; cancellation must stop before the next team", source.teamCalls)
	}
}
