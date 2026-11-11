package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
)

// runHttpServer will start the HTTP server
func runHttpServer(
	ctx context.Context,
	listenAddress string,
	promRegistry prometheus.Gatherer,
	log *slog.Logger,
	webhookHandler http.Handler,
) error {
	router := newRouter(promRegistry, webhookHandler)

	srv := &http.Server{
		Addr:              listenAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	wg, ctx := errgroup.WithContext(ctx)
	wg.Go(func() error {
		defer log.Debug("Done running http shutdown logic")
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		log.InfoContext(ctx, "HTTP server shutting down...")
		if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.InfoContext(ctx, "HTTP server shutdown failed", "error", err)
			return err
		}
		return nil
	})

	wg.Go(func() error {
		defer log.Debug("Done running http server")
		log.InfoContext(ctx, "HTTP server accepting requests", "address", listenAddress)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.InfoContext(ctx, "unexpected error from HTTP server", "error", err)
			return err
		}
		log.InfoContext(ctx, "HTTP server finished, terminating...")
		return nil
	})
	return wg.Wait()
}

func newRouter(registry prometheus.Gatherer, webhookHandler http.Handler) http.Handler {
	router := http.NewServeMux()
	router.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	router.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if webhookHandler == nil {
		webhookHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Nais API webhook receiver is disabled", http.StatusServiceUnavailable)
		})
	}
	router.Handle("POST /webhooks/nais", webhookHandler)
	return router
}
