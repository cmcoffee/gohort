// Deleting an agent that was the sole carrier of a tool takes that tool out
// of EVERY agent's catalog. Observed: the tool went to the orphan pool, no
// surface said anything, and the next session's model — which had used the
// tool before and still believed in it — invented a shell invocation for a
// tool that no longer existed rather than reporting it gone.
//
// Two properties hold the fix: the delete NAMES what it removed, and a
// re-homed orphan never silently overwrites a live tool of the same name.
package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestDeletingTheSoleCarrierNamesTheToolsItDarkened(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const owner = "craig@example.com"
	agent := AgentRecord{ID: "ag-wren", Name: "Wren", Owner: owner, OrchestratorPrompt: "you are Wren"}
	if _, err := saveAgent(db, agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	// One tool only this agent carries, one shared with a second agent, one
	// user-wide. Only the first should be orphaned.
	if err := AdminPersistTempTool(db, owner, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if err := AdminPersistTempTool(db, owner, TempTool{Name: "check_surf_report"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if err := AdminPersistTempTool(db, owner, TempTool{Name: "shared_helper"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	SetUserToolScopeAgents(db, owner, "get_top_stories", []string{"ag-wren"})
	SetUserToolScopeAgents(db, owner, "check_surf_report", []string{"ag-wren", "ag-other"})

	orphaned, err := deleteAgentReporting(db, "ag-wren", owner)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(orphaned) != 1 || orphaned[0] != "get_top_stories" {
		t.Fatalf("reported %v, want exactly [get_top_stories]", orphaned)
	}
	if _, ok := UserToolByName(db, owner, "get_top_stories"); ok {
		t.Error("the sole-carried tool should have left the live pool")
	}
	// The co-carried tool keeps its other agent; the shared one is untouched.
	if p, ok := UserToolByName(db, owner, "check_surf_report"); !ok {
		t.Error("a co-carried tool must survive its other carrier")
	} else if len(p.ScopeAgents) != 1 || p.ScopeAgents[0] != "ag-other" {
		t.Errorf("scope = %v, want [ag-other]", p.ScopeAgents)
	}
	if p, ok := UserToolByName(db, owner, "shared_helper"); !ok || len(p.ScopeAgents) != 0 {
		t.Error("a user-wide tool must not be orphaned by an agent delete")
	}
}

// The tool-facing delete has to carry the warning into the model's context —
// a Log line reaches nobody who could report it to the user.
func TestTheDeleteToolReportsTheOrphanedNames(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const owner = "craig@example.com"
	if _, err := saveAgent(db, AgentRecord{ID: "ag-wren", Name: "Wren", Owner: owner, OrchestratorPrompt: "you are Wren"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := AdminPersistTempTool(db, owner, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	SetUserToolScopeAgents(db, owner, "get_top_stories", []string{"ag-wren"})

	sess := &ToolSession{Username: owner, DB: db}
	out, err := deleteAgentTool{}.RunWithSession(map[string]any{"id": "ag-wren"}, sess)
	if err != nil {
		t.Fatalf("delete tool: %v", err)
	}
	if !strings.Contains(out, "get_top_stories") {
		t.Errorf("the result must name the darkened tool:\n%s", out)
	}
	if !strings.Contains(out, "NO agent") {
		t.Errorf("the result must say the tool is callable by nobody:\n%s", out)
	}
}

// Both re-home targets end in AdminPersistTempTool, which is replace-by-name.
// With a live tool of the same name present, re-homing was a silent revert to
// the pre-delete definition.
func TestRehomingOverALiveToolIsRefused(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const owner, name = "craig@example.com", "get_top_stories"

	AddOrphanedTempTools(db, owner, []OrphanedTempTool{{
		Tool:            TempTool{Name: name, Description: "news (old copy)"},
		FormerAgentName: "Wren",
		OrphanedAt:      time.Now(),
	}})
	if err := AdminPersistTempTool(db, owner, TempTool{Name: name, Description: "news (rebuilt)"}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// The persist itself resolves the conflict, so re-stage the orphan to
	// exercise the guard directly (older installs already carry both rows).
	AddOrphanedTempTools(db, owner, []OrphanedTempTool{{
		Tool: TempTool{Name: name, Description: "news (old copy)"},
	}})

	err := rehomeOrphanTool(db, owner, name, "global")
	if err == nil {
		t.Fatal("re-home succeeded and clobbered the live tool")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the refusal must name the conflict, got: %v", err)
	}
	live, ok := UserToolByName(db, owner, name)
	if !ok || live.Tool.Description != "news (rebuilt)" {
		t.Errorf("the live definition changed: %+v", live.Tool)
	}
}
