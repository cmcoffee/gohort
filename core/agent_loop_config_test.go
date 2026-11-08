// TurnNotes: volatile, turn-scoped context appended to the newest user message.
//
// The seam exists because a tool SCHEMA cannot carry anything that changes
// between turns — schemas sit at the front of the prompt, so a changing one
// re-pays cold prefill every turn. The newest user turn has the opposite
// property: it never hits cache anyway, which is why the date stamp lives there.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestATurnNoteLandsOnTheNewestUserMessage(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "older question"},
		{Role: "assistant", Content: "older answer"},
		{Role: "user", Content: "add Alex to the garage picture"},
	}
	cfg := AgentLoopConfig{TurnNotes: func(user string) string {
		// The app is handed the user's OWN words — not a message that opens
		// with a timestamp it has to look past to understand the request.
		if strings.Contains(user, "[Current date & time:") {
			t.Errorf("TurnNotes saw a stamped message: %q", user)
		}
		return "recent images: image#1 — the garage"
	}}

	applyTurnNotes(cfg, history)

	if !strings.Contains(history[2].Content, "image#1 — the garage") {
		t.Errorf("note missing from the newest user turn: %q", history[2].Content)
	}
	if !strings.Contains(history[2].Content, "add Alex to the garage picture") {
		t.Errorf("the user's own words must survive: %q", history[2].Content)
	}
	// Earlier turns are settled context and part of the cached prefix. Writing
	// into one moves the cache boundary backwards for no gain.
	if strings.Contains(history[0].Content, "image#1") {
		t.Error("an earlier user turn must not be touched")
	}
}

func TestATurnNoteIsSkippedWhenThereIsNothingToSay(t *testing.T) {
	history := []Message{{Role: "user", Content: "what's for dinner"}}
	applyTurnNotes(AgentLoopConfig{TurnNotes: func(string) string { return "   " }}, history)
	if history[0].Content != "what's for dinner" {
		t.Errorf("a blank note must add nothing, got %q", history[0].Content)
	}
	// No hook at all is the shape every host had before this existed.
	applyTurnNotes(AgentLoopConfig{}, history)
	if history[0].Content != "what's for dinner" {
		t.Errorf("no hook must add nothing, got %q", history[0].Content)
	}
}

func TestATurnNoteNeverLandsOnAToolResult(t *testing.T) {
	// Mid-loop the trailing message is a tool result, not the human turn.
	// Appending there would put reference material inside a result the model
	// reads as output from something it just ran.
	history := []Message{
		{Role: "user", Content: "blend these"},
		{Role: "assistant", Content: ""},
		{Role: "tool", Content: "edit failed"},
	}
	applyTurnNotes(AgentLoopConfig{TurnNotes: func(string) string { return "NOTE" }}, history)
	for _, m := range history {
		if strings.Contains(m.Content, "NOTE") {
			t.Errorf("note landed on a %s message: %q", m.Role, m.Content)
		}
	}
	applyTurnNotes(AgentLoopConfig{TurnNotes: func(string) string { return "NOTE" }}, nil) // must not panic
}

// Every agent loop gohort builds says where its tools queue pictures for the
// model (DrainViewImages), so a picture a tool put "in front of you on your
// next step" (view_image's show_me, a render shown to its maker) is shown on
// that step. A loop without it left them queued with nobody to show them, and
// the model, told it would see them, said it could not read them. A file whose
// loops' tools have no tool session to queue on says so here.
func TestEveryAgentLoopShowsWhatItsToolsQueue(t *testing.T) {
	noSession := map[string]string{
		"core/sdk.go":                        "the bare loop for an outside caller: its tools take no tool session",
		"core/pipeline_interp.go":            "pipeline stages here are given tools without a tool session",
		"apps/hello/agent.go":                "the minimal example: its tools take no tool session",
		"apps/servitor/probe_session.go":     "Servitor's own tools, which take no tool session",
		"apps/servitor/sysprobe.go":          "Servitor's own tools, which take no tool session",
		"apps/servitor/repo_audit.go":        "Servitor's own tools, which take no tool session",
		"apps/servitor/workspace_session.go": "Servitor's own tools, which take no tool session",
		"apps/servitor/map_session.go":       "Servitor's own tools, which take no tool session",
	}
	root := filepath.Join("..")
	var missing []string
	for _, dir := range []string{"core", "apps", "tools"} {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			src := string(b)
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if !strings.Contains(src, "AgentLoopConfig{") || rel == "core/agent_loop.go" || rel == "core/agent_loop_config.go" {
				return nil
			}
			if strings.Count(src, "AgentLoopConfig{") > strings.Count(src, "DrainViewImages") && noSession[rel] == "" {
				missing = append(missing, rel)
			}
			return nil
		})
	}
	if len(missing) > 0 {
		t.Errorf("agent loops that do not show the model what their tools queue (set DrainViewImages to the tools' session, or say here why they have none): %v", missing)
	}
	for f := range noSession {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s is listed as having no tool session but is gone: take it off the list", f)
		}
	}
}

