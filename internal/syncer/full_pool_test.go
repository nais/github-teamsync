package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/nais/nais-github-teamsync/internal/reconcilers"
)

type poolReconciler struct {
	reconcile func(context.Context, *naisapi.Team) error
	delete    func(context.Context, *naisapi.Team) error
	inventory func(context.Context) ([]string, error)
}

func (r *poolReconciler) Reconcile(ctx context.Context, _ reconcilers.TeamSource, team *naisapi.Team, _ *slog.Logger) error {
	return r.reconcile(ctx, team)
}

func (r *poolReconciler) Delete(ctx context.Context, _ reconcilers.TeamSource, team *naisapi.Team, _ *slog.Logger) error {
	return r.delete(ctx, team)
}

func (r *poolReconciler) ManagedTeamSlugs(ctx context.Context) ([]string, error) {
	return r.inventory(ctx)
}

func TestFullSyncPoolWaitsForAllTeamsBeforeCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("team-failure=%t", fail), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, source, _ := newTestSyncer()
				s.Workers = 5
				const total = 8
				for i := range total {
					source.teams = append(source.teams, &naisapi.Team{Slug: fmt.Sprintf("team-%d", i), DeletionInProgress: i == 2})
				}
				var active, peak, finished, inventories, deleted atomic.Int32
				release := make(chan struct{})
				wantErr := errors.New("one team failed")
				process := func(ctx context.Context, team *naisapi.Team) error {
					current := active.Add(1)
					for previous := peak.Load(); current > previous; previous = peak.Load() {
						if peak.CompareAndSwap(previous, current) {
							break
						}
					}
					select {
					case <-release:
					case <-ctx.Done():
					}
					active.Add(-1)
					finished.Add(1)
					if fail && team.Slug == "team-0" {
						return wantErr
					}
					return nil
				}
				s.Reconciler = &poolReconciler{
					reconcile: process,
					delete: func(ctx context.Context, team *naisapi.Team) error {
						if team.DeletionInProgress {
							return process(ctx, team)
						}
						if active.Load() != 0 || finished.Load() != total {
							t.Error("orphan deletion overlapped full-sync jobs")
						}
						deleted.Add(1)
						return nil
					},
					inventory: func(context.Context) ([]string, error) {
						if active.Load() != 0 || finished.Load() != total {
							t.Error("cleanup inventory started before every team finished")
						}
						inventories.Add(1)
						return []string{"orphan-one", "orphan-two"}, nil
					},
				}
				done := make(chan error, 1)
				go func() { done <- s.Sync(t.Context()) }()
				synctest.Wait()
				if active.Load() != 5 || peak.Load() != 5 || inventories.Load() != 0 || deleted.Load() != 0 {
					t.Fatal("full sync must fill only the configured pool, without starting cleanup")
				}
				close(release)
				err := <-done
				if active.Load() != 0 || finished.Load() != total || peak.Load() != 5 {
					t.Fatal("every full-sync job must finish within the pool limit")
				}
				if fail {
					if !errors.Is(err, wantErr) || inventories.Load() != 0 || deleted.Load() != 0 || source.calls != 1 {
						t.Fatalf("failed team must suppress all cleanup, error=%v", err)
					}
				} else if err != nil || inventories.Load() != 1 || deleted.Load() != 2 || source.calls != 2 {
					t.Fatalf("successful pool must allow refreshed serial cleanup, error=%v", err)
				}
			})
		})
	}
}

func TestFullSyncPoolCancellationWaitsForActiveJobs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, source, _ := newTestSyncer()
		s.Workers = 3
		s.Timeout = time.Second
		s.TeamTimeout = time.Minute
		for i := range 10 {
			source.teams = append(source.teams, &naisapi.Team{Slug: fmt.Sprintf("team-%d", i)})
		}
		var calls, active atomic.Int32
		release := make(chan struct{})
		s.Reconciler = &poolReconciler{
			reconcile: func(ctx context.Context, _ *naisapi.Team) error {
				calls.Add(1)
				active.Add(1)
				<-ctx.Done()
				<-release
				active.Add(-1)
				return nil
			},
			inventory: func(context.Context) ([]string, error) {
				t.Error("cleanup must not run after cycle timeout")
				return nil, nil
			},
		}
		done := make(chan error, 1)
		go func() { done <- s.Sync(t.Context()) }()
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 3 || active.Load() != 3 {
			t.Fatal("cycle timeout must stop dispatch without returning ahead of active jobs")
		}
		select {
		case err := <-done:
			t.Fatalf("full sync returned before active jobs finished: %v", err)
		default:
		}
		close(release)
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want cycle deadline exceeded", err)
		}
		if active.Load() != 0 || calls.Load() != 3 || source.calls != 1 {
			t.Fatal("cycle timeout must join the pool without starting more teams or cleanup")
		}
	})
}

func TestFullSyncRejectsDuplicateSlugsBeforePoolStarts(t *testing.T) {
	s, source, r := newTestSyncer()
	s.Workers = 5
	source.teams = []*naisapi.Team{{Slug: "same"}, {Slug: "SAME"}}
	if err := s.Sync(t.Context()); err == nil {
		t.Fatal("expected duplicate-slug error")
	}
	if len(r.reconciled) != 0 || len(r.deleted) != 0 || r.inventoryCalls != 0 {
		t.Fatal("duplicate slugs must be rejected before parallel work or cleanup")
	}
}

func TestSyncerRejectsInvalidWorkerCount(t *testing.T) {
	for _, count := range []int{0, -1} {
		t.Run(fmt.Sprintf("workers=%d", count), func(t *testing.T) {
			s, source, _ := newTestSyncer()
			s.Workers = count
			if err := s.Run(t.Context()); err == nil {
				t.Fatal("Run must reject a non-positive pool size")
			}
			if err := s.Sync(t.Context()); err == nil {
				t.Fatal("Sync must reject a non-positive pool size")
			}
			if source.calls != 0 || len(source.teamCalls) != 0 {
				t.Fatal("invalid pool size must not cause upstream reads")
			}
		})
	}
}
