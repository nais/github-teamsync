package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testSecret = "random-test-signing-secret-at-least-32-bytes"
	testSource = "https://example.com/api"
)

func payload(t *testing.T, eventType, id string, stamp time.Time) string {
	t.Helper()
	// This is the BuildCloudEvent shape from the API branch, including nested data.
	body, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "source": testSource, "type": eventType,
		"subject": "my-team/my-team", "time": stamp.UTC().Format(time.RFC3339),
		"datacontenttype": "application/json",
		"data": map[string]any{
			"actor": "user@example.com", "resourceType": "TEAM", "resourceName": "my-team", "teamSlug": "my-team",
			"data": map[string]any{"userEmail": "member@example.com", "role": "MEMBER"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func signature(body string) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	_, _ = mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func request(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/nais", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/cloudevents+json")
	r.Header.Set("X-Webhook-Signature", signature(body))
	return r
}

func newTestHandler() (*Handler, *atomic.Int32) {
	log := slog.New(slog.DiscardHandler)
	var calls atomic.Int32
	h := New(testSecret, testSource, func(string) bool { calls.Add(1); return true }, log)
	return h, &calls
}

func TestSupportedBranchEvents(t *testing.T) {
	for _, eventType := range []string{
		"io.nais.team.created", "io.nais.team.updated", "io.nais.team.confirm.delete.key",
		"io.nais.team.member.added", "io.nais.team.member.removed", "io.nais.team.member.set.role",
	} {
		t.Run(eventType, func(t *testing.T) {
			h, calls := newTestHandler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(payload(t, eventType, "stable-delivery-id", time.Now())))
			if w.Code != http.StatusAccepted {
				t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("scheduled syncs = %d, want 1", got)
			}
		})
	}
}

func TestRejectsInvalidRequestsWithoutScheduling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"missing signature", func(r *http.Request) { r.Header.Del("X-Webhook-Signature") }, http.StatusUnauthorized},
		{"wrong signature", func(r *http.Request) { r.Header.Set("X-Webhook-Signature", "sha256="+strings.Repeat("0", 64)) }, http.StatusUnauthorized},
		{"malformed signature", func(r *http.Request) { r.Header.Set("X-Webhook-Signature", "sha256=not-hex") }, http.StatusUnauthorized},
		{"ambiguous signature", func(r *http.Request) { r.Header.Add("X-Webhook-Signature", r.Header.Get("X-Webhook-Signature")) }, http.StatusUnauthorized},
		{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, http.StatusUnsupportedMediaType},
		{"wrong method", func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, calls := newTestHandler()
			r := request(payload(t, "io.nais.team.created", "id", time.Now()))
			tc.mutate(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("scheduled syncs = %d, want 0", got)
			}
		})
	}
}

func TestRejectsInvalidSignedPayloads(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	valid := payload(t, "io.nais.team.created", "id", now)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"invalid JSON", `{`, http.StatusBadRequest},
		{"trailing JSON", valid + `{}`, http.StatusBadRequest},
		{"wrong version", strings.Replace(valid, `"1.0"`, `"0.3"`, 1), http.StatusBadRequest},
		{"wrong source", strings.Replace(valid, testSource, "https://other.example.com/api", 1), http.StatusForbidden},
		{"missing id", strings.Replace(valid, `"id":"id"`, `"id":""`, 1), http.StatusBadRequest},
		{"invalid data", strings.Replace(valid, `"TEAM"`, `"APPLICATION"`, 1), http.StatusBadRequest},
		{"missing team slug", strings.Replace(valid, `"teamSlug":"my-team"`, `"ignoredTeamSlug":"my-team"`, 1), http.StatusBadRequest},
		{"null team slug", strings.Replace(valid, `"teamSlug":"my-team"`, `"teamSlug":null`, 1), http.StatusBadRequest},
		{"empty team slug", strings.Replace(valid, `"teamSlug":"my-team"`, `"teamSlug":""`, 1), http.StatusBadRequest},
		{"mismatched team slug", strings.Replace(valid, `"teamSlug":"my-team"`, `"teamSlug":"other-team"`, 1), http.StatusBadRequest},
		{"invalid team slug", strings.ReplaceAll(valid, "my-team", "bad/team"), http.StatusBadRequest},
		{"oversized team slug", strings.ReplaceAll(valid, "my-team", strings.Repeat("a", 31)), http.StatusBadRequest},
		{"old timestamp", payload(t, "io.nais.team.created", "id", now.Add(-6*time.Minute)), http.StatusBadRequest},
		{"future timestamp", payload(t, "io.nais.team.created", "id", now.Add(6*time.Minute)), http.StatusBadRequest},
		{"oversized", strings.Repeat("x", maxBodyBytes+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, calls := newTestHandler()
			h.now = func() time.Time { return now }
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(tc.body))
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("scheduled syncs = %d, want 0", got)
			}
		})
	}
}

