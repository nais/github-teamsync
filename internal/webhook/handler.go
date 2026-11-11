// Package webhook implements the receiver contract from nais/api's experimental
// webhook branch at b8beefa436dcbf1e0f2cd7d3149d50ae7e728b51. Events select teams
// for fresh API reads; payloads never supply GitHub state or deletion commands.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxBodyBytes   = 1 << 20
	maxClockSkew   = 5 * time.Minute
	dedupTTL       = 24 * time.Hour
	maxDeliveryIDs = 4096
)

type cloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	Data            json.RawMessage `json:"data"`
}

type eventData struct {
	ResourceType string `json:"resourceType"`
	ResourceName string `json:"resourceName"`
	TeamSlug     string `json:"teamSlug"`
}

type Handler struct {
	secret, source string
	trigger        func(string) bool
	log            *slog.Logger
	now            func() time.Time
	mu             sync.Mutex
	seen           map[string]time.Time
}

// trigger must enqueue without performing I/O. False requests a delivery retry;
// the delivery is not deduplicated until the queue accepts it.
func New(secret, source string, trigger func(string) bool, log *slog.Logger) *Handler {
	return &Handler{secret: secret, source: source, trigger: trigger, log: log, now: time.Now, seen: make(map[string]time.Time)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.secret == "" || h.source == "" {
		http.Error(w, "Nais API webhook receiver is disabled", http.StatusServiceUnavailable)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/cloudevents+json" {
		http.Error(w, "expected application/cloudevents+json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "could not read webhook body", status)
		return
	}
	if !validSignature(h.secret, r.Header.Values("X-Webhook-Signature"), body) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}
	var event cloudEvent
	if err := json.Unmarshal(body, &event); err != nil || event.SpecVersion != "1.0" || event.ID == "" || len(event.ID) > 256 || event.Type == "" || event.DataContentType != "application/json" {
		http.Error(w, "invalid CloudEvents envelope", http.StatusBadRequest)
		return
	}
	if event.Source != h.source {
		http.Error(w, "unexpected CloudEvents source", http.StatusForbidden)
		return
	}
	stamp, err := time.Parse(time.RFC3339, event.Time)
	now := h.now()
	if err != nil || stamp.Before(now.Add(-maxClockSkew)) || stamp.After(now.Add(maxClockSkew)) {
		http.Error(w, "invalid or stale CloudEvents time", http.StatusBadRequest)
		return
	}
	if event.Type == "io.nais.ping" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !relevant(event.Type) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var data eventData
	if err := json.Unmarshal(event.Data, &data); err != nil || data.ResourceType != "TEAM" || data.TeamSlug == "" || len(data.TeamSlug) > 30 || data.TeamSlug != data.ResourceName || strings.ContainsAny(data.TeamSlug, "/\\ \t\r\n") {
		http.Error(w, "invalid team event data", http.StatusBadRequest)
		return
	}
	status := h.schedule(event.ID, data.TeamSlug, now)
	if status == http.StatusServiceUnavailable {
		http.Error(w, "team sync queue is full; retry delivery", status)
		return
	}
	if status == http.StatusAccepted {
		h.log.DebugContext(r.Context(), "webhook queued targeted Nais team sync", "team", data.TeamSlug, "event_type", event.Type, "delivery_id", event.ID)
	}
	w.WriteHeader(status)
}

func validSignature(secret string, headers []string, body []byte) bool {
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "sha256=") {
		return false
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(headers[0], "sha256="))
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(signature, mac.Sum(nil))
}

func relevant(eventType string) bool {
	switch eventType {
	case "io.nais.team.created", "io.nais.team.updated",
		"io.nais.team.confirm.delete.key", "io.nais.team.member.added",
		"io.nais.team.member.removed", "io.nais.team.member.set.role":
		return true
	default:
		return false
	}
}

// Enqueue and remember accepted deliveries atomically, so concurrent retries
// cannot be acknowledged as duplicates when no work was accepted by the queue.
// Eviction/restart can cause another idempotent sync using fresh API state.
func (h *Handler) schedule(id, slug string, now time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, expires := range h.seen {
		if !expires.After(now) {
			delete(h.seen, key)
		}
	}
	if _, exists := h.seen[id]; exists {
		return http.StatusNoContent
	}
	if !h.trigger(slug) {
		return http.StatusServiceUnavailable
	}
	if len(h.seen) >= maxDeliveryIDs {
		var oldest string
		var deadline time.Time
		for key, expires := range h.seen {
			if oldest == "" || expires.Before(deadline) {
				oldest, deadline = key, expires
			}
		}
		delete(h.seen, oldest)
	}
	h.seen[id] = now.Add(dedupTTL)
	return http.StatusAccepted
}
