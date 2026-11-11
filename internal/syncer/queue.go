package syncer

import "sync"

const maxPendingTeams = 1024

// TeamQueue holds distinct slugs waiting for a fresh read. Producers and workers
// may access it concurrently, but a slug can be processed by only one worker at a
// time. Construct it with NewTeamQueue; each successful take requires one complete.
type TeamQueue struct {
	mu         sync.Mutex
	pending    map[string]struct{}
	processing map[string]struct{}
	slugs      [maxPendingTeams]string
	head       int
	size       int
	// wake is a notification, not storage for work. A buffered notification is
	// enough: take passes it on when another team is ready, and complete wakes a
	// worker when a blocked follow-up becomes ready.
	wake chan struct{}
}

func NewTeamQueue() *TeamQueue {
	return &TeamQueue{
		pending:    make(map[string]struct{}),
		processing: make(map[string]struct{}),
		wake:       make(chan struct{}, 1),
	}
}

// Enqueue never waits for a sync. False means the queue cannot accept the team;
// the webhook sender should retry. Already-pending teams count as accepted.
func (q *TeamQueue) Enqueue(slug string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if slug == "" {
		return false
	}
	if _, exists := q.pending[slug]; exists {
		return true
	}
	if q.size == len(q.slugs) {
		return false
	}
	tail := (q.head + q.size) % len(q.slugs)
	q.slugs[tail] = slug
	q.size++
	q.pending[slug] = struct{}{}
	if _, active := q.processing[slug]; !active {
		q.signal()
	}
	return true
}

// take claims the oldest ready slug. Follow-ups for active teams stay in place;
// workers skip them rather than block unrelated work. Events arriving after take
// may queue one follow-up, but it cannot be claimed until complete releases the slug.
func (q *TeamQueue) take() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for offset := range q.size {
		slug := q.slugs[(q.head+offset)%len(q.slugs)]
		if _, active := q.processing[slug]; active {
			continue
		}
		// Close the gap without reordering waiting follow-ups. Moving the prefix
		// lets the usual head removal stay O(1); skipped work is bounded by 1024.
		for i := offset; i > 0; i-- {
			q.slugs[(q.head+i)%len(q.slugs)] = q.slugs[(q.head+i-1)%len(q.slugs)]
		}
		q.slugs[q.head] = ""
		q.head = (q.head + 1) % len(q.slugs)
		q.size--
		delete(q.pending, slug)
		q.processing[slug] = struct{}{}
		if q.hasReady() {
			q.signal()
		}
		return slug, true
	}
	return "", false
}

// complete releases a claim after success, failure, timeout, or cancellation.
// Processing teams do not consume pending capacity; their follow-ups do.
func (q *TeamQueue) complete(slug string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.processing, slug)
	if _, waiting := q.pending[slug]; waiting {
		q.signal()
	}
}

// hasReady requires q.mu. Blocked follow-ups alone must not cause workers to spin.
func (q *TeamQueue) hasReady() bool {
	for offset := range q.size {
		if _, active := q.processing[q.slugs[(q.head+offset)%len(q.slugs)]]; !active {
			return true
		}
	}
	return false
}

func (q *TeamQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
