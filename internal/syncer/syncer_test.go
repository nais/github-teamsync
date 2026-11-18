package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nais/nais-github-teamsync/internal/naisapi"
	"github.com/nais/nais-github-teamsync/internal/reconcilers"
)

type fakeSource struct {
	teams      []*naisapi.Team
	err        error
	refreshErr error
	calls      int
	onList     func(context.Context, int)
	onTeam     func(context.Context, string)
	targeted   map[string]*naisapi.Team
	teamCalls  []string
	teamErr    error
}

func (s *fakeSource) Teams(ctx context.Context) ([]*naisapi.Team, error) {
	s.calls++
	if s.onList != nil {
		s.onList(ctx, s.calls)
	}
	if s.calls > 1 && s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return s.teams, s.err
}

func (*fakeSource) Members(context.Context, string) ([]*naisapi.Member, error) { return nil, nil }

func (s *fakeSource) Team(ctx context.Context, slug string) (*naisapi.Team, error) {
	s.teamCalls = append(s.teamCalls, slug)
	if s.onTeam != nil {
		s.onTeam(ctx, slug)
	}
	return s.targeted[slug], s.teamErr
}

type fakeReconciler struct {
	reconciled, deleted, managed []string
	failTeam                     string
	inventoryErr                 error
	inventoryCalls               int
	onReconcile                  func(context.Context, string)
	onDelete                     func(context.Context, string) error
	onInventory                  func(context.Context)
}

func (r *fakeReconciler) Reconcile(ctx context.Context, _ reconcilers.TeamSource, team *naisapi.Team, _ *slog.Logger) error {
	r.reconciled = append(r.reconciled, team.Slug)
	if r.onReconcile != nil {
		r.onReconcile(ctx, team.Slug)
	}
	if team.Slug == r.failTeam {
		return errors.New("GitHub unavailable")
	}
	return nil
}

func (r *fakeReconciler) Delete(ctx context.Context, _ reconcilers.TeamSource, team *naisapi.Team, _ *slog.Logger) error {
	slug := team.Slug
	r.deleted = append(r.deleted, slug)
	if r.onDelete != nil {
		return r.onDelete(ctx, slug)
	}
	return nil
}

func (r *fakeReconciler) ManagedTeamSlugs(ctx context.Context) ([]string, error) {
	r.inventoryCalls++
	if r.onInventory != nil {
		r.onInventory(ctx)
	}
	return r.managed, r.inventoryErr
}

func newTestSyncer() (*Syncer, *fakeSource, *fakeReconciler) {
	source := &fakeSource{}
	r := &fakeReconciler{}
	log := slog.New(slog.DiscardHandler)
	return &Syncer{Client: source, Reconciler: r, Log: log, Workers: 1, Interval: time.Hour, Timeout: time.Minute, TeamTimeout: time.Second}, source, r
}

func TestFullSyncLifecycleLogs(t *testing.T) {
	for _, outcome := range []string{"success", "inventory failure", "cleanup failure", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			s, source, r := newTestSyncer()
			s.Organization = "example-org"
			source.teams = []*naisapi.Team{{Slug: "active"}}
			var output bytes.Buffer
			s.Log = slog.New(slog.NewJSONHandler(&output, nil))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch outcome {
			case "inventory failure":
				source.err = errors.New("inventory unavailable")
			case "cleanup failure":
				r.inventoryErr = errors.New("cleanup inventory unavailable")
			case "canceled":
				source.onList = func(context.Context, int) { cancel() }
			}
			wantSuccess := outcome == "success"
			if err := s.Sync(ctx); (err == nil) != wantSuccess {
				t.Fatalf("Sync() error = %v, want success = %t", err, wantSuccess)
			}

			logs := json.NewDecoder(&output)
			for _, message := range []string{"GitHub team sync started", "GitHub team sync finished"} {
				var entry struct {
					Level        string         `json:"level"`
					Message      string         `json:"msg"`
					Organization string         `json:"organization"`
					Success      *bool          `json:"success"`
					Duration     *time.Duration `json:"duration"`
					Teams        *int           `json:"teams"`
				}
				if err := logs.Decode(&entry); err != nil {
					t.Fatalf("decode %q: %v", message, err)
				}
				if entry.Level != "INFO" || entry.Message != message || entry.Organization != s.Organization {
					t.Fatalf("unexpected lifecycle log: %+v", entry)
				}
				if message == "GitHub team sync finished" {
					if entry.Success == nil || *entry.Success != wantSuccess || entry.Duration == nil || *entry.Duration < 0 {
						t.Error("finish log must report the sync outcome and duration")
					}
					if wantSuccess && (entry.Teams == nil || *entry.Teams != 1) {
						t.Error("successful finish log must report the team count")
					}
				}
			}
			if err := logs.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
				t.Errorf("expected exactly two lifecycle logs, got decode error %v after the second", err)
			}
		})
	}
}

func TestReconcileAndConfirmedDeletion(t *testing.T) {
	s, source, r := newTestSyncer()
	source.teams = []*naisapi.Team{{Slug: "active"}, {Slug: "deleting", DeletionInProgress: true}}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"active"}; !slices.Equal(r.reconciled, want) {
		t.Errorf("reconciled = %v, want %v", r.reconciled, want)
	}
	if want := []string{"deleting"}; !slices.Equal(r.deleted, want) {
		t.Errorf("deleted = %v, want %v", r.deleted, want)
	}
	if r.inventoryCalls != 1 {
		t.Errorf("inventory calls = %d, want 1", r.inventoryCalls)
	}
}

