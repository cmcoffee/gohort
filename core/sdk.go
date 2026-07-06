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