func TestPingAndUnrelatedEventsDoNotSchedule(t *testing.T) {
	for _, eventType := range []string{"io.nais.ping", "io.nais.postgres.deleted", "io.nais.team.environment.updated"} {
		h, calls := newTestHandler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(payload(t, eventType, "id", time.Now())))
		if w.Code != http.StatusNoContent {
			t.Errorf("status = %d, want %d", w.Code, http.StatusNoContent)
		}
		if got := calls.Load(); got != 0 {
			t.Errorf("scheduled syncs = %d, want 0", got)
		}
	}
}

func TestConcurrentRetriesAreDeduplicated(t *testing.T) {
	h, calls := newTestHandler()
	body := payload(t, "io.nais.team.updated", "stable-id", time.Now())
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(body))
			if w.Code != http.StatusAccepted && w.Code != http.StatusNoContent {
				t.Errorf("unexpected status %d", w.Code)
			}
		})
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("scheduled syncs = %d, want 1", got)
	}
}

func TestDedupCacheExpiresAndIsBounded(t *testing.T) {
	h, _ := newTestHandler()
	now := time.Now()
	if got := h.schedule("id", "my-team", now); got != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want 202", got)
	}
	if got := h.schedule("id", "my-team", now); got != http.StatusNoContent {
		t.Fatalf("duplicate delivery status = %d, want 204", got)
	}
	if got := h.schedule("id", "my-team", now.Add(dedupTTL)); got != http.StatusAccepted {
		t.Fatalf("expired delivery status = %d, want 202", got)
	}
	for i := range maxDeliveryIDs + 1 {
		h.schedule(string(rune(i)), "my-team", now.Add(dedupTTL))
	}
	if len(h.seen) != maxDeliveryIDs {
		t.Errorf("cached deliveries = %d, want %d", len(h.seen), maxDeliveryIDs)
	}
}

func TestDisabledReceiver(t *testing.T) {
	h, calls := newTestHandler()
	h.secret = ""
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(payload(t, "io.nais.team.created", "id", time.Now())))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("scheduled syncs = %d, want 0", got)
	}
}

func TestSchedulesOnlyTeamFromEventData(t *testing.T) {
	for _, eventType := range []string{"io.nais.team.updated", "io.nais.team.member.added", "io.nais.team.member.removed", "io.nais.team.member.set.role"} {
		t.Run(eventType, func(t *testing.T) {
			var got string
			h := New(testSecret, testSource, func(slug string) bool { got = slug; return true }, slog.New(slog.DiscardHandler))
			body := payload(t, eventType, "id", time.Now())
			body = strings.Replace(body, `"subject":"my-team/my-team"`, `"subject":"unrelated-subject"`, 1)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(body))
			if w.Code != http.StatusAccepted || got != "my-team" {
				t.Errorf("status = %d, queued team = %q; want 202 and my-team", w.Code, got)
			}
		})
	}
}

func TestFullQueueDoesNotDeduplicateRejectedDelivery(t *testing.T) {
	accept := false
	calls := 0
	h := New(testSecret, testSource, func(string) bool { calls++; return accept }, slog.New(slog.DiscardHandler))
	body := payload(t, "io.nais.team.member.removed", "retry-id", time.Now())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(body))
	if w.Code != http.StatusServiceUnavailable || len(h.seen) != 0 {
		t.Fatalf("full queue status = %d, remembered deliveries = %d; want 503 and 0", w.Code, len(h.seen))
	}
	accept = true
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request(body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request(body))
	if w.Code != http.StatusNoContent || calls != 2 {
		t.Errorf("duplicate status = %d, enqueue attempts = %d; want 204 and 2", w.Code, calls)
	}
}
