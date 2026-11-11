// Package syncer reconciles full snapshots and freshly queried individual teams
// from Nais API. Webhook payloads select work, never supply desired GitHub state.
package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/nais/nais-github-teamsync/internal/reconcilers"
)

// Syncer owns reconciliation for one organization. Run and Sync must not be
// called concurrently; the scheduler provides the phase and cleanup barriers.
// Client and Reconciler must support concurrent operations on different teams.
type Syncer struct {
	Organization string
	Client       Source
	Reconciler   reconcilers.Reconciler
	Log          *slog.Logger
	Interval     time.Duration
	Timeout      time.Duration
	TeamTimeout  time.Duration
	Workers      int
	Queue        *TeamQueue
}

type Source interface {
	reconcilers.TeamSource
	Teams(ctx context.Context) ([]*naisapi.Team, error)
	Team(ctx context.Context, slug string) (*naisapi.Team, error)
}

func (s *Syncer) Run(ctx context.Context) error {
	if s.Workers <= 0 {
		return fmt.Errorf("sync workers must be positive")
	}
	w := scheduler{
		queue:    s.Queue,
		workers:  s.Workers,
		interval: s.Interval,
		fullSync: func(ctx context.Context) {
			if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
				s.Log.ErrorContext(ctx, "GitHub team sync failed; retrying on the next poll", "error", err)
			}
		},
		teamSync: func(ctx context.Context, slug string) {
			if err := s.SyncTeam(ctx, slug); err != nil && ctx.Err() == nil {
				s.Log.ErrorContext(ctx, "targeted team sync failed; periodic polling will retry", "team", slug, "error", err)
			}
		},
	}
	w.run(ctx)
	return nil
}
