package core

// Process-wide default worker and lead LLM references. Set at startup
// by the application so stateless chat tools (which don't carry an
// agent handle) can reach an LLM. Not a safe pattern for arbitrary
// concurrency primitives — the writes happen once, before any tool
// is registered or called, so the zero-sync read is fine.
var (
	sharedWorkerLLM LLM
	sharedLeadLLM   LLM
)

// SetSharedLLMs registers the process-wide default worker and lead
// LLMs. Called once at startup after the application resolves its
// LLM configuration. Tools that need to make inline LLM calls without
// owning an agent (e.g., shell simulator, paraphrase helpers) read
// these via SharedWorkerLLM / SharedLeadLLM.
func SetSharedLLMs(worker, lead LLM) {
	sharedWorkerLLM = worker
	sharedLeadLLM = lead
}

// SharedWorkerLLM returns the default worker LLM, or nil if none
// has been configured. Tools should guard against nil and return a
// descriptive error rather than panic when the app hasn't wired
// an LLM (e.g., `--help` invocations).
func SharedWorkerLLM() LLM { return sharedWorkerLLM }

// SharedLeadLLM returns the default lead LLM, or nil if not configured.
func SharedLeadLLM() LLM { return sharedLeadLLM }
