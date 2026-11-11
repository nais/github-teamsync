package syncer

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
)

func TestTargetedSyncRejectsEmptySlugBeforeReading(t *testing.T) {
	s, source, r := newTestSyncer()
	if err := s.SyncTeam(t.Context(), ""); err == nil {
		t.Fatal("expected empty-slug error")
	}
	if len(source.teamCalls) != 0 || len(r.reconciled) != 0 || len(r.deleted) != 0 {
		t.Fatal("empty slug must not cause reads or writes")
	}
}

func TestTargetedSyncSharesDeadlineAcrossReadAndWrite(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(map[bool]string{false: "reconcile", true: "delete"}[deleting], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, source, r := newTestSyncer()
				source.targeted = map[string]*naisapi.Team{"target": {Slug: "target", DeletionInProgress: deleting}}
				var readDeadline time.Time
				source.onTeam = func(ctx context.Context, _ string) {
					readDeadline, _ = ctx.Deadline()
					time.Sleep(600 * time.Millisecond)
				}
				block := func(ctx context.Context, _ string) {
					deadline, _ := ctx.Deadline()
					if !deadline.Equal(readDeadline) {
						t.Errorf("write deadline = %v, want lookup deadline %v", deadline, readDeadline)
					}
					<-ctx.Done()
				}
				r.onReconcile = block
				r.onDelete = func(ctx context.Context, slug string) error {
					block(ctx, slug)
					return nil // Even a late nil return must not count as success.
				}
				start := time.Now()
				if err := s.SyncTeam(t.Context(), "target"); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error = %v, want deadline exceeded", err)
				}
				if elapsed := time.Since(start); elapsed != s.TeamTimeout {
					t.Fatalf("elapsed = %s, want %s", elapsed, s.TeamTimeout)
				}
				if source.calls != 0 || r.inventoryCalls != 0 {
					t.Fatal("targeted timeout must not trigger inventory or cleanup")
				}
			})
		})
	}
}

func TestTargetedLookupTimeoutNeverWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, source, r := newTestSyncer()
		source.targeted = map[string]*naisapi.Team{"target": {Slug: "target", DeletionInProgress: true}}
		source.onTeam = func(ctx context.Context, _ string) { <-ctx.Done() }
		if err := s.SyncTeam(t.Context(), "target"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if len(r.reconciled) != 0 || len(r.deleted) != 0 {
			t.Fatal("timed-out lookup must not reconcile or delete, even with returned state")
		}
	})
}

func TestInvalidInventoryNeverWrites(t *testing.T) {
	for _, stage := range []string{"initial", "refresh"} {
		for _, tc := range []struct {
			name string
			team *naisapi.Team
		}{
			{name: "nil"},
			{name: "empty-slug", team: &naisapi.Team{}},
		} {
			t.Run(stage+"/"+tc.name, func(t *testing.T) {
				s, source, r := newTestSyncer()
				r.managed = []string{"orphan"}
				source.onList = func(_ context.Context, call int) {
					if stage == "initial" || call == 2 {
						source.teams = []*naisapi.Team{{Slug: "valid"}, tc.team}
					}
				}
				if err := s.Sync(t.Context()); err == nil {
					t.Fatal("expected invalid-inventory error")
				}
				if len(r.reconciled) != 0 || len(r.deleted) != 0 {
					t.Fatal("invalid inventory must not produce writes")
				}
			})
		}
	}
}

func TestDeletionEventDuringProcessingUsesFreshFollowupState(t *testing.T) {
	s, source, r := newTestSyncer()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	s.Queue = NewTeamQueue()
	s.Queue.Enqueue("target")
	source.targeted = map[string]*naisapi.Team{"target": {Slug: "target"}}
	r.onReconcile = func(_ context.Context, slug string) {
		// A deletion event arrives during reconciliation. Only its slug is queued;
		// the next read, not the event, decides whether GitHub deletion is allowed.
		source.targeted[slug] = &naisapi.Team{Slug: slug, DeletionInProgress: true}
		s.Queue.Enqueue(slug)
	}
	r.onDelete = func(context.Context, string) error {
		cancel()
		return nil
	}
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(source.teamCalls, []string{"target", "target"}) {
		t.Fatalf("targeted reads = %v, want initial read and fresh follow-up", source.teamCalls)
	}
	if !slices.Equal(r.reconciled, []string{"target"}) || !slices.Equal(r.deleted, []string{"target"}) {
		t.Fatalf("reconciled = %v, deleted = %v; want reconcile followed by confirmed deletion", r.reconciled, r.deleted)
	}
	if source.calls != 2 || r.inventoryCalls != 1 {
		t.Fatal("targeted deletion must not trigger cleanup; expect startup inventories only")
	}
}

