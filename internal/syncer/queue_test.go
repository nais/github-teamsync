package syncer

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func TestTeamQueueCoalescesWithoutDroppingDistinctTeams(t *testing.T) {
	q := NewTeamQueue()
	for _, slug := range []string{"one", "one", "two", "three", "two"} {
		if !q.Enqueue(slug) {
			t.Fatalf("queue rejected %q", slug)
		}
	}
	var got []string
	for range 3 {
		select {
		case <-q.wake:
		default:
			t.Fatal("pending work must have a wake-up")
		}
		slug, ok := q.take()
		if !ok {
			t.Fatal("missing queued team")
		}
		got = append(got, slug)
		q.complete(slug)
	}
	if !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Errorf("queue order = %v, want one, two, three", got)
	}
	if _, ok := q.take(); ok {
		t.Error("queue should be empty")
	}
	if !q.Enqueue("one") {
		t.Error("a processed team must be accepted again")
	}
}

func TestTeamQueueIsBoundedAndCanRetry(t *testing.T) {
	q := NewTeamQueue()
	if q.Enqueue("") {
		t.Error("empty team slug must not be queued")
	}
	for i := range maxPendingTeams {
		if !q.Enqueue(fmt.Sprintf("team-%d", i)) {
			t.Fatalf("queue filled too soon at %d", i)
		}
	}
	if !q.Enqueue("team-0") {
		t.Error("an already-pending team must be accepted even when full")
	}
	if q.Enqueue("retry-team") || len(q.pending) != maxPendingTeams {
		t.Fatal("full queue must reject a new team without growing")
	}
	q.take()
	if !q.Enqueue("retry-team") {
		t.Error("retry must succeed after capacity is available")
	}
}

func TestTeamQueueConcurrentEnqueues(t *testing.T) {
	q := NewTeamQueue()
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			for range 3 {
				if !q.Enqueue(fmt.Sprintf("team-%d", i)) {
					t.Errorf("queue rejected team-%d", i)
				}
			}
		})
	}
	wg.Wait()
	seen := make(map[string]bool)
	for {
		slug, ok := q.take()
		if !ok {
			break
		}
		if seen[slug] {
			t.Errorf("duplicate pending team: %s", slug)
		}
		seen[slug] = true
		q.complete(slug)
	}
	if len(seen) != 100 {
		t.Errorf("queued distinct teams = %d, want 100", len(seen))
	}
}

func TestTeamQueueEventDuringProcessingQueuesOneFollowup(t *testing.T) {
	q := NewTeamQueue()
	q.Enqueue("active")
	q.Enqueue("other")
	if slug, ok := q.take(); !ok || slug != "active" {
		t.Fatalf("take = (%q, %t), want active", slug, ok)
	}
	// The worker is still processing active. Only waiting work is coalesced.
	for range 3 {
		if !q.Enqueue("active") {
			t.Fatal("event during processing must be accepted")
		}
	}
	if slug, ok := q.take(); !ok || slug != "other" {
		t.Fatalf("take = (%q, %t), want other", slug, ok)
	}
	q.complete("other")
	if slug, ok := q.take(); ok || slug != "" {
		t.Fatalf("take = (%q, %t); follow-up must wait for active processing to finish", slug, ok)
	}
	q.complete("active")
	if slug, ok := q.take(); !ok || slug != "active" {
		t.Fatalf("take = (%q, %t), want one active follow-up", slug, ok)
	}
	q.complete("active")
	if slug, ok := q.take(); ok || slug != "" {
		t.Fatalf("take = (%q, %t), want empty queue", slug, ok)
	}
}

