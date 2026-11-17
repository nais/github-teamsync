package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/go-github/v93/github"
	"github.com/nais/nais-github-teamsync/internal/config"
	"github.com/nais/nais-github-teamsync/internal/githubauth"
	"github.com/nais/nais-github-teamsync/internal/logger"
	"github.com/nais/nais-github-teamsync/internal/naisapi"
	githubteams "github.com/nais/nais-github-teamsync/internal/reconcilers/github/team"
	"github.com/nais/nais-github-teamsync/internal/syncer"
	"github.com/nais/nais-github-teamsync/internal/webhook"
	"github.com/sethvargo/go-envconfig"
	"github.com/shurcooL/githubv4"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"
)

func Run(ctx context.Context) error {
	cfg, err := config.New(ctx, envconfig.OsLookuper())
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log, err := logger.New(cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		return err
	}
	log = log.With("organization", cfg.GitHubOrganization)
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	client := naisapi.New(cfg.NaisAPIURL.String(), cfg.NaisServiceAccountTokenPath, &http.Client{
		Timeout:       30 * time.Second,
		Transport:     otelhttp.NewTransport(http.DefaultTransport),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
	mp, registry, err := newMeterProvider()
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mp.Shutdown(shutdownCtx)
	}()
	httpClient, err := githubauth.New(ctx, cfg, http.DefaultTransport)
	if err != nil {
		return fmt.Errorf("GitHub authentication for %q: %w", cfg.GitHubOrganization, err)
	}
	restClient, err := github.NewClient(github.WithHTTPClient(httpClient))
	if err != nil {
		return err
	}
	identityClient := httpClient
	if cfg.GitHubIdentityProvider != config.IdentityProviderOrganizationSAML {
		identityClient, err = githubauth.NewEnterpriseIdentityClient(ctx, cfg, http.DefaultTransport)
		if err != nil {
			return fmt.Errorf("GitHub enterprise identity authentication: %w", err)
		}
	}
	r := githubteams.New(cfg.GitHubOrganization, restClient.Teams, githubv4.NewClient(httpClient),
		githubteams.WithDryRun(cfg.DryRun), githubteams.WithIdentityProvider(cfg.GitHubIdentityProvider, cfg.GitHubEnterprise, githubv4.NewClient(identityClient)))
	queue := syncer.NewTeamQueue()
	s := &syncer.Syncer{
		Client:       client,
		Reconciler:   r,
		Log:          log,
		Organization: cfg.GitHubOrganization,
		Interval:     cfg.SyncInterval,
		Timeout:      cfg.SyncTimeout,
		TeamTimeout:  cfg.TeamTimeout,
		Workers:      cfg.SyncWorkers,
		Queue:        queue,
	}
	hooks := webhook.New(cfg.WebhookSecret, cfg.WebhookSource, queue.Enqueue, log)
	log.InfoContext(ctx, "starting GitHub team sync", "interval", cfg.SyncInterval,
		"workers", cfg.SyncWorkers, "dry_run", cfg.DryRun, "identity_provider", cfg.GitHubIdentityProvider,
		"enterprise", cfg.GitHubEnterprise, "version", version)
	wg, ctx := errgroup.WithContext(ctx)
	wg.Go(func() error { return runHttpServer(ctx, cfg.ListenAddress, registry, log, hooks) })
	wg.Go(func() error { return s.Run(ctx) })
	return wg.Wait()
}
