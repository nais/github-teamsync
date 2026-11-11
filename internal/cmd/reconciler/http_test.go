package reconciler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHTTPRoutes(t *testing.T) {
	router := newRouter(prometheus.NewRegistry(), nil)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/metrics", http.StatusOK},
		{http.MethodHead, "/metrics", http.StatusOK},
		{http.MethodPost, "/metrics", http.StatusOK},
		{http.MethodPost, "/webhooks/nais", http.StatusServiceUnavailable},
		{http.MethodGet, "/webhooks/nais", http.StatusMethodNotAllowed},
		{http.MethodHead, "/webhooks/nais", http.StatusMethodNotAllowed},
		{http.MethodPut, "/webhooks/nais", http.StatusMethodNotAllowed},
		{http.MethodGet, "/healthz/extra", http.StatusNotFound},
		{http.MethodGet, "/metrics/extra", http.StatusNotFound},
		{http.MethodPost, "/webhooks/nais/extra", http.StatusNotFound},
		{http.MethodPost, "/unknown", http.StatusNotFound},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"event":"team.deleted"}`)))
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusMethodNotAllowed {
				wantAllow := "POST"
				if tc.path == "/healthz" {
					wantAllow = "GET, HEAD"
				}
				if got := w.Header().Get("Allow"); got != wantAllow {
					t.Errorf("Allow = %q, want %q", got, wantAllow)
				}
			}
		})
	}
}

func TestWebhookHandlerIsMounted(t *testing.T) {
	called := false
	hooks := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	})
	router := newRouter(prometheus.NewRegistry(), hooks)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhooks/nais", nil))
	if !called {
		t.Error("webhook handler was not called")
	}
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}