func TestTeamQueueWraparoundPreservesFIFO(t *testing.T) {
	q := NewTeamQueue()
	// Repeatedly refill a partly drained queue to exercise slot reuse.
	for i := range maxPendingTeams {
		if !q.Enqueue(fmt.Sprintf("team-%d", i)) {
			t.Fatalf("initial enqueue %d rejected", i)
		}
	}
	for i := range 3 * maxPendingTeams {
		want := fmt.Sprintf("team-%d", i)
		if slug, ok := q.take(); !ok || slug != want {
			t.Fatalf("take = (%q, %t), want %q", slug, ok, want)
		}
		q.complete(want)
		if !q.Enqueue(fmt.Sprintf("team-%d", i+maxPendingTeams)) {
			t.Fatalf("refill after take %d rejected", i)
		}
	}
	for i := 3 * maxPendingTeams; i < 4*maxPendingTeams; i++ {
		want := fmt.Sprintf("team-%d", i)
		if slug, ok := q.take(); !ok || slug != want {
			t.Fatalf("take = (%q, %t), want %q", slug, ok, want)
		}
		q.complete(want)
	}
	if _, ok := q.take(); ok {
		t.Fatal("queue should be empty after draining")
	}
	for i, slug := range q.slugs {
		if slug != "" {
			t.Errorf("drained slot %d retains %q", i, slug)
		}
	}
}

func TestTeamQueueWakeAfterDrainAndRefill(t *testing.T) {
	q := NewTeamQueue()
	for range 3 {
		if !q.Enqueue("team") {
			t.Fatal("enqueue rejected")
		}
		select {
		case <-q.wake:
		default:
			t.Fatal("enqueue must wake the worker")
		}
		if slug, ok := q.take(); !ok || slug != "team" {
			t.Fatalf("take = (%q, %t), want team", slug, ok)
		}
		q.complete("team")
		select {
		case <-q.wake:
			t.Fatal("draining the queue must not produce another notification")
		default:
		}
	}
}

func TestTeamQueueConcurrentProducerAndConsumer(t *testing.T) {
	q := NewTeamQueue()
	const teams = 500 // Below capacity, even if all producers finish first.
	var producers sync.WaitGroup
	for i := range teams {
		producers.Go(func() {
			if !q.Enqueue(fmt.Sprintf("team-%d", i)) {
				t.Errorf("queue rejected team-%d", i)
			}
		})
	}
	done := make(chan struct{})
	go func() {
		producers.Wait()
		close(done)
	}()
	seen := make(map[string]bool)
	for {
		select {
		case <-q.wake:
			if slug, ok := q.take(); ok {
				if seen[slug] {
					t.Fatalf("duplicate team %q", slug)
				}
				seen[slug] = true
				q.complete(slug)
			}
		case <-done:
			// With all producers finished, drain any remaining work directly.
			for {
				slug, ok := q.take()
				if !ok {
					if len(seen) != teams {
						t.Fatalf("processed %d teams, want %d", len(seen), teams)
					}
					return
				}
				if seen[slug] {
					t.Fatalf("duplicate team %q", slug)
				}
				seen[slug] = true
				q.complete(slug)
			}
		}
	}
}

func TestTeamQueueSkipsBlockedFollowupsWithoutReordering(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrapped=%t", wrapped), func(t *testing.T) {
			q := NewTeamQueue()
			if wrapped {
				// After the next two active claims, put the head at the end so
				// removing ready work must shift across the circular boundary.
				for range maxPendingTeams - 3 {
					q.Enqueue("warmup")
					q.take()
					q.complete("warmup")
				}
			}
			for _, slug := range []string{"one", "two"} {
				q.Enqueue(slug)
				if got, ok := q.take(); !ok || got != slug {
					t.Fatalf("take = (%q, %t), want %q", got, ok, slug)
				}
			}
			// one and two are still processing. Their follow-ups precede other
			// in the queue, but must not prevent another worker from taking it.
			for _, slug := range []string{"one", "two", "other", "last"} {
				q.Enqueue(slug)
			}
			for _, want := range []string{"other", "last"} {
				if got, ok := q.take(); !ok || got != want {
					t.Fatalf("take = (%q, %t), want %q", got, ok, want)
				}
				q.complete(want)
			}
			if got, ok := q.take(); ok {
				t.Fatalf("take = %q; both remaining teams are still processing", got)
			}
			// Release them in reverse order. Their pending order must survive
			// workers taking unrelated entries from the middle of the buffer.
			q.complete("two")
			q.complete("one")
			for _, want := range []string{"one", "two"} {
				if got, ok := q.take(); !ok || got != want {
					t.Fatalf("follow-up take = (%q, %t), want %q", got, ok, want)
				}
				q.complete(want)
			}
			if q.size != 0 || len(q.pending) != 0 || len(q.processing) != 0 {
				t.Fatal("drained queue must release waiting and processing state")
			}
			for _, slug := range q.slugs {
				if slug != "" {
					t.Fatalf("drained buffer retains %q", slug)
				}
			}
		})
	}
}

