package syncer

import (
	"context"
	"sync"
	"time"
)

// scheduler alternates full cycles and targeted worker-pool phases. A phase must
// finish all active work before the next phase starts, including on shutdown.
type scheduler struct {
	queue    *TeamQueue
	workers  int
	interval time.Duration
	fullSync func(context.Context)
	teamSync func(context.Context, string)
}

func (w *scheduler) run(ctx context.Context) {
	for ctx.Err() == nil {
		w.fullSync(ctx)
		if ctx.Err() != nil {
			return
		}
		w.targetedPhase(ctx)
	}
}

func (w *scheduler) targetedPhase(ctx context.Context) {
	// Each completed full sync, successful or not, starts a new poll interval.
	// Workers check the deadline before claiming work; notifications never reset it.
	nextPoll := time.Now().Add(w.interval)
	timer := time.NewTimer(w.interval)
	defer timer.Stop()
	stop := make(chan struct{})
	var workers sync.WaitGroup
	if w.queue != nil {
		for range w.workers {
			workers.Go(func() { w.targetedWorker(ctx, nextPoll, stop) })
		}
	}
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	close(stop)
	// stop prevents new work, but active jobs retain the deployment context and
	// their team timeout. Only shutdown cancels them; a due poll does not.
	workers.Wait()
}

func (w *scheduler) targetedWorker(ctx context.Context, nextPoll time.Time, stop <-chan struct{}) {
	for ctx.Err() == nil && time.Now().Before(nextPoll) {
		if slug, ok := w.queue.take(); ok {
			w.teamSync(ctx, slug)
			w.queue.complete(slug)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-w.queue.wake:
		}
	}
}
