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

// A bare `gohort serve` listens where --setup said it would: the saved
// address, else :8080. An address given on the command line wins.
func TestServeListensWhereSetupSaid(t *testing.T) {
	savedDB := global.db
	prev := global.cfg.Get(cfgWebSection, "addr")
	t.Cleanup(func() {
		global.db = savedDB
		_ = global.cfg.Set(cfgWebSection, "addr", prev)
	})
	global.db = nil
	_ = global.cfg.Set(cfgWebSection, "addr", "127.0.0.1:8181")
	if got := serveAddr(""); got != "127.0.0.1:8181" {
		t.Errorf("with an address saved: %q", got)
	}
	if got := serveAddr(" :9000 "); got != ":9000" {
		t.Errorf("with an address given: %q", got)
	}
	_ = global.cfg.Set(cfgWebSection, "addr", "")
	if got := serveAddr(""); got != ":8080" {
		t.Errorf("with none saved: %q", got)
	}
}

// A brand-new install's users get Agents and Knowledge; an install with
// accounts, or whose default apps were ever saved (even as none), keeps its
// own.
func TestANewInstallsUsersGetAgentsAndKnowledge(t *testing.T) {
	fresh := &DBase{Store: kvlite.MemStore()}
	seedNewInstallApps(fresh)
	got := AuthResolveUserApps(fresh, AuthUser{Username: "pat"})
	if len(got) != 2 || got[0] != "/orchestrate" || got[1] != "/knowledge" {
		t.Errorf("a new install's user gets %v", got)
	}

	chosen := &DBase{Store: kvlite.MemStore()}
	AuthSetDefaultApps(chosen, []string{})
	seedNewInstallApps(chosen)
	if got := AuthGetDefaultApps(chosen); len(got) != 0 {
		t.Errorf("an administrator's empty list was replaced: %v", got)
	}

	existing := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(existing, "root", "pw", true)
	seedNewInstallApps(existing)
	if got := AuthGetDefaultApps(existing); len(got) != 0 {
		t.Errorf("an existing install's defaults changed: %v", got)
	}
}
