package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/prompts"
)

// tierTextStore is a prompt-override store that round-trips through JSON, as
// the real one does.
type tierTextStore map[string][]byte

func (m tierTextStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table+"/"+key]
	return ok && json.Unmarshal(b, out) == nil
}
func (m tierTextStore) Set(table, key string, v interface{}) {
	b, _ := json.Marshal(v)
	m[table+"/"+key] = b
}
func (m tierTextStore) Unset(table, key string) { delete(m, table+"/"+key) }

const (
	tierHandleKey    = "test.tier_handle"
	tierHandleShared = "[Shared rule: check before claiming.]"
	tierHandleLead   = "[Lead rule: check, briefly.]"
)

func init() {
	RegisterPromptBlock(PromptBlock{Key: tierHandleKey, Title: "tier handle test", Text: tierHandleShared})
}

// The tier's words are chosen where the call is answered, so a lead call
// that falls back to the worker carries the worker's words, not the ones the
// lead was going to get.
func TestTierTextFollowsTheTierThatAnswers(t *testing.T) {
	SetPromptOverrideDB(tierTextStore{})
	prompts.SetPromptTierOverride(prompts.TierLead, tierHandleKey, tierHandleLead, "")
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL); SetLeadInitError("", "", nil); SetPromptOverrideDB(nil) })

	worker := &FakeLLM{Turns: []FakeTurn{{Content: "from the worker", OutputTokens: 1, Repeat: true}}}
	lead := &FakeLLM{Turns: []FakeTurn{{Content: "from the lead", OutputTokens: 1}, {Err: errors.New("lead is down")}}}
	SetSharedLLMs(worker, lead)
	app := &AppCore{LLM: ReloadableWorkerLLM(), LeadLLM: ReloadableLeadLLM()}
	system := WithSystemPrompt("Persona.\n" + tierHandleShared)
	msgs := []Message{{Role: "user", Content: "hi"}}

	if _, err := app.LeadChat(context.Background(), msgs, system); err != nil {
		t.Fatal(err)
	}
	if got := lead.Config(0).SystemPrompt; !strings.Contains(got, tierHandleLead) || strings.Contains(got, tierHandleShared) {
		t.Fatalf("the lead was sent %q", got)
	}

	resp, err := app.LeadChat(context.Background(), msgs, system)
	if err != nil || resp.Content != "from the worker" {
		t.Fatalf("fallback: %v %+v", err, resp)
	}
	if got := worker.Config(0).SystemPrompt; !strings.Contains(got, tierHandleShared) || strings.Contains(got, tierHandleLead) {
		t.Fatalf("the worker, answering for a failed lead, was sent %q", got)
	}

	if _, err := app.WorkerChat(context.Background(), msgs, system); err != nil {
		t.Fatal(err)
	}
	if got := worker.Config(1).SystemPrompt; strings.Contains(got, tierHandleLead) {
		t.Fatalf("a worker call got the lead's words: %q", got)
	}
}

// A shipped tool's edited description goes out in the calls the edit is for,
// per tier, and the caller's own tool list is not changed by it.
func TestToolDescriptionEditsGoOut(t *testing.T) {
	SetPromptOverrideDB(tierTextStore{})
	prompts.RegisterTunableTools(prompts.ToolGroupAuthoring, "test_handle_tool")
	prompts.SetPromptTierOverride(prompts.TierLead, prompts.ToolBlockKey("test_handle_tool"), "lead wording", "")
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL); SetPromptOverrideDB(nil) })
	worker := &FakeLLM{Turns: []FakeTurn{{Content: "w", OutputTokens: 1, Repeat: true}}}
	lead := &FakeLLM{Turns: []FakeTurn{{Content: "l", OutputTokens: 1, Repeat: true}}}
	SetSharedLLMs(worker, lead)
	app := &AppCore{LLM: ReloadableWorkerLLM(), LeadLLM: ReloadableLeadLLM()}
	tools := []Tool{{Name: "test_handle_tool", Description: "shipped wording"}, {Name: "my_own", Description: "mine"}}
	msgs := []Message{{Role: "user", Content: "hi"}}

	app.LeadChat(context.Background(), msgs, WithTools(tools))
	app.WorkerChat(context.Background(), msgs, WithTools(tools))
	lt, wt := lead.Config(0).Tools, worker.Config(0).Tools
	if lt[0].Description != "lead wording" || lt[1].Description != "mine" {
		t.Fatalf("lead tools = %+v", lt)
	}
	if wt[0].Description != "shipped wording" {
		t.Fatalf("worker tools = %+v", wt)
	}
	if tools[0].Description != "shipped wording" {
		t.Fatal("the caller's tool list was changed")
	}
}
