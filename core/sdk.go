// SDK convenience surface — the minimal entry point for using core as an agent
// library (no server, no boot, no database). Build an LLM from a provider
// config, then run agentic turns with your own tools. This is Phase 0 of the
// SDK plan: the agent loop is already global-free, so this just packages the
// pattern. The richer subsystems (persistent memory, RAG) still read the RootDB
// family of package globals; see docs/sdk-decoupling-scope.md for decoupling.
package core

import (
	"context"
	"fmt"
)

// NewAgent builds a minimal agent from an LLM provider config. It wires the LLM
// into an AppCore and nothing else: no database, no route stages, no server, no
// boot. For a local model set Provider "llama.cpp" or "ollama" with an Endpoint;
// for a hosted one set "anthropic" / "openai" / "gemini" with an APIKey.
//
//	agent, err := core.NewAgent(core.LLMProviderConfig{
//	    Provider: "anthropic", APIKey: key, Model: "claude-sonnet-5",
//	})
//	reply, err := agent.RunOnce(ctx, "hello", myTools)
func NewAgent(cfg LLMProviderConfig) (*AppCore, error) {
	llm, err := NewLLMFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &AppCore{LLM: llm}, nil
}

// RunOnce runs a single agentic turn: it sends message as the user turn and lets
// the model call tools across rounds, returning the final reply text. A thin
// wrapper over RunAgentLoop for the common case (no routing, sane defaults).
// Reach for RunAgentLoop directly when you need prior history, streaming,
// per-stage routing, confirmation gates, or custom round options.
func (T *AppCore) RunOnce(ctx context.Context, message string, tools []AgentToolDef) (string, error) {
	if T == nil || T.LLM == nil {
		return "", fmt.Errorf("agent has no LLM configured (build it with core.NewAgent)")
	}
	resp, _, err := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: message}}, AgentLoopConfig{
		Tools:     tools,
		MaxRounds: 10,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("no response from agent")
	}
	return resp.Content, nil
}

// Remember saves a fact to explicit ("always-in-prompt") memory under a
// namespace you pick, using the agent's DB. Returns true when the fact was newly
// stored (false if a near-duplicate already existed). Set AppCore.DB to your own
// kvlite/Database instance first. Works with no server boot; semantic dedup is
// off unless you configure embeddings (a Phase 1 SDK item, see
// docs/sdk-decoupling-scope.md). Recall the facts and put them in your system
// prompt to give the model persistent memory across turns.
func (T *AppCore) Remember(namespace, fact string) (bool, error) {
	if T == nil || T.DB == nil {
		return false, fmt.Errorf("no store configured (set AppCore.DB before Remember)")
	}
	// SDK callers load facts programmatically from their own data → imported
	// on the provenance envelope (distinguishable from in-session writes;
	// excluded from the grounding corpus).
	res := StoreMemoryFactP(T.DB, namespace, fact, FactWritePolicy{Source: MemSourceImported})
	return res.Reason == FactStored, nil
}

// Recall returns the facts stored under a namespace, for injection into a system
// prompt or your own use. Set AppCore.DB first.
func (T *AppCore) Recall(namespace string) []MemoryFact {
	if T == nil || T.DB == nil {
		return nil
	}
	return ListMemoryFacts(T.DB, namespace)
}
