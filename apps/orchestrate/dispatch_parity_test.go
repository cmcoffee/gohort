package orchestrate

import (
	"context"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// capturingLLM records the system prompt of every call so a test can assert on
// the prompt a path actually built rather than on the helper it was supposed
// to call.
type capturingLLM struct {
	reply  string
	system []string
}

func (c *capturingLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	// The loop delivers the system prompt as a ChatOption, not as a message.
	var cfg ChatConfig
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.SystemPrompt != "" {
		c.system = append(c.system, cfg.SystemPrompt)
	}
	for _, m := range msgs {
		if m.Role == "system" {
			c.system = append(c.system, m.Content)
		}
	}
	return &Response{Content: c.reply}, nil
}

func (c *capturingLLM) ChatStream(ctx context.Context, msgs []Message, h StreamHandler, opts ...ChatOption) (*Response, error) {
	return c.Chat(ctx, msgs, opts...)
}

// promptHeadings lists the `## ` section headings of a prompt, sorted, so two
// prompts can be compared by the blocks they carry rather than byte for byte
// (the two dispatch paths legitimately differ in wording of agent-specific
// content; what must not differ is WHICH blocks are present).
func promptHeadings(prompt string) []string {
	var out []string
	for _, ln := range strings.Split(prompt, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "## ") && !strings.HasPrefix(ln, "### ") {
			out = append(out, strings.TrimPrefix(ln, "## "))
		}
	}
	sort.Strings(out)
	return out
}

// The SAME target reached two ways must run on the same prompt blocks. The
// in-session agents(action="run") path used to hand-roll prependAgentContext +
// the custom-tool index and stop there, while RunAgentSync built the
// Available-agents / skills / topics blocks, a cortex agent's feed, and the
// per-agent capability blocks. A target reached in-session was left to discover
// its peers by calling agents(action="list"), which a model almost never does
// speculatively.
func TestDispatchPathsRenderTheSameBlocks(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	llm := &capturingLLM{reply: "done"}
	app := &OrchestrateApp{AppCore: AppCore{DB: root, LLM: llm, LeadLLM: llm}}

	caller, err := saveAgent(udb, AgentRecord{Name: "Caller", Owner: "u", DispatchMode: dispatchAll, OrchestratorPrompt: "caller persona"})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	target, err := saveAgent(udb, AgentRecord{Name: "Specialist", Owner: "u", Description: "knows the domain", OrchestratorPrompt: "specialist persona"})
	if err != nil {
		t.Fatalf("save target: %v", err)
	}
	// A third agent so the target's own fleet block has something to list.
	if _, err := saveAgent(udb, AgentRecord{Name: "Peer", Owner: "u", Description: "a peer specialist", OrchestratorPrompt: "peer persona"}); err != nil {
		t.Fatalf("save peer: %v", err)
	}

	turn := &chatTurn{app: app, user: "u", udb: udb, agent: caller, ctx: context.Background()}
	if _, err := turn.agentsRunAction(map[string]any{"agent": target.Name, "message": "what do you know"}); err != nil {
		t.Fatalf("inline dispatch: %v", err)
	}
	if len(llm.system) == 0 {
		t.Fatal("inline dispatch never reached the model with a system prompt")
	}
	inline := llm.system[0]

	// What the channel/dispatch path would build for the same target.
	subSess := &ToolSession{LLM: llm, LeadLLM: llm, Username: "u", DB: udb, AgentID: target.ID}
	_, availableBlock, customToolPrompt, _ := app.buildDispatchTurnExtras(context.Background(), target, "u", udb, subSess)
	external := dispatchSystemPrompt(target, ListMemoryFacts(udb, factsNamespace(target.ID)),
		availableBlock, customToolPrompt, "chan:1", udb, "u")

	gotInline, gotExternal := promptHeadings(inline), promptHeadings(external)
	if strings.Join(gotInline, "|") != strings.Join(gotExternal, "|") {
		t.Fatalf("dispatch paths disagree on prompt blocks:\n  inline:   %v\n  external: %v", gotInline, gotExternal)
	}
	// Pin the specific regression rather than trusting equality alone: two
	// EMPTY prompts also compare equal.
	if !strings.Contains(inline, "## Available agents") {
		t.Error("in-session dispatch lost the Available agents block")
	}
	if !strings.Contains(inline, "Peer") {
		t.Error("Available agents block did not name the target's fleet peer")
	}
}

