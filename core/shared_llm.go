package core

import (
	"context"
	"errors"
	"sync"
)

// Process-wide default worker and lead LLM references. Set at startup by the
// application, and re-set live when the admin UI changes LLM config (so a model
// / provider / key swap takes effect without a restart). Apps hold a stable
// reloadable handle (ReloadableWorkerLLM / ReloadableLeadLLM) rather than the
// concrete LLM, so a swap reaches every reference without re-threading.
var (
	sharedMu        sync.RWMutex
	sharedWorkerLLM LLM
	sharedLeadLLM   LLM
	llmReloader     func() error
)

// SetSharedLLMs registers the process-wide default worker and lead LLMs. Called
// at startup and again on a live reload. Safe to call concurrently with chat
// traffic — the swap is under a write lock and reads take the read lock.
func SetSharedLLMs(worker, lead LLM) {
	sharedMu.Lock()
	sharedWorkerLLM = worker
	sharedLeadLLM = lead
	sharedMu.Unlock()
}

// SharedWorkerLLM returns the default worker LLM, or nil if none has been
// configured. Tools should guard against nil and return a descriptive error
// rather than panic when the app hasn't wired an LLM (e.g., `--help`).
func SharedWorkerLLM() LLM {
	sharedMu.RLock()
	defer sharedMu.RUnlock()
	return sharedWorkerLLM
}

// SharedLeadLLM returns the default lead LLM, or nil if not configured.
func SharedLeadLLM() LLM {
	sharedMu.RLock()
	defer sharedMu.RUnlock()
	return sharedLeadLLM
}

// RegisterLLMReloader installs the function that rebuilds the shared worker +
// lead LLMs from current config and swaps them in via SetSharedLLMs. Called once
// at startup by the main package (which owns config access). Lets the admin UI
// apply LLM config changes live.
func RegisterLLMReloader(fn func() error) { llmReloader = fn }

// ReloadLLMs rebuilds the shared LLMs from current config. No-op when no reloader
// is registered (e.g. CLI invocations). Returns the rebuild error so the caller
// can surface a bad config; on error the previous LLMs stay active.
func ReloadLLMs() error {
	if llmReloader == nil {
		return nil
	}
	return llmReloader()
}

// reloadableLLM forwards every call to whatever the getter currently returns, so
// a handle captured once (by an app at startup) always uses the live shared LLM
// after a reload. The LLM interface is just Chat + ChatStream.
type reloadableLLM struct{ get func() LLM }

func (r reloadableLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	llm := r.get()
	if llm == nil {
		return nil, errors.New("no LLM configured")
	}
	return llm.Chat(ctx, messages, opts...)
}

func (r reloadableLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	llm := r.get()
	if llm == nil {
		return nil, errors.New("no LLM configured")
	}
	return llm.ChatStream(ctx, messages, handler, opts...)
}

// ContextSize forwards the underlying LLM's ContextSizer, mirroring retryLLM.
// Without it, T.LLM.(ContextSizer) fails through this wrapper and returns 0,
// silently disabling every context-size-dependent feature (history compaction,
// context math). Returns 0 when the underlying exposes no window. (Pinger is
// deliberately NOT implemented — retryLLM doesn't either, so the existing
// assertion behavior is preserved.)
func (r reloadableLLM) ContextSize() int {
	if cs, ok := r.get().(ContextSizer); ok {
		return cs.ContextSize()
	}
	return 0
}

// ReloadableWorkerLLM returns a stable handle that always forwards to the current
// shared worker LLM — apps hold this so a live reload reaches them.
func ReloadableWorkerLLM() LLM { return reloadableLLM{get: SharedWorkerLLM} }

// ReloadableLeadLLM returns a stable handle for the lead LLM, falling back to the
// worker when no lead is configured ("use primary"). Always forwards (never a nil
// interface) so reconfiguring the lead provider later also takes effect live.
func ReloadableLeadLLM() LLM {
	return reloadableLLM{get: func() LLM {
		if l := SharedLeadLLM(); l != nil {
			return l
		}
		return SharedWorkerLLM()
	}}
}