func TestFullSyncTeamTimeoutContinuesAndSuppressesCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, source, r := newTestSyncer()
		source.teams = []*naisapi.Team{{Slug: "slow"}, {Slug: "healthy"}}
		r.onReconcile = func(ctx context.Context, slug string) {
			if slug == "slow" {
				<-ctx.Done()
			}
		}
		if err := s.Sync(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if !slices.Equal(r.reconciled, []string{"slow", "healthy"}) {
			t.Fatalf("reconciled = %v, want slow then healthy", r.reconciled)
		}
		if r.inventoryCalls != 0 || source.calls != 1 {
			t.Fatal("a team timeout must suppress orphan cleanup")
		}
	})
}

func TestFullSyncDeletionFailureContinuesAndSuppressesCleanup(t *testing.T) {
	s, source, r := newTestSyncer()
	source.teams = []*naisapi.Team{{Slug: "deleting", DeletionInProgress: true}, {Slug: "healthy"}}
	wantErr := errors.New("deletion failed")
	r.onDelete = func(context.Context, string) error { return wantErr }
	if err := s.Sync(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want deletion error", err)
	}
	if !slices.Equal(r.reconciled, []string{"healthy"}) || !slices.Equal(r.deleted, []string{"deleting"}) {
		t.Fatalf("reconciled = %v, deleted = %v; want deletion attempt then healthy team", r.reconciled, r.deleted)
	}
	if r.inventoryCalls != 0 {
		t.Fatal("failed explicit deletion must suppress orphan cleanup")
	}
}

func TestFullSyncCycleTimeoutCoversEveryStage(t *testing.T) {
	for _, stage := range []string{"inventory", "team", "managed inventory", "refresh", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, source, r := newTestSyncer()
				// Make the cycle limit shorter than the per-team limit.
				s.Timeout = time.Second
				s.TeamTimeout = time.Minute
				source.teams = []*naisapi.Team{{Slug: "active"}, {Slug: "waiting"}}
				r.managed = []string{"orphan-one", "orphan-two"}
				block := func(ctx context.Context) { <-ctx.Done() }
				source.onList = func(ctx context.Context, call int) {
					if (stage == "inventory" && call == 1) || (stage == "refresh" && call == 2) {
						block(ctx)
					}
				}
				r.onReconcile = func(ctx context.Context, _ string) {
					if stage == "team" {
						block(ctx)
					}
				}
				r.onInventory = func(ctx context.Context) {
					if stage == "managed inventory" {
						block(ctx)
					}
				}
				r.onDelete = func(ctx context.Context, _ string) error {
					if stage == "cleanup" {
						block(ctx)
					}
					return nil
				}
				start := time.Now()
				if err := s.Sync(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error = %v, want cycle deadline exceeded", err)
				}
				if elapsed := time.Since(start); elapsed != s.Timeout {
					t.Fatalf("elapsed = %s, want cycle timeout %s", elapsed, s.Timeout)
				}
				if stage == "inventory" && len(r.reconciled) != 0 {
					t.Fatal("expired inventory must not start team work")
				}
				if stage == "team" && !slices.Equal(r.reconciled, []string{"active"}) {
					t.Fatalf("reconciled = %v; must stop before waiting team", r.reconciled)
				}
				if stage == "cleanup" {
					if !slices.Equal(r.deleted, []string{"orphan-one"}) {
						t.Fatalf("deleted = %v; must stop before second orphan", r.deleted)
					}
				} else if len(r.deleted) != 0 {
					t.Fatalf("unexpected deletion attempts: %v", r.deleted)
				}
			})
		})
	}
}

