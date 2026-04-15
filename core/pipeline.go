package core

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// PipelineConfig describes a pipeline run. Apps fill this in and pass
// it to FuzzAgent.RunPipeline, which handles session registration,
// persistent queuing, slot acquisition, notification, and cleanup.
type PipelineConfig struct {
	App        string      // app identifier for queue/logging
	Label      string      // human-readable label (topic, question)
	Params     interface{} // app-specific queue params (JSON-marshalable)
	NotifyUser string      // user to notify on completion (email)
	LinkPath   string      // URL path template for notification links (e.g. "/myapp/?id=")

	// Session callbacks — the app wires these to its LiveSessionMap.
	OnRegister func(id string, cancel context.CancelFunc) // register the live session
	OnEvent    func(id string, status string, done bool)  // update session status
	OnCleanup  func(id string)                            // schedule session cleanup
	// OnStarted fires exactly once, after the queue slot has been
	// acquired and immediately before the work function is invoked.
	// Restore paths use it to clear LiveSessionMap.ClearRestoring so
	// a stale browser cancel racing with the restore cannot kill the
	// pipeline before it gets a chance to run. Safe to leave nil for
	// fresh RunPipeline / RunPipelineAsync paths.
	OnStarted func(id string)
	// ParentCtx roots the pipeline's context at a parent pipeline's
	// ctx instead of AppContext(). Set this when one pipeline spawns
	// another (e.g. autoblog → debate/research) so cancelling the
	// parent propagates into the child's LLM calls through normal
	// ctx derivation rather than requiring manual cancel-tree
	// bookkeeping. Leave nil for top-level pipelines.
	ParentCtx context.Context
}

// pipelineRoot returns the parent context the pipeline should derive
// its own WithCancel from. Falls back to the process-lifetime
// AppContext when cfg.ParentCtx is not set.
func pipelineRoot(cfg PipelineConfig) context.Context {
	if cfg.ParentCtx != nil {
		return cfg.ParentCtx
	}
	return AppContext()
}

// PipelineResult is returned to the app after the pipeline completes.
type PipelineResult struct {
	ID     string // the generated pipeline ID
	RecID  string // record ID set by the work function via SetRecordID
	Cancel context.CancelFunc
}

// PipelineCtx is passed to the work function so it can report status
// and set the record ID for notification links.
type PipelineCtx struct {
	id       string
	cfg      PipelineConfig
	record_id string
}

// SetRecordID sets the final record ID used in notification links.
// Call this from the work function when the result is persisted.
func (p *PipelineCtx) SetRecordID(id string) {
	p.record_id = id
}

// Status updates the live session status text.
func (p *PipelineCtx) Status(msg string) {
	if p.cfg.OnEvent != nil {
		p.cfg.OnEvent(p.id, msg, false)
	}
}

// PipelineWork is the function signature for the app's business logic.
// The context is cancelled if the user cancels. Use pc.SetRecordID()
// to set the result ID for notification links, and pc.Status() to
// update the live session status.
type PipelineWork func(ctx context.Context, pc *PipelineCtx) error

// RunPipeline executes a pipeline with full lifecycle management:
// session registration, persistent queue, slot acquisition, work
// execution, notification, and cleanup. Returns the pipeline ID.
func (T *FuzzAgent) RunPipeline(cfg PipelineConfig, work PipelineWork) string {
	id := UUIDv4()
	ctx, cancel := context.WithCancel(pipelineRoot(cfg))

	// 1. Register live session.
	if cfg.OnRegister != nil {
		cfg.OnRegister(id, cancel)
	}

	// 2. Persist to queue.
	QueueAdd(id, cfg.App, cfg.Label, cfg.Params, cfg.NotifyUser)

	// 3. Acquire slot from global queue.
	if !GlobalQueue().Acquire(ctx, id, cfg.Label, func(position int) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, fmt.Sprintf("Position in queue: %d", position), false)
		}
	}) {
		// Cancelled while queued.
		cancel()
		QueueRemove(id)
		if cfg.OnCleanup != nil {
			cfg.OnCleanup(id)
		}
		return id
	}

	// 4. Run the work function.
	pc := &PipelineCtx{id: id, cfg: cfg}

	defer func() {
		p := recover()
		cancel()
		GlobalQueue().Release()
		if p != nil {
			// Panic: log the stack and leave the queue entry in place
			// so a restart rehydrates and retries the pipeline. Do NOT
			// re-panic -- we've handled it cleanly and don't want to
			// tear down the caller's goroutine.
			Err("[%s] pipeline %s panicked: %v\n%s", cfg.App, id[:8], p, debug.Stack())
			if cfg.OnEvent != nil {
				cfg.OnEvent(id, fmt.Sprintf("Internal error: %v (will retry on restart)", p), true)
			}
			if cfg.OnCleanup != nil {
				cfg.OnCleanup(id)
			}
			return
		}
		// 5. Notify on completion.
		if pc.record_id != "" && cfg.LinkPath != "" {
			link := DashboardURL() + cfg.LinkPath + pc.record_id
			users := QueueGetNotifyUsers(id)
			subject := "[" + ServiceName() + "] " + cfg.App + " complete: " + cfg.Label
			body := fmt.Sprintf("Your %s has completed on %s.\n\n%s\n\n%s\n", cfg.App, DashboardURL(), cfg.Label, link)
			for _, nu := range users {
				NotifyUser(nu, subject, body)
			}
			NotifyAdmin(subject, body, users...)
		}
		// 6. Cleanup.
		QueueRemove(id)
		if cfg.OnCleanup != nil {
			cfg.OnCleanup(id)
		}
	}()

	if cfg.OnStarted != nil {
		cfg.OnStarted(id)
	}

	if err := work(ctx, pc); err != nil {
		Log("[%s] pipeline %s error: %v", cfg.App, id[:8], err)
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, "Error: "+err.Error(), true)
		}
	} else {
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, "Complete", true)
		}
	}

	return id
}