// The persona gate reads the TARGET's tool list. The in-session path used to
// gate against the CALLER's, which strips a target's persona sections on the
// strength of an unrelated agent's allowlist.
func TestDispatchPersonaGatesOnTargetNotCaller(t *testing.T) {
	persona := "base persona\n\n## Web research\n<!-- @requires-tools: web_search -->\nsearch the web when asked.\n"

	withTool := AgentRecord{Name: "T", AllowedTools: []string{"web_search"}, OrchestratorPrompt: persona}
	if got := gatedPersonaFor(withTool, persona); !strings.Contains(got, "Web research") {
		t.Error("a target that HAS the tool must keep the section")
	}
	without := AgentRecord{Name: "T", AllowedTools: []string{"knowledge_search"}, OrchestratorPrompt: persona}
	if got := gatedPersonaFor(without, persona); strings.Contains(got, "Web research") {
		t.Error("a target that lacks the tool must lose the section")
	}
	// Empty AllowedTools means the default pool, not "no tools" — keep
	// everything, and strip the marker comments so they never reach the model.
	open := AgentRecord{Name: "T", OrchestratorPrompt: persona}
	got := gatedPersonaFor(open, persona)
	if !strings.Contains(got, "Web research") {
		t.Error("empty AllowedTools = default pool; the section must survive")
	}
	if strings.Contains(got, "@requires-tool") {
		t.Error("the marker comment must never be shipped to the model")
	}
}

// A dispatched sub-agent (OwnedBy set) still skips the fleet block: no point
// telling a leaf about peers it cannot dispatch to, and the DELEGATE FIRST
// nudge would contradict the missing tool.
func TestDispatchContextBlocksSkipFleetForOwnedSubAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	parent, err := saveAgent(udb, AgentRecord{Name: "Parent", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if _, err := saveAgent(udb, AgentRecord{Name: "Peer", Owner: "u", Description: "d", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("save peer: %v", err)
	}
	sub, err := saveAgent(udb, AgentRecord{Name: "Sub", Owner: "u", OwnedBy: parent.ID, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save sub: %v", err)
	}

	top := &chatTurn{user: "u", udb: udb, agent: parent, ctx: context.Background()}
	if !strings.Contains(top.dispatchContextBlocks(), "## Available agents") {
		t.Error("a top-level target must get the fleet block")
	}
	leaf := &chatTurn{user: "u", udb: udb, agent: sub, ctx: context.Background()}
	if strings.Contains(leaf.dispatchContextBlocks(), "## Available agents") {
		t.Error("an owned sub-agent must not get the fleet block")
	}
}

// Comparing the two dispatch paths' heading SETS cannot catch a block that both
// paths emit twice — the sets still match. This counts instead.
//
// The skills catalog was appended in two places: dispatchContextBlocks, and
// appendAgentCapabilityBlocks (which dispatchSystemPrompt calls after splicing
// the first one in). Same pure function, same arguments, so every dispatch,
// channel and scheduled prompt for a skills-enabled agent carried the whole
// catalog twice. The web turn had already been changed to rely on the assembler
// owning it — "do NOT re-append here or it doubles" — and the dispatch side
// re-appended anyway.
func TestDispatchPromptEmitsEachBlockOnce(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	if _, err := SaveSkill(udb, "u", SkillRecord{ID: "sk1", Name: "Deep Research", Description: "how to research"}); err != nil {
		t.Fatalf("save skill: %v", err)
	}
	peer, err := saveAgent(udb, AgentRecord{Name: "Peer", Owner: "u", Description: "a peer", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save peer: %v", err)
	}
	_ = peer
	target, err := saveAgent(udb, AgentRecord{
		Name: "Specialist", Owner: "u", Description: "knows things",
		OrchestratorPrompt: "persona", AllowedSkills: []string{"sk1"},
	})
	if err != nil {
		t.Fatalf("save target: %v", err)
	}

	turn := &chatTurn{user: "u", udb: udb, agent: target}
	prompt := dispatchSystemPrompt(target, nil, turn.dispatchContextBlocks(), "", "chan:1", udb, "u")

	for _, heading := range []string{"## Available skills", "## Available agents"} {
		if n := strings.Count(prompt, heading); n > 1 {
			t.Errorf("%q appears %d times in one dispatch prompt; every block must be emitted once", heading, n)
		}
	}
	// The skills block must still be PRESENT — the fix removes a duplicate, not
	// the capability. A dispatched agent that cannot see its skills hallucinates
	// that the tools behind them are unavailable.
	if !strings.Contains(prompt, "## Available skills") {
		t.Error("the dispatch prompt lost the skills block entirely")
	}
}