func TestCanceledExecutionDoesNotStartReads(t *testing.T) {
	s, source, r := newTestSyncer()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, operation := range map[string]func() error{
		"full": func() error { return s.Sync(ctx) },
		"team": func() error { return s.SyncTeam(ctx, "target") },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s error = %v, want canceled", name, err)
		}
	}
	if source.calls != 0 || len(source.teamCalls) != 0 || r.inventoryCalls != 0 {
		t.Fatal("already-canceled work must not start reads")
	}
}

func TestCleanupProtectsNewTeamsByNaisSlug(t *testing.T) {
	s, source, r := newTestSyncer()
	source.teams = []*naisapi.Team{{Slug: "existing"}}
	r.managed = []string{"EXISTING", "new-team", "orphan"}
	source.onList = func(_ context.Context, call int) {
		if call == 2 {
			source.teams = []*naisapi.Team{
				{Slug: "existing"},
				{Slug: "new-team"},
			}
		}
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.reconciled, []string{"existing"}) {
		t.Fatalf("reconciled = %v, want initial inventory only", r.reconciled)
	}
	if !slices.Equal(r.deleted, []string{"orphan"}) {
		t.Fatalf("deleted = %v; existing and new Nais team slugs must be protected", r.deleted)
	}
}

func TestCleanupStopsOnDeletionFailure(t *testing.T) {
	s, _, r := newTestSyncer()
	r.managed = []string{"first", "second"}
	wantErr := errors.New("GitHub unavailable")
	r.onDelete = func(context.Context, string) error { return wantErr }
	if err := s.Sync(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want deletion error", err)
	}
	if !slices.Equal(r.deleted, []string{"first"}) {
		t.Fatalf("deletions = %v, want first only", r.deleted)
	}
}

func TestCancellationDuringCleanupStopsFurtherDeletes(t *testing.T) {
	s, _, r := newTestSyncer()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.managed = []string{"first", "second"}
	r.onDelete = func(context.Context, string) error {
		cancel()
		return nil
	}
	if err := s.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
	if !slices.Equal(r.deleted, []string{"first"}) {
		t.Fatalf("deletions = %v, want first only", r.deleted)
	}
}

func TestCleanupTeamTimeoutStopsFurtherDeletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, r := newTestSyncer()
		r.managed = []string{"first", "second"}
		r.onDelete = func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return nil
		}
		start := time.Now()
		if err := s.Sync(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if elapsed := time.Since(start); elapsed != s.TeamTimeout {
			t.Fatalf("elapsed = %s, want per-team timeout %s", elapsed, s.TeamTimeout)
		}
		if !slices.Equal(r.deleted, []string{"first"}) {
			t.Fatalf("deletions = %v, want first only", r.deleted)
		}
	})
}

func TestFailedFullSyncAllowsQueuedWorkAndRetriesOnPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, source, r := newTestSyncer()
		s.Interval = time.Minute
		s.Queue = NewTeamQueue()
		s.Queue.Enqueue("target")
		source.targeted = map[string]*naisapi.Team{"target": {Slug: "target"}}
		source.err = errors.New("inventory unavailable")
		r.onReconcile = func(context.Context, string) {
			// Check and recover the fake source on the worker goroutine. Virtual
			// time alone does not synchronize shared state with future timer work.
			if source.calls != 1 || r.inventoryCalls != 0 {
				t.Errorf("failed startup must allow targeted work without cleanup or immediate retry: source calls=%d, managed inventory calls=%d", source.calls, r.inventoryCalls)
			}
			source.err = nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		synctest.Wait()
		time.Sleep(s.Interval)
		synctest.Wait()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if source.calls != 3 || r.inventoryCalls != 1 || !slices.Equal(r.reconciled, []string{"target"}) {
			t.Fatalf("next poll must retry the full sync: source calls=%d, managed inventory calls=%d, reconciled=%v", source.calls, r.inventoryCalls, r.reconciled)
		}
	})
}
