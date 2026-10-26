package orchestrate

// Folding agents into a family: Builder's update_agent owned_by is the same
// move the agent menu makes, a moved agent keeps the memory it had, and the
// family's build notes live on the top-level agent and reach whoever changes a
// sub-agent next.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A top-level agent that remembered goes on remembering once it is a
// sub-agent; one that had memory off stays a fresh-every-run specialist.
func TestMoveUnderKeepsTheMemoryAnAgentHad(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "rem", Name: "Rememberer"})
	moveTestAgent(t, udb, AgentRecord{ID: "fresh", Name: "Fresh", DisableExplicit: true, DisableInferred: true})

	w := moveAgentReq(t, app, udb, "rem", `{"owned_by":"p1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, "rem")
	if !got.KeepsMemory || got.DisableExplicit || got.DisableInferred {
		t.Fatalf("moved agent lost its memory: keeps=%v explicit_off=%v inferred_off=%v", got.KeepsMemory, got.DisableExplicit, got.DisableInferred)
	}
	if !strings.Contains(w.Body.String(), "keeps its own memory") {
		t.Errorf("the move did not say the memory was kept: %s", w.Body.String())
	}

	if w := moveAgentReq(t, app, udb, "fresh", `{"owned_by":"p1"}`); w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	got, _ = loadAgent(udb, "fresh")
	if got.KeepsMemory || !got.DisableExplicit || !got.DisableInferred {
		t.Fatalf("an agent with memory off gained memory by moving: keeps=%v explicit_off=%v inferred_off=%v", got.KeepsMemory, got.DisableExplicit, got.DisableInferred)
	}
}

// The menu's Move lists only places the agent can go, and says why when there
// are none, rather than offering choices that fail on the click.
func TestMoveChoicesAreCheckedBeforeTheyAreOffered(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "kid", Name: "Kid", OwnedBy: "p1"})
	moveTestAgent(t, udb, AgentRecord{ID: "solo", Name: "Solo"})
	moveTestAgent(t, udb, AgentRecord{ID: "shared", Name: "Shared", AllowedUsers: []string{"bob"}})

	choices := func(id string) (opts []moveChoice, why string) {
		w := httptest.NewRecorder()
		routed(app)(w, as(t, httptest.NewRequest(http.MethodGet, "/api/agents/"+id+"/move", nil), "alice"))
		var d struct {
			Options []moveChoice `json:"options"`
			Why     string       `json:"why"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("%s: %v: %s", id, err, w.Body.String())
		}
		return d.Options, d.Why
	}
	values := func(opts []moveChoice) []string {
		var out []string
		for _, o := range opts {
			out = append(out, o.Value)
		}
		return out
	}

	// Solo may go under Planner or Shared (both top-level), never under Kid
	// or itself.
	opts, _ := choices("solo")
	if got := strings.Join(values(opts), ","); got != "p1,shared" {
		t.Errorf("solo's places = %q, want p1,shared", got)
	}
	// A sub-agent may go to the top level or under another parent, not back
	// under the one it has.
	opts, _ = choices("kid")
	if got := strings.Join(values(opts), ","); got != ",shared,solo" {
		t.Errorf("kid's places = %q, want top level, shared, solo", got)
	}
	// Planner owns Kid; Shared is shared: neither can go anywhere, and each
	// says why.
	for id, want := range map[string]string{"p1": "sub-agent", "shared": "Share tab", "seed-builder": "built-in"} {
		opts, why := choices(id)
		if len(opts) != 0 || !strings.Contains(why, want) {
			t.Errorf("%s: options=%v why=%q, want none and a reason mentioning %q", id, values(opts), why, want)
		}
	}
}

func consolidateSession(udb Database) *ToolSession {
	// Builder asked directly, the one session that may change any agent of
	// the user's (agentFamilyRefusal).
	return &ToolSession{Username: "alice", DB: udb, AgentID: "seed-builder"}
}

