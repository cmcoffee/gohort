package core

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// PipelineConfig describes a pipeline run. Apps fill this in and pass
// it to AppCore.RunPipeline, which handles session registration,
// persistent queuing, slot acquisition, notification, and cleanup.
type PipelineConfig struct {
	// ID runs the pipeline under this id instead of a fresh one. For work on
	// a record that already exists (re-synthesize it, fold children into it):
	// a page that opens the record by its id can then rejoin the run, which a
	// generated id would never match. Empty = generate one, as before.
	ID         string
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
	// Ephemeral runs without a persistent queue entry: it still waits for a
	// slot and gets recovery and the notice, but a restart does not bring it
	// back. For a run nothing could resume, which as a queue entry would sit
	// there forever, skipped and warned about at every start.
	Ephemeral bool
	// Name is what the notice and the queue view call this kind of run. App is
	// the queue's key and can be an internal one ("run:pipeline"). Empty =
	// App.
	Name string
	// ParentCtx roots the pipeline's context at a parent pipeline's
	// ctx instead of AppContext(). Set this when one pipeline spawns
	// another (e.g. autoblog → debate/research) so cancelling the
	// parent propagates into the child's LLM calls through normal
	// ctx derivation rather than requiring manual cancel-tree
	// bookkeeping. Leave nil for top-level pipelines.
	ParentCtx context.Context
}

// pipelineName is what people see this run called.
func pipelineName(cfg PipelineConfig) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	return cfg.App
}

// pipelineQueueAdd and pipelineQueueRemove keep a run's persistent queue
// entry, unless it is ephemeral.
func pipelineQueueAdd(id string, cfg PipelineConfig) {
	if !cfg.Ephemeral {
		QueueAdd(id, cfg.App, cfg.Label, cfg.Params, cfg.NotifyUser)
	}
}

func pipelineQueueRemove(id string, cfg PipelineConfig) {
	if !cfg.Ephemeral {
		QueueRemove(id)
	}
}

// pipelineNotify sends a finished run's notices: each user on its notify list
// and the admin. Needs a record id and a link path; a run with neither has
// nothing to point anyone at. An ephemeral run has no queue entry to hold the
// list, so its one NotifyUser is the list.
func pipelineNotify(cfg PipelineConfig, id, recordID string) {
	if recordID == "" || cfg.LinkPath == "" {
		return
	}
	link := DashboardURL() + cfg.LinkPath + recordID
	var users []string
	if cfg.Ephemeral {
		if cfg.NotifyUser != "" {
			users = []string{cfg.NotifyUser}
		}
	} else {
		users = QueueGetNotifyUsers(id)
	}
	name := pipelineName(cfg)
	subject := "[" + ServiceName() + "] " + name + " complete: " + cfg.Label
	body := fmt.Sprintf("Your %s has completed on %s.\n\n%s\n\n%s\n", name, DashboardURL(), cfg.Label, link)
	for _, nu := range users {
		NotifyUser(nu, subject, body)
	}
	NotifyAdmin(subject, body, users...)
}

// pipelineID is the id a run goes by: the caller's, else a fresh one.
func pipelineID(cfg PipelineConfig) string {
	if cfg.ID != "" {
		return cfg.ID
	}
	return UUIDv4()
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
	id        string
	cfg       PipelineConfig
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
func (T *AppCore) RunPipeline(cfg PipelineConfig, work PipelineWork) string {
	id := pipelineID(cfg)
	ctx, cancel := context.WithCancel(pipelineRoot(cfg))

	// 1. Register live session.
	if cfg.OnRegister != nil {
		cfg.OnRegister(id, cancel)
	}

	// 2. Persist to queue.
	pipelineQueueAdd(id, cfg)

	// 3. Acquire slot from global queue.
	if !GlobalQueue().Acquire(ctx, id, cfg.Label, pipelineName(cfg), cfg.LinkPath, func(position int) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, fmt.Sprintf("Position in queue: %d", position), false)
		}
	}) {
		// Cancelled while queued.
		cancel()
		pipelineQueueRemove(id, cfg)
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
		pipelineNotify(cfg, id, pc.record_id)
		// 6. Cleanup.
		pipelineQueueRemove(id, cfg)
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
func (T *AppCore) RunPipelineAsync(cfg PipelineConfig, work PipelineWork) string {
	id := pipelineID(cfg)
	ctx, cancel := context.WithCancel(pipelineRoot(cfg))

	if cfg.OnRegister != nil {
		cfg.OnRegister(id, cancel)
	}

	pipelineQueueAdd(id, cfg)

	go func() {
		if !GlobalQueue().Acquire(ctx, id, cfg.Label, pipelineName(cfg), cfg.LinkPath, func(position int) {
			if cfg.OnEvent != nil {
				cfg.OnEvent(id, fmt.Sprintf("Position in queue: %d", position), false)
			}
		}) {
			pipelineQueueRemove(id, cfg)
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
			pipelineNotify(cfg, id, pc.record_id)
			pipelineQueueRemove(id, cfg)
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
func (T *AppCore) RestorePipeline(entry QueueEntry, cfg PipelineConfig, work PipelineWork) {
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

	if !GlobalQueue().Acquire(ctx, id, cfg.Label, pipelineName(cfg), cfg.LinkPath, func(position int) {
		if cfg.OnEvent != nil {
			cfg.OnEvent(id, fmt.Sprintf("Position in queue: %d", position), false)
		}
	}) {
		cancel()
		pipelineQueueRemove(id, cfg)
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
		pipelineNotify(cfg, id, pc.record_id)
		pipelineQueueRemove(id, cfg)
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
