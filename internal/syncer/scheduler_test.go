package syncer

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestSchedulerStartupBeforeQueuedTeams(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q := NewTeamQueue()
	q.Enqueue("one")
	q.Enqueue("two")
	var calls []string
	w := scheduler{
		workers: 1,
		queue:   q, interval: time.Hour,
		fullSync: func(context.Context) { calls = append(calls, "full") },
		teamSync: func(_ context.Context, slug string) {
			calls = append(calls, slug)
			if slug == "two" {
				cancel()
			}
		},
	}
	w.run(ctx)
	if want := []string{"full", "one", "two"}; !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestSchedulerPollIntervalStartsAfterFullSyncCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var starts []time.Time
		w := scheduler{
			workers:  1,
			interval: time.Minute,
			fullSync: func(context.Context) {
				starts = append(starts, time.Now())
				time.Sleep(20 * time.Second)
				if len(starts) == 2 {
					cancel()
				}
			},
		}
		w.run(ctx) // A nil queue permits periodic-only operation.
		if len(starts) != 2 || starts[1].Sub(starts[0]) != 80*time.Second {
			t.Fatalf("poll starts = %v, want two starts 80 seconds apart", starts)
		}
	})
}

func TestSchedulerStopsWhileIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		polls := 0
		w := scheduler{
			workers: 1,
			queue:   NewTeamQueue(), interval: time.Hour,
			fullSync: func(context.Context) { polls++ },
		}
		done := make(chan struct{})
		go func() {
			w.run(ctx)
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done
		if polls != 1 {
			t.Fatalf("full syncs = %d, want startup only", polls)
		}
	})
}

func TestSchedulerCancellationReachesActiveOperation(t *testing.T) {
	for _, operation := range []string{"full", "team"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				q := NewTeamQueue()
				q.Enqueue("active")
				q.Enqueue("waiting")
				var calls []string
				block := func(ctx context.Context) { <-ctx.Done() }
				w := scheduler{
					workers: 1,
					queue:   q, interval: time.Hour,
					fullSync: func(ctx context.Context) {
						calls = append(calls, "full")
						if operation == "full" {
							block(ctx)
						}
					},
					teamSync: func(ctx context.Context, slug string) {
						calls = append(calls, slug)
						block(ctx)
					},
				}
				done := make(chan struct{})
				go func() {
					w.run(ctx)
					close(done)
				}()
				synctest.Wait()
				cancel()
				<-done
				if len(q.processing) != 0 {
					t.Fatal("cancellation must release the worker's active queue claim")
				}
				want := []string{"full"}
				if operation == "team" {
					want = append(want, "active")
				}
				if !slices.Equal(calls, want) {
					t.Fatalf("calls = %v, want %v; no work may start after cancellation", calls, want)
				}
			})
		})
	}
}

func TestSchedulerAlreadyCanceledDoesNoWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := scheduler{
		workers:  1,
		fullSync: func(context.Context) { t.Fatal("unexpected full sync") },
		teamSync: func(context.Context, string) { t.Fatal("unexpected targeted sync") },
	}
	w.run(ctx)
}

func TestSchedulerPoolLimitAndSameTeamExclusion(t *testing.T) {
	for _, count := range []int{1, 5, 10} {
		t.Run(fmt.Sprintf("workers=%d", count), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
				defer cancel()
				q := NewTeamQueue()
				for i := range count + 2 {
					q.Enqueue(fmt.Sprintf("team-%d", i))
				}
				var active, peak, finished, polls atomic.Int32
				var mu sync.Mutex
				running := make(map[string]bool)
				attempts := make(map[string]int)
				started := make(chan string, 2*(count+2))
				release := make(chan struct{})
				w := scheduler{
					queue: q, workers: count, interval: time.Hour,
					fullSync: func(context.Context) { polls.Add(1) },
					teamSync: func(ctx context.Context, slug string) {
						mu.Lock()
						if running[slug] {
							t.Errorf("team %q is already running", slug)
						}
						running[slug] = true
						attempts[slug]++
						mu.Unlock()
						current := active.Add(1)
						for previous := peak.Load(); current > previous; previous = peak.Load() {
							if peak.CompareAndSwap(previous, current) {
								break
							}
						}
						started <- slug
						select {
						case <-release:
						case <-ctx.Done():
						}
						active.Add(-1)
						mu.Lock()
						delete(running, slug)
						mu.Unlock()
						if finished.Add(1) == int32(count+3) {
							cancel()
						}
					},
				}
				done := make(chan struct{})
				go func() { w.run(ctx); close(done) }()
				synctest.Wait()
				if active.Load() != int32(count) || len(started) != count {
					t.Fatalf("active = %d, started = %d, want pool size %d", active.Load(), len(started), count)
				}
				followup := <-started
				for range 3 {
					if !q.Enqueue(followup) {
						t.Fatal("follow-up rejected")
					}
				}
				synctest.Wait()
				if active.Load() != int32(count) {
					t.Fatal("follow-up must not exceed the pool limit")
				}
				close(release)
				<-done
				if finished.Load() != int32(count+3) || peak.Load() != int32(count) || active.Load() != 0 {
					t.Fatalf("finished=%d, peak=%d, active=%d; want all work, bounded concurrency, and no active jobs", finished.Load(), peak.Load(), active.Load())
				}
				for slug, calls := range attempts {
					want := 1
					if slug == followup {
						want = 2
					}
					if calls != want {
						t.Errorf("team %q attempts = %d, want %d", slug, calls, want)
					}
				}
				if polls.Load() != 1 || q.size != 0 || len(q.processing) != 0 {
					t.Fatal("expected startup only and a fully released queue")
				}
			})
		})
	}
}

