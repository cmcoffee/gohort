package core

import (
	"context"
	"sync"
)

// The app context is the root context for everything that should
// outlive individual HTTP requests but die when the daemon is shutting
// down — persistent pipelines, task-queue work, interactive REPLs, etc.
// It is created in main() via InitAppContext(), cancelled once via
// ShutdownApp() on SIGINT / SIGTERM / normal exit, and is the correct
// parent for any work that should be torn down cleanly on daemon close.
var (
	app_ctx_mu     sync.RWMutex
	app_ctx        context.Context = context.Background()
	app_ctx_cancel context.CancelFunc
)

// InitAppContext builds the process-lifetime context off the given
// parent (usually context.Background()) and stores it for retrieval via
// AppContext(). Call once from main() before any agent / pipeline work
// starts. Returns the CancelFunc so the caller can wire it to signal
// handlers; ShutdownApp() is the normal way to trigger it.
func InitAppContext(parent context.Context) context.CancelFunc {
	app_ctx_mu.Lock()
	defer app_ctx_mu.Unlock()
	app_ctx, app_ctx_cancel = context.WithCancel(parent)
	return app_ctx_cancel
}

// AppContext returns the process-lifetime context. Before
// InitAppContext runs it returns context.Background(), so code loading
// at init() time sees a safe default.
func AppContext() context.Context {
	app_ctx_mu.RLock()
	defer app_ctx_mu.RUnlock()
	return app_ctx
}

// ShutdownApp cancels the app context if one has been installed,
// signalling every goroutine rooted off AppContext() to wind down.
// Safe to call multiple times; subsequent calls are no-ops.
func ShutdownApp() {
	app_ctx_mu.Lock()
	cancel := app_ctx_cancel
	app_ctx_cancel = nil
	app_ctx_mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// BgContext returns the process-lifetime app context. Agents should
// prefer this over context.Background() for any work that must survive
// the request that started it (task queue submissions, persistent
// pipelines, REPL loops) while still being cancellable on daemon
// shutdown.
func (T *AppCore) BgContext() context.Context {
	return AppContext()
}
