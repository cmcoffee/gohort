package core

import (
	"context"
	"sync"
)

// SetupWebAgentFunc is set by the application to initialize agents for the
// central web server (sets up DB buckets, LLM config, etc.).
var SetupWebAgentFunc func(agent Agent)

// MaxConcurrentTasks is the maximum number of simultaneous apps allowed.
// Set by the --max_concurrent flag. Default 1.
var MaxConcurrentTasks = 1

// globalQueue is a shared queue for all apps. Tasks acquire a slot before
// starting and release it when done. Other tasks wait in FIFO order.
var globalQueue = &TaskQueue{
	notify: make(chan struct{}, 1),
}

// GlobalQueue returns the shared task queue.
func GlobalQueue() *TaskQueue { return globalQueue }

// TaskQueue manages a shared FIFO queue across all apps.
type TaskQueue struct {
	mu     sync.Mutex
	active int
	queue  []queueWaiter
	notify chan struct{}
}

type queueWaiter struct {
	id       string
	label    string
	app      string // app name for live view
	linkPath string // URL path for linking (e.g. "/myapp/?session=")
	ready    chan struct{}
	ctx      context.Context
}

// Acquire blocks until a slot is available or ctx is cancelled.
// onQueue is called with the queue position whenever it changes.
// Returns false if ctx was cancelled.
func (q *TaskQueue) Acquire(ctx context.Context, id, label, app, linkPath string, onQueue func(position int)) bool {
	q.mu.Lock()
	if q.active < MaxConcurrentTasks {
		q.active++
		q.mu.Unlock()
		return true
	}

	// Queue this request.
	w := queueWaiter{
		id:       id,
		label:    label,
		app:      app,
		linkPath: linkPath,
		ready:    make(chan struct{}, 1),
		ctx:      ctx,
	}
	q.queue = append(q.queue, w)
	pos := len(q.queue)
	q.mu.Unlock()

	Log("[queue] %s queued at position %d: %s", id[:8], pos, label)

	if onQueue != nil {
		onQueue(pos)
	}

	for {
		select {
		case <-w.ready:
			Log("[queue] %s promoted — starting", id[:8])
			return true
		case <-ctx.Done():
			Log("[queue] %s context cancelled — removing from queue", id[:8])
			q.mu.Lock()
			for i, qw := range q.queue {
				if qw.id == id {
					q.queue = append(q.queue[:i], q.queue[i+1:]...)
					break
				}
			}
			q.mu.Unlock()
			return false
		case <-q.notify:
			// Check if we moved up in the queue.
			q.mu.Lock()
			found := false
			for i, qw := range q.queue {
				if qw.id == id {
					found = true
					q.mu.Unlock()
					if onQueue != nil {
						onQueue(i + 1)
					}
					break
				}
			}
			if !found {
				q.mu.Unlock()
			}
		}
	}
}

// CancelQueued removes a queued item by ID. Returns true if found and removed.
func (q *TaskQueue) CancelQueued(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, w := range q.queue {
		if w.id == id {
			q.queue = append(q.queue[:i], q.queue[i+1:]...)
			return true
		}
	}
	return false
}

// QueuedEntries returns a list of items waiting in the queue.
func (q *TaskQueue) QueuedEntries() []LiveEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	var entries []LiveEntry
	for _, w := range q.queue {
		entry := LiveEntry{ID: w.id, Label: w.label, Queued: true, App: w.app}
		if w.linkPath != "" {
			entry.URL = w.linkPath + w.id
		}
		entries = append(entries, entry)
	}
	return entries
}

// Release frees a slot and starts the next queued task.
func (q *TaskQueue) Release() {
	q.mu.Lock()
	if len(q.queue) > 0 {
		// Start the next queued task — don't decrement active.
		next := q.queue[0]
		q.queue = q.queue[1:]
		remaining := len(q.queue)
		q.mu.Unlock()
		Log("[queue] slot released — promoting %s (%d still queued)", next.id[:8], remaining)
		select {
		case next.ready <- struct{}{}:
		default:
		}
		// Notify remaining queue members of position change.
		select {
		case q.notify <- struct{}{}:
		default:
		}
	} else {
		q.active--
		q.mu.Unlock()
		Log("[queue] slot released — no items queued (active: %d)", q.active)
	}
}