func TestIncompleteInventoryNeverMutatesGitHub(t *testing.T) {
	s, source, r := newTestSyncer()
	source.teams = []*naisapi.Team{{Slug: "partial"}}
	source.err = errors.New("later page failed")
	if err := s.Sync(t.Context()); err == nil {
		t.Fatal("expected inventory error")
	}
	if len(r.reconciled) != 0 || len(r.deleted) != 0 || r.inventoryCalls != 0 {
		t.Errorf("incomplete inventory must not mutate GitHub: %+v", r)
	}
}

func TestCleanupErrorsNeverDelete(t *testing.T) {
	for _, stage := range []string{"inventory", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			s, source, r := newTestSyncer()
			r.managed = []string{"orphan"}
			if stage == "inventory" {
				r.inventoryErr = errors.New("GitHub listing failed")
			} else {
				source.refreshErr = errors.New("API refresh failed")
			}
			if err := s.Sync(t.Context()); err == nil {
				t.Fatal("expected cleanup error")
			}
			if len(r.deleted) != 0 {
				t.Errorf("cleanup error must not delete teams, got %v", r.deleted)
			}
		})
	}
}

func TestEmptyAuthoritativeInventoryCanPrune(t *testing.T) {
	s, _, r := newTestSyncer()
	r.managed = []string{"orphan"}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"orphan"}; !slices.Equal(r.deleted, want) {
		t.Errorf("deleted = %v, want %v", r.deleted, want)
	}
}

func TestWebhookTargetsOneTeamAfterStartup(t *testing.T) {
	s, source, r := newTestSyncer()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	s.Queue = NewTeamQueue()
	s.Queue.Enqueue("target")
	source.targeted = map[string]*naisapi.Team{"target": {Slug: "target"}}
	r.onReconcile = func(context.Context, string) { cancel() }
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || r.inventoryCalls != 1 {
		t.Errorf("full inventory calls = %d, managed inventory calls = %d; want 2 and 1 for startup only", source.calls, r.inventoryCalls)
	}
	if !slices.Equal(source.teamCalls, []string{"target"}) || !slices.Equal(r.reconciled, []string{"target"}) {
		t.Errorf("targeted reads = %v, reconciled = %v; want only target", source.teamCalls, r.reconciled)
	}
}

func TestTargetedSyncUsesFreshStateWithoutPruning(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		s, source, r := newTestSyncer()
		source.targeted = map[string]*naisapi.Team{"target": {Slug: "target", DeletionInProgress: deleting}}
		r.managed = []string{"unrelated-orphan"}
		if err := s.SyncTeam(t.Context(), "target"); err != nil {
			t.Fatal(err)
		}
		if source.calls != 0 || r.inventoryCalls != 0 {
			t.Fatal("targeted sync must not enumerate full inventories or prune")
		}
		if deleting {
			if !slices.Equal(r.deleted, []string{"target"}) || len(r.reconciled) != 0 {
				t.Errorf("expected only API-confirmed deletion, got %+v", r)
			}
		} else if !slices.Equal(r.reconciled, []string{"target"}) || len(r.deleted) != 0 {
			t.Errorf("expected only target reconciliation, got %+v", r)
		}
	}
}

func TestFailedOrMissingTargetNeverDeletes(t *testing.T) {
	for _, tc := range []struct {
		name string
		team *naisapi.Team
		err  error
	}{
		{"missing", nil, nil},
		{"inaccessible", nil, errors.New("not found or unauthorized")},
		{"partial response", &naisapi.Team{Slug: "target", DeletionInProgress: true}, errors.New("partial data")},
		{"mismatched", &naisapi.Team{Slug: "other", DeletionInProgress: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, source, r := newTestSyncer()
			source.targeted = map[string]*naisapi.Team{"target": tc.team}
			source.teamErr = tc.err
			if err := s.SyncTeam(t.Context(), "target"); err == nil {
				t.Fatal("expected target lookup error")
			}
			if len(r.deleted) != 0 || len(r.reconciled) != 0 || r.inventoryCalls != 0 || source.calls != 0 {
				t.Fatalf("failed target lookup must not reconcile, delete, or prune: %+v", r)
			}
		})
	}
}

func TestFailedTargetDoesNotBlockOtherQueuedTeams(t *testing.T) {
	s, source, r := newTestSyncer()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	s.Queue = NewTeamQueue()
	s.Queue.Enqueue("broken")
	s.Queue.Enqueue("healthy")
	source.targeted = map[string]*naisapi.Team{"broken": {Slug: "broken"}, "healthy": {Slug: "healthy"}}
	r.failTeam = "broken"
	r.onReconcile = func(_ context.Context, slug string) {
		if slug == "healthy" {
			cancel()
		}
	}
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(source.teamCalls, []string{"broken", "healthy"}) || !slices.Equal(r.reconciled, []string{"broken", "healthy"}) {
		t.Errorf("targeted reads = %v, reconciled = %v; want broken then healthy", source.teamCalls, r.reconciled)
	}
	if source.calls != 2 || r.inventoryCalls != 1 {
		t.Error("failed targeted work must not trigger full scans or pruning")
	}
}

func TestWebhookTrafficDoesNotPostponeFullPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, source, _ := newTestSyncer()
		s.Interval = time.Minute
		s.Queue = NewTeamQueue()
		source.targeted = map[string]*naisapi.Team{"target": {Slug: "target"}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		synctest.Wait()
		for range 3 {
			time.Sleep(20 * time.Second) // Virtual time: the periodic deadline, not a real sleep.
			s.Queue.Enqueue("target")
			synctest.Wait()
		}
		if source.calls != 4 || len(source.teamCalls) != 3 {
			t.Errorf("full inventory reads = %d, targeted reads = %v; want startup + periodic poll and three targeted reads", source.calls, source.teamCalls)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
