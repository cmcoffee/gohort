package main

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tier is built with its key only while the key was given for the address
// the tier goes to: the worker's at the address it was saved for, the lead's
// pointed elsewhere since, with none.
func TestATierIsBuiltWithTheKeyForItsAddress(t *testing.T) {
	saved := global.db
	t.Cleanup(func() { global.db = saved })
	db := &DBase{Store: kvlite.MemStore()}
	global.db = db

	db.Set(LLMTable, "provider", "openai")
	db.Set(LLMTable, "endpoint", "https://api.groq.example/openai/v1")
	LLMProviderConfig{Provider: "openai", Endpoint: "https://api.groq.example/openai/v1"}.SaveKey(db, LLMTable, "gsk-worker-key-0000")
	if got := dbcfg.llm(); got.APIKey != "gsk-worker-key-0000" {
		t.Errorf("the worker's key: %q", got.APIKey)
	}

	db.Set(LeadLLMTable, "provider", "anthropic")
	LLMProviderConfig{Provider: "anthropic"}.SaveKey(db, LeadLLMTable, "sk-ant-lead-key-0000")
	db.Set(LeadLLMTable, "endpoint", "https://gateway.example/anthropic")
	if got := dbcfg.leadLLM(); got.APIKey != "" {
		t.Errorf("the lead's key went to an address it was not given for: %q", got.APIKey)
	}
}

// The Judge model is built with thinking off, whatever is stored: a check
// that asks to think goes to the worker, and a model that thinks by default
// would otherwise think through every check that forgot to say so. It is
// also built as a server of its own, outside the worker's queue.
func TestTheJudgeModelNeverThinks(t *testing.T) {
	saved := global.db
	t.Cleanup(func() { global.db = saved })
	db := &DBase{Store: kvlite.MemStore()}
	global.db = db
	db.Set(JudgeLLMTable, "provider", "vllm")
	db.Set(JudgeLLMTable, "disable_thinking", false)
	got := dbcfg.judgeLLM()
	if !got.DisableThinking || !got.SeparateServer {
		t.Errorf("the Judge model's config: thinking disabled %v, its own server %v", got.DisableThinking, got.SeparateServer)
	}
}