// recordingTrace is a ToolTrace that writes down what it hears.
type recordingTrace struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingTrace) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, s)
}

func (r *recordingTrace) RoundStarted(round int, said string) {
	r.add(fmt.Sprintf("round %d said %q", round, said))
}

func (r *recordingTrace) ToolStarted(name string, args map[string]any) func(string, error) {
	r.add("start " + name + " " + fmt.Sprint(args["n"]))
	return func(out string, err error) {
		if err != nil {
			r.add("end " + name + " err " + err.Error())
			return
		}
		r.add("end " + name + " " + out)
	}
}

// An agent loop running under a trace tells it each round that calls tools,
// with what the model said, and each call from start to end: alone or beside
// others, a failing call with its error, a panicking one with the error the
// panic became. A loop with no trace on its context runs as it always did.
func TestATraceHearsEachCallAsItHappens(t *testing.T) {
	probe := AgentToolDef{
		Tool: Tool{Name: "probe", Description: "d", Parameters: map[string]ToolParam{"n": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			switch args["n"] {
			case "bad":
				return "", errors.New("no such thing")
			case "boom":
				panic("kaboom")
			}
			return "ok " + args["n"].(string), nil
		},
	}
	turns := func() *AppCore {
		return &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
			{Content: "Looking it up.", ToolCalls: []ToolCall{{ID: "c1", Name: "probe", Args: map[string]any{"n": "alone"}}}},
			{ToolCalls: []ToolCall{
				{ID: "c2", Name: "probe", Args: map[string]any{"n": "bad"}},
				{ID: "c3", Name: "probe", Args: map[string]any{"n": "boom"}}}},
			{Content: "done", Repeat: true},
		}}}
	}
	tr := &recordingTrace{}
	ctx := WithToolTrace(context.Background(), tr)
	if _, _, err := turns().RunAgentLoop(ctx, []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: []AgentToolDef{probe}, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	got := tr.lines
	if len(got) < 3 || got[0] != `round 1 said "Looking it up."` || got[1] != "start probe alone" || got[2] != "end probe ok alone" {
		t.Fatalf("the first round, in order: %q", got)
	}
	rest := append([]string(nil), got[3:]...)
	sort.Strings(rest)
	want := []string{
		"end probe err no such thing",
		"end probe err tool panicked: kaboom",
		`round 2 said ""`,
		"start probe bad",
		"start probe boom",
	}
	if strings.Join(rest, "|") != strings.Join(want, "|") {
		t.Errorf("the parallel round:\n got %q\nwant %q", rest, want)
	}

	// A run that starts its own loop puts its own trace on, and the one
	// beneath hears nothing of it.
	inner := &recordingTrace{}
	if _, _, err := turns().RunAgentLoop(WithToolTrace(ctx, inner), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: []AgentToolDef{probe}, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(inner.lines) == 0 || len(tr.lines) != len(got) {
		t.Errorf("the inner trace heard %d, the outer %d more", len(inner.lines), len(tr.lines)-len(got))
	}

	if _, _, err := turns().RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: []AgentToolDef{probe}, MaxRounds: 4}); err != nil {
		t.Fatalf("a loop with no trace: %v", err)
	}
}