func TestSchedulerDuePollDrainsPoolAndPausesTargetedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
		defer cancel()
		q := NewTeamQueue()
		for _, slug := range []string{"one", "two", "waiting"} {
			q.Enqueue(slug)
		}
		var active, finished, polls atomic.Int32
		var inFull atomic.Bool
		releaseOne, releaseTwo, releasePoll := make(chan struct{}), make(chan struct{}), make(chan struct{})
		w := scheduler{
			queue: q, workers: 2, interval: time.Minute,
			fullSync: func(ctx context.Context) {
				if active.Load() != 0 {
					t.Error("full sync overlapped active targeted jobs")
				}
				inFull.Store(true)
				if polls.Add(1) == 2 {
					select {
					case <-releasePoll:
					case <-ctx.Done():
					}
				}
				inFull.Store(false)
			},
			teamSync: func(ctx context.Context, slug string) {
				if inFull.Load() {
					t.Error("targeted work overlapped a full sync")
				}
				active.Add(1)
				var release <-chan struct{}
				switch slug {
				case "one":
					release = releaseOne
				case "two":
					release = releaseTwo
				default:
					if polls.Load() != 2 {
						t.Error("waiting work ran before the overdue full poll")
					}
				}
				if release != nil {
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				active.Add(-1)
				if finished.Add(1) == 4 {
					cancel()
				}
			},
		}
		done := make(chan struct{})
		go func() { w.run(ctx); close(done) }()
		synctest.Wait()
		if active.Load() != 2 {
			t.Fatalf("active = %d, want two blocked jobs", active.Load())
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		close(releaseOne)
		synctest.Wait()
		if active.Load() != 1 || polls.Load() != 1 || finished.Load() != 1 {
			t.Fatal("due poll must wait for every active job without starting waiting work")
		}
		close(releaseTwo)
		synctest.Wait()
		if polls.Load() != 2 || !inFull.Load() || active.Load() != 0 {
			t.Fatal("full poll must start after the entire targeted pool drains")
		}
		q.Enqueue("during-full")
		synctest.Wait()
		if finished.Load() != 2 {
			t.Fatal("events during a full sync must remain queued")
		}
		close(releasePoll)
		<-done
		if finished.Load() != 4 || len(q.processing) != 0 {
			t.Fatal("queued work must resume after the full cycle and release all claims")
		}
	})
}

func TestSchedulerShutdownWaitsForEveryActiveWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		q := NewTeamQueue()
		for _, slug := range []string{"one", "two", "three", "waiting"} {
			q.Enqueue(slug)
		}
		var active, calls atomic.Int32
		release := make(chan struct{})
		w := scheduler{
			queue: q, workers: 3, interval: time.Hour,
			fullSync: func(context.Context) {},
			teamSync: func(ctx context.Context, _ string) {
				active.Add(1)
				calls.Add(1)
				<-ctx.Done()
				<-release
				active.Add(-1)
			},
		}
		done := make(chan struct{})
		go func() { w.run(ctx); close(done) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("scheduler returned while jobs were still active")
		default:
		}
		close(release)
		<-done
		if active.Load() != 0 || calls.Load() != 3 || len(q.processing) != 0 || q.size != 1 {
			t.Fatal("shutdown must join all active workers without taking waiting work")
		}
	})
}
