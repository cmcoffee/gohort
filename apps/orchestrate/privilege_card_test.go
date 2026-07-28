package orchestrate

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func privilegeRowByName(rows []privilegeGrant, name string) (privilegeGrant, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return privilegeGrant{}, false
}

// TestPrivilegeToolRowsPolicies pins the three tiers the card draws: a
// read-only tool runs freely, a consequential one asks unless it has been
// pre-authorized for unattended runs.
func TestPrivilegeToolRowsPolicies(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	bundled := []TempTool{
		{Name: "read_notes", CommandTemplate: "cat notes"},
		{Name: "post_update", CommandTemplate: "curl x", RawNetwork: true},
		{Name: "ship_it", CommandTemplate: "curl y", RawNetwork: true},
	}
	rec := AgentRecord{
		ID:               "a1",
		Name:             "Poster",
		AllowedTools:     []string{"read_notes", "post_update", "ship_it"},
		AutoApproveTools: []string{"ship_it"},
	}
	rows := privilegeToolRows(sess, rec, bundled)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(rows), rows)
	}
	for name, want := range map[string]string{
		"read_notes":  "auto",  // benign shell tool — never stops
		"post_update": "ask",   // leaves the sandbox, not pre-authorized
		"ship_it":     "allow", // same tier, pre-authorized
	} {
		row, ok := privilegeRowByName(rows, name)
		if !ok {
			t.Fatalf("missing row for %q", name)
		}
		if row.Policy != want {
			t.Errorf("%s policy = %q, want %q (detail %q)", name, row.Policy, want, row.Detail)
		}
	}
}

// TestPrivilegeToolRowsSubAgent — a sub-agent carries its parent's authority,
// so the card must not draw per-tool controls that would never fire.
func TestPrivilegeToolRowsSubAgent(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	bundled := []TempTool{{Name: "post_update", CommandTemplate: "curl x", RawNetwork: true}}
	rec := AgentRecord{ID: "a2", Name: "Helper", OwnedBy: "parent-1", AllowedTools: []string{"post_update"}}
	rows := privilegeToolRows(sess, rec, bundled)
	if len(rows) != 1 || rows[0].Policy != "auto" {
		t.Fatalf("sub-agent rows = %+v, want one auto row", rows)
	}
	if want := "raw network · via parent"; rows[0].Detail != want {
		t.Errorf("detail = %q, want %q", rows[0].Detail, want)
	}
}

// TestPrivilegeToolRowsListsOnlyGrantedTools guards the lookup/list split: the
// user's whole tool pool is loaded to CLASSIFY names, but only tools this agent
// can actually call may appear.
func TestPrivilegeToolRowsListsOnlyGrantedTools(t *testing.T) {
	prev := ListUserAgentTools
	ListUserAgentTools = func(Database, string) []TempTool {
		return []TempTool{
			{Name: "granted", CommandTemplate: "echo hi"},
			{Name: "someone_elses", CommandTemplate: "curl z", RawNetwork: true},
		}
	}
	defer func() { ListUserAgentTools = prev }()

	sess := &ToolSession{Username: "u"}
	rec := AgentRecord{ID: "a3", Name: "Scoped", AllowedTools: []string{"granted"}}
	rows := privilegeToolRows(sess, rec, nil)
	if len(rows) != 1 || rows[0].Name != "granted" {
		t.Fatalf("rows = %+v, want only the granted tool", rows)
	}
	// Classified from the store record, not guessed.
	if rows[0].Policy != "auto" || rows[0].Detail != "read-only" {
		t.Errorf("stored tool misclassified: %+v", rows[0])
	}
}

// TestPrivilegeFlagRows — the three capability toggles always show (their
// absence is information); a sub-agent's are locked because the server refuses
// to change them.
func TestPrivilegeFlagRows(t *testing.T) {
	flags := privilegeFlagRows(AgentRecord{ID: "a4", Fleet: true})
	if len(flags) != 3 {
		t.Fatalf("flags = %d, want 3: %+v", len(flags), flags)
	}
	if !flags[0].On || flags[0].Field != "fleet" {
		t.Errorf("conductor flag = %+v", flags[0])
	}
	for _, f := range flags {
		if f.Locked {
			t.Errorf("top-level agent flag should be editable: %+v", f)
		}
	}
	// Narrow grants appear only when on.
	flags = privilegeFlagRows(AgentRecord{ID: "a5", MCPExposed: true, AllowBuilderDispatch: true})
	if len(flags) != 5 {
		t.Fatalf("flags = %d, want 5 with the two narrow grants on", len(flags))
	}
	for _, f := range privilegeFlagRows(AgentRecord{ID: "a6", OwnedBy: "p"}) {
		if !f.Locked {
			t.Errorf("sub-agent flag must be locked: %+v", f)
		}
	}
}

// TestPrivilegeCardWorthShowing keeps the card off screen when there is nothing
// to decide — an agent that got read-only tools and holds no capability.
func TestPrivilegeCardWorthShowing(t *testing.T) {
	quiet := []privilegeGrant{{Name: "a", Policy: "auto"}}
	off := []privilegeFlag{{Field: "fleet"}, {Field: "author"}, {Field: "exposed"}}
	if privilegeCardWorthShowing(quiet, off) {
		t.Error("nothing consequential and no capability on — the card should stay quiet")
	}
	if !privilegeCardWorthShowing([]privilegeGrant{{Name: "a", Policy: "ask"}}, off) {
		t.Error("a tool that will stop and ask is worth showing")
	}
	if !privilegeCardWorthShowing(quiet, []privilegeFlag{{Field: "author", On: true}}) {
		t.Error("a capability grant is worth showing")
	}
}

// TestEmitPrivilegeCardPayload checks what the renderer actually receives: the
// agent id it must POST against, and JSON it can parse.
func TestEmitPrivilegeCardPayload(t *testing.T) {
	var gotID, gotName string
	var gotData map[string]string
	sess := &ToolSession{Username: "u"}
	sess.PrivilegePrompt = func(id, name string, data map[string]string) {
		gotID, gotName, gotData = id, name, data
	}
	rec := AgentRecord{ID: "a7", Name: "Poster", Author: true, AllowedTools: []string{"post_update"}}
	emitPrivilegeCard(sess, rec, []TempTool{{Name: "post_update", CommandTemplate: "curl x", RawNetwork: true}})
	if gotID != "a7" || gotName != "Poster" {
		t.Fatalf("card target = %q/%q", gotID, gotName)
	}
	var tools []privilegeGrant
	if err := json.Unmarshal([]byte(gotData["tools"]), &tools); err != nil {
		t.Fatalf("tools payload not parseable: %v", err)
	}
	if len(tools) != 1 || tools[0].Policy != "ask" {
		t.Errorf("tools payload = %+v", tools)
	}
	var flags []privilegeFlag
	if err := json.Unmarshal([]byte(gotData["flags"]), &flags); err != nil {
		t.Fatalf("flags payload not parseable: %v", err)
	}
	if gotData["sub_agent"] != "" {
		t.Errorf("top-level agent must not be marked a sub-agent")
	}

	// No live viewer (a scheduled authoring run) — nothing to emit, no panic.
	emitPrivilegeCard(&ToolSession{Username: "u"}, rec, nil)
}