// update_agent owned_by is the menu's move: the same checks refuse, before
// anything is written, and a move that lands stores what the menu's would.
func TestUpdateAgentOwnedByIsTheMenusMove(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	sess := consolidateSession(udb)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "shared", Name: "Shared", AllowedUsers: []string{"bob"}})
	moveTestAgent(t, udb, AgentRecord{ID: "kid", Name: "Kid", OwnedBy: "p1"})
	moveTestAgent(t, udb, AgentRecord{ID: "p2", Name: "Second"})

	// Refused, and nothing else in the call lands either.
	_, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "shared", "owned_by": "p1", "description": "changed"}, sess)
	if err == nil || !strings.Contains(err.Error(), "Share tab") {
		t.Fatalf("moving a shared agent: err=%v, want the Share tab refusal", err)
	}
	if got, _ := loadAgent(udb, "shared"); got.OwnedBy != "" || got.Description == "changed" {
		t.Fatalf("a refused move wrote: owned_by=%q description=%q", got.OwnedBy, got.Description)
	}
	// Under a sub-agent: the menu refuses it, so does this.
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "p2", "owned_by": "kid"}, sess); err == nil {
		t.Fatal("update_agent moved an agent under a sub-agent")
	}

	// Two identical agents, one moved by the menu, one by update_agent.
	moveTestAgent(t, udb, AgentRecord{ID: "by-menu", Name: "Twin", ShowOnDashboard: true})
	moveTestAgent(t, udb, AgentRecord{ID: "by-tool", Name: "Twin", ShowOnDashboard: true})
	if w := moveAgentReq(t, app, udb, "by-menu", `{"owned_by":"p1"}`); w.Code != http.StatusOK {
		t.Fatalf("menu move: %d %s", w.Code, w.Body.String())
	}
	out, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "by-tool", "owned_by": "p1"}, sess)
	if err != nil {
		t.Fatalf("update_agent move: %v", err)
	}
	if !strings.Contains(out, "AGENT_UPDATED") || !strings.Contains(out, "owned_by") || !strings.Contains(out, "Moved: ") {
		t.Errorf("update_agent did not report the move: %s", out)
	}
	menu, _ := loadAgent(udb, "by-menu")
	tool, _ := loadAgent(udb, "by-tool")
	type place struct {
		OwnedBy                                       string
		Hidden, Card, Keeps, ExplicitOff, InferredOff bool
	}
	at := func(a AgentRecord) place {
		return place{a.OwnedBy, a.Hidden, a.ShowOnDashboard, a.KeepsMemory, a.DisableExplicit, a.DisableInferred}
	}
	if m, o := at(menu), at(tool); m != o {
		t.Fatalf("the two moves stored different agents:\n menu %+v\n tool %+v", m, o)
	}

	// And back out with "".
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "by-tool", "owned_by": ""}, sess); err != nil {
		t.Fatalf("update_agent move out: %v", err)
	}
	if got, _ := loadAgent(udb, "by-tool"); got.OwnedBy != "" || got.Hidden {
		t.Fatalf("after moving out: owned_by=%q hidden=%v", got.OwnedBy, got.Hidden)
	}
}

// keeps_memory passed in the same call is the author's decision, not the
// move's.
func TestUpdateAgentMoveHonorsAnExplicitKeepsMemory(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "c1", Name: "Scout"})
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "c1", "owned_by": "p1", "keeps_memory": false}, consolidateSession(udb)); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(udb, "c1"); got.KeepsMemory {
		t.Fatal("keeps_memory=false in the same call was overridden by the move")
	}
}

func TestBuildNotesLiveOnTheTopLevelAgent(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	sess := consolidateSession(udb)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "kid", Name: "Kid", OwnedBy: "p1"})

	const notes = "No sub-agent gets send_message: replies go out through Planner's channel."
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "p1", "build_notes": notes}, sess); err != nil {
		t.Fatalf("notes on the top-level agent: %v", err)
	}
	if got, _ := loadAgent(udb, "p1"); got.BuildNotes != notes {
		t.Fatalf("notes not saved: %q", got.BuildNotes)
	}
	// On a sub-agent: refused, naming where they go.
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "kid", "build_notes": "mine"}, sess); err == nil || !strings.Contains(err.Error(), "Planner") {
		t.Fatalf("notes on a sub-agent: err=%v, want a refusal naming Planner", err)
	}
	// Over the cap: refused.
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "p1", "build_notes": strings.Repeat("x", agentBuildNotesCap+1)}, sess); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("notes over the cap: err=%v", err)
	}

	// A new sub-agent's create result carries them, and so does get.
	out, err := (createAgentTool{}).RunWithSession(map[string]any{
		"name": "Writer", "orchestrator_prompt": "writes", "allowed_tools": []any{"web_search"}, "owned_by": "p1",
	}, sess)
	if err != nil {
		t.Fatalf("create sub-agent: %v", err)
	}
	if !strings.Contains(out, "BUILD NOTES") || !strings.Contains(out, notes) {
		t.Errorf("a new sub-agent's result did not carry the family's notes: %s", out)
	}
	if strings.Contains(out, "Do NOT call ask_user, create_agent, or any other tool") {
		t.Errorf("the result forbids the fix its notes ask for: %s", out)
	}
	kid, _ := loadAgent(udb, "kid")
	if slim := string(slimAgentJSON(udb, "alice", kid)); !strings.Contains(slim, "family_build_notes") || !strings.Contains(slim, "send_message") {
		t.Errorf("get of a sub-agent does not show the family's notes: %s", slim)
	}
	if in := recipeInput(sess, "change", "add a reviewer", "p1", ""); !strings.Contains(in, "BUILD NOTES") || !strings.Contains(in, notes) {
		t.Errorf("the recipe does not see the family's notes:\n%s", in)
	}

	// A top-level agent with no family notes gets the plain DONE line.
	out, err = (createAgentTool{}).RunWithSession(map[string]any{
		"name": "Loner", "orchestrator_prompt": "alone", "allowed_tools": []any{"web_search"},
	}, sess)
	if err != nil || strings.Contains(out, "BUILD NOTES") {
		t.Errorf("a top-level create carried notes: err=%v out=%s", err, out)
	}
}

// A move under a parent says what becomes of notes the agent carried.
func TestMoveSaysTheMovedAgentsNotesGoUnread(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "c1", Name: "Scout", BuildNotes: "scout decisions"})
	w := moveAgentReq(t, app, udb, "c1", `{"owned_by":"p1"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "build notes") {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
}
