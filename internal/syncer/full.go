package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Sync reads a complete inventory, reconciles teams with a bounded pool, then
// cleans up orphans only if every team succeeded. The timeout includes all stages.
// Only one instance may own a GitHub organization at a time.
func (s *Syncer) Sync(ctx context.Context) (syncErr error) {
	if s.Workers <= 0 {
		return fmt.Errorf("sync workers must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	start := time.Now()
	meter := otel.Meter("nais-github-teamsync")
	cycles, err := meter.Int64Counter("teamsync_cycles_total", metric.WithDescription("Completed polling cycles"))
	if err != nil {
		return err
	}
	duration, err := meter.Float64Histogram("teamsync_cycle_duration_seconds", metric.WithUnit("s"))
	if err != nil {
		return err
	}
	defer func() {
		opts := metric.WithAttributes(attribute.String("organization", s.Organization), attribute.Bool("error", syncErr != nil))
		cycles.Add(ctx, 1, opts)
		duration.Record(ctx, time.Since(start).Seconds(), opts)
	}()

	teams, err := s.teams(ctx)
	if err != nil {
		return err
	}
	if err := s.reconcileInventory(ctx, teams); err != nil {
		return err
	}
	if err := s.prune(ctx); err != nil {
		return err
	}
	s.Log.InfoContext(ctx, "GitHub team sync completed", "teams", len(teams))
	return nil
}

func (s *Syncer) reconcileInventory(ctx context.Context, teams []*naisapi.Team) error {
	jobs := make(chan int)
	// Each index is owned by one job. Reading results after Wait avoids a shared
	// error accumulator and keeps errors in inventory order, not completion order.
	failures := make([]error, len(teams))
	var workers sync.WaitGroup
	for range min(s.Workers, len(teams)) {
		workers.Go(func() {
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				team := teams[index]
				teamCtx, cancel := context.WithTimeout(ctx, s.TeamTimeout)
				err := s.reconcileTeam(teamCtx, team)
				cancel()
				if err != nil {
					s.Log.ErrorContext(ctx, "team sync failed", "team", team.Slug, "error", err)
					failures[index] = fmt.Errorf("team %q: %w", team.Slug, err)
				}
			}
		})
	}
dispatch:
	for index := range teams {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	// Wait for every active job even when the cycle expires. Cleanup must never
	// overlap a slow job or run after a timeout, cancellation, or team failure.
	return errors.Join(append(failures, ctx.Err())...)
}

// teams never exposes an inventory with an error or an invalid entry. Validate
// every entry before starting writes, including when refreshing for cleanup.
// Source is responsible for complete pagination and rejecting partial responses.
func (s *Syncer) teams(ctx context.Context) ([]*naisapi.Team, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	teams, err := s.Client.Teams(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch complete Nais team snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(teams))
	for _, team := range teams {
		if team == nil || team.Slug == "" {
			return nil, fmt.Errorf("nais API returned incomplete team inventory")
		}
		slug := strings.ToLower(team.Slug)
		if seen[slug] {
			return nil, fmt.Errorf("nais API returned duplicate team slugs")
		}
		seen[slug] = true
	}
	return teams, nil
}