// RunPipelineAsync is like RunPipeline but runs in a goroutine and
// returns the pipeline ID immediately.
func (T *FuzzAgent) RunPipelineAsync(cfg PipelineConfig, work PipelineWork) string {
	id := UUIDv4()
	ctx, cancel := context.WithCancel(pipelineRoot(cfg))

	if cfg.OnRegister != nil {
		cfg.OnRegister(id, cancel)
	}

	QueueAdd(id, cfg.App, cfg.Label, cfg.Params, cfg.NotifyUser)

	go func() {
		if !GlobalQueue().Acquire(ctx, id, cfg.Label, func(position int) {
			if cfg.OnEvent != nil {
				cfg.OnEvent(id, fmt.Sprintf("Position in queue: %d", position), false)
			}
		}) {
			QueueRemove(id)
			if cfg.OnCleanup != nil {
				cfg.OnCleanup(id)
			}
			return
		}

		pc := &PipelineCtx{id: id, cfg: cfg}

		defer func() {
			p := recover()
			cancel()
			GlobalQueue().Release()
			if p != nil {
				Err("[%s] pipeline %s panicked: %v\n%s", cfg.App, id[:8], p, debug.Stack())
				if cfg.OnEvent != nil {
					cfg.OnEvent(id, fmt.Sprintf("Internal error: %v (will retry on restart)", p), true)
				}
				if cfg.OnCleanup != nil {
					cfg.OnCleanup(id)
				}
				return
			}
			if pc.record_id != "" && cfg.LinkPath != "" {
				link := DashboardURL() + cfg.LinkPath + pc.record_id
				users := QueueGetNotifyUsers(id)
				subject := "[" + ServiceName() + "] " + cfg.App + " complete: " + cfg.Label
				body := fmt.Sprintf("Your %s has completed on %s.\n\n%s\n\n%s\n", cfg.App, DashboardURL(), cfg.Label, link)
				for _, nu := range users {
					NotifyUser(nu, subject, body)
				}
				NotifyAdmin(subject, body, users...)
			}
			QueueRemove(id)
			if cfg.OnCleanup != nil {
				cfg.OnCleanup(id)
			}
		}()

		if cfg.OnStarted != nil {
			cfg.OnStarted(id)
		}

		if err := work(ctx, pc); err != nil {
			Log("[%s] pipeline %s error: %v", cfg.App, id[:8], err)
			if cfg.OnEvent != nil {
				cfg.OnEvent(id, "Error: "+err.Error(), true)
			}
		} else {
			if cfg.OnEvent != nil {
				cfg.OnEvent(id, "Complete", true)
			}
		}
	}()

	return id
}

// RestorePipeline re-runs a pipeline from a persistent queue entry.
// Called by QueueHandler implementations registered via RegisterQueueHandler.
func (T *FuzzAgent) RestorePipeline(entry QueueEntry, cfg PipelineConfig, work PipelineWork) {
	id := entry.ID
	ctx, cancel := context.WithCancel(pipelineRoot(cfg))

	if cfg.OnRegister != nil {
		cfg.OnRegister(id, cancel)
	}
	if cfg.OnEvent != nil {
		cfg.OnEvent(id, "Restoring from queue...", false)
	}

	// Brief delay to let the server finish starting.
	time.Sleep(2 * time.Second)

	if !GlobalQueue().Acquire(ctx, id, cfg.Label, func(position int) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, fmt.Sprintf("Position in queue: %d", position), false)
		}
	}) {
		cancel()
		QueueRemove(id)
		if cfg.OnCleanup != nil {
			cfg.OnCleanup(id)
		}
		return
	}

	pc := &PipelineCtx{id: id, cfg: cfg}

	defer func() {
		p := recover()
		cancel()
		GlobalQueue().Release()
		if p != nil {
			Err("[%s] restored pipeline %s panicked: %v\n%s", cfg.App, id[:8], p, debug.Stack())
			if cfg.OnEvent != nil {
				cfg.OnEvent(id, fmt.Sprintf("Internal error: %v (will retry on restart)", p), true)
			}
			if cfg.OnCleanup != nil {
				cfg.OnCleanup(id)
			}
			return
		}
		if pc.record_id != "" && cfg.LinkPath != "" {
			link := DashboardURL() + cfg.LinkPath + pc.record_id
			users := QueueGetNotifyUsers(id)
			subject := "[" + ServiceName() + "] " + cfg.App + " complete: " + cfg.Label
			body := fmt.Sprintf("Your %s has completed on %s.\n\n%s\n\n%s\n", cfg.App, DashboardURL(), cfg.Label, link)
			for _, nu := range users {
				NotifyUser(nu, subject, body)
			}
			NotifyAdmin(subject, body, users...)
		}
		QueueRemove(id)
		if cfg.OnCleanup != nil {
			cfg.OnCleanup(id)
		}
	}()

	Log("[queue] restored %s/%s: %s", cfg.App, id[:8], truncLabel(cfg.Label))

	if cfg.OnStarted != nil {
		cfg.OnStarted(id)
	}

	if err := work(ctx, pc); err != nil {
		Log("[%s] restored pipeline %s error: %v", cfg.App, id[:8], err)
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, "Error: "+err.Error(), true)
		}
	} else {
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, "Complete", true)
		}
	}
}