func TestTeamQueueBlockedFollowupWakesOnlyAfterCompletion(t *testing.T) {
	q := NewTeamQueue()
	q.Enqueue("active")
	<-q.wake
	q.take()
	for range 3 {
		if !q.Enqueue("active") {
			t.Fatal("follow-up must be accepted and coalesced")
		}
	}
	if _, ok := q.take(); ok {
		t.Fatal("follow-up must not be available while the team is active")
	}
	select {
	case <-q.wake:
		t.Fatal("blocked work must not keep waking idle workers")
	default:
	}
	q.complete("active")
	select {
	case <-q.wake:
	default:
		t.Fatal("completion must wake a worker for the follow-up")
	}
	if slug, ok := q.take(); !ok || slug != "active" {
		t.Fatalf("take = (%q, %t), want active follow-up", slug, ok)
	}
	q.complete("active")
	select {
	case <-q.wake:
		t.Fatal("completion without a follow-up must not wake workers")
	default:
	}
}

func TestTeamQueueActiveAndWaitingCapacityAreSeparate(t *testing.T) {
	q := NewTeamQueue()
	q.Enqueue("active")
	q.take()
	for i := range maxPendingTeams - 1 {
		if !q.Enqueue(fmt.Sprintf("waiting-%d", i)) {
			t.Fatalf("waiting slot %d rejected", i)
		}
	}
	for range 2 {
		if !q.Enqueue("active") {
			t.Fatal("one follow-up must fit and duplicates must coalesce")
		}
	}
	if q.size != maxPendingTeams || len(q.pending) != maxPendingTeams || len(q.processing) != 1 {
		t.Fatal("capacity must count waiting teams, not the additional active claim")
	}
	if q.Enqueue("retry") {
		t.Fatal("new team must be rejected when waiting capacity is full")
	}
	q.complete("active")
	if q.size != maxPendingTeams {
		t.Fatal("completion must not discard the accepted follow-up")
	}
	if slug, ok := q.take(); !ok || slug != "waiting-0" {
		t.Fatalf("take = (%q, %t), want waiting-0", slug, ok)
	}
	q.complete("waiting-0")
	if !q.Enqueue("retry") {
		t.Fatal("rejected team must be accepted when a waiting slot is freed")
	}
}

func TestTeamQueueConcurrentWorkersCannotClaimActiveFollowup(t *testing.T) {
	q := NewTeamQueue()
	q.Enqueue("active")
	q.take()
	q.Enqueue("active")
	const distinctTeams = 20
	for i := range distinctTeams {
		q.Enqueue(fmt.Sprintf("other-%d", i))
	}
	// Each contender holds its claim until all other contenders have tried.
	// The active team must never escape to a second worker.
	claims := make(chan string, distinctTeams+10)
	var workers sync.WaitGroup
	for range distinctTeams + 10 {
		workers.Go(func() {
			if slug, ok := q.take(); ok {
				claims <- slug
			}
		})
	}
	workers.Wait()
	close(claims)
	seen := make(map[string]bool)
	for slug := range claims {
		if slug == "active" || seen[slug] {
			t.Fatalf("concurrent claim for already-processing team %q", slug)
		}
		seen[slug] = true
		q.complete(slug)
	}
	if len(seen) != distinctTeams {
		t.Fatalf("distinct claims = %d, want %d", len(seen), distinctTeams)
	}
	q.complete("active")
	if slug, ok := q.take(); !ok || slug != "active" {
		t.Fatalf("take = (%q, %t), want active follow-up after completion", slug, ok)
	}
	q.complete("active")
	if _, ok := q.take(); ok {
		t.Fatal("only one follow-up may be pending")
	}
}
