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

// TestPrivilegeToolRowsPolicies pins the three tiers the card draws. Policy
// answers "what happens on an unattended run", so it tracks the gate: what stops
// is a credential configured to ask, and it stops unless pre-authorized.
func TestPrivilegeToolRowsPolicies(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	bundled := []TempTool{
		{Name: "read_notes", CommandTemplate: "cat notes"},
		{Name: "post_update", CommandTemplate: "curl x", Credential: "loud_api"},
		{Name: "ship_it", CommandTemplate: "curl y", Credential: "loud_api"},
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
		"post_update": "ask",   // unresolvable credential → fails closed, not pre-authorized
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

// The card used to tier on temptool.NeedsConfirm, so a raw-network tool rendered
// "ask" — inviting the user to grant something the gate never withholds, since a
// tool with no credential configured to ask just runs. Policy now tells the truth
// and DETAIL still discloses the reach, which is the part worth knowing.
func TestRawNetworkToolRunsAndSaysSo(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	bundled := []TempTool{{Name: "post_update", CommandTemplate: "curl x", RawNetwork: true}}
	rec := AgentRecord{ID: "a1b", Name: "Poster", AllowedTools: []string{"post_update"}}
	rows := privilegeToolRows(sess, rec, bundled)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if rows[0].Policy != "auto" {
		t.Errorf("policy = %q, want %q — nothing is configured to hold this back", rows[0].Policy, "auto")
	}
	if rows[0].Detail != "raw network" {
		t.Errorf("detail = %q — the card must still disclose the reach", rows[0].Detail)
	}
}

// TestPrivilegeToolRowsSubAgent — a sub-agent carries its parent's authority,
// so the card must not draw per-tool controls that would never fire.
func TestPrivilegeToolRowsSubAgent(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	bundled := []TempTool{{Name: "post_update", CommandTemplate: "curl x", Credential: "loud_api"}}
	rec := AgentRecord{ID: "a2", Name: "Helper", OwnedBy: "parent-1", AllowedTools: []string{"post_update"}}
	rows := privilegeToolRows(sess, rec, bundled)
	if len(rows) != 1 || rows[0].Policy != "auto" {
		t.Fatalf("sub-agent rows = %+v, want one auto row", rows)
	}
	// The annotation is the REASON it doesn't stop, so it belongs only where the
	// parent's authority is what's carrying it — a tool nothing was withholding
	// isn't running "via parent", it's just running.
	if want := "credential: loud_api · via parent"; rows[0].Detail != want {
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
	emitPrivilegeCard(sess, rec, []TempTool{{Name: "post_update", CommandTemplate: "curl x", Credential: "loud_api"}}, nil)
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
	emitPrivilegeCard(&ToolSession{Username: "u"}, rec, nil, nil)
}

// TestUpdateCardOnlyForWidening pins the update rule: an agent that already
// holds a consequential tool and a capability flag does not re-raise the card
// on a save that changed neither, and does the moment the save grants
// something new. Before this, every update of such an agent prompted for
// "permission changes" nobody had proposed.
func TestUpdateCardOnlyForWidening(t *testing.T) {
	shown := 0
	sess := &ToolSession{Username: "u"}
	sess.PrivilegePrompt = func(string, string, map[string]string) { shown++ }
	tool := []TempTool{{Name: "post_update", CommandTemplate: "curl x", Credential: "loud_api"}}
	rec := AgentRecord{ID: "a7", Name: "Poster", Author: true, AllowedTools: []string{"post_update"}}
	// Same powers, prompt-only edit: quiet. The snapshot is built the way the
	// update path builds it, from the rows of the stored record.
	prev := &privilegeSnapshot{tools: privilegeToolRows(sess, rec, tool), flags: privilegeFlagRows(rec)}
	edited := rec
	edited.OrchestratorPrompt = "be terse"
	emitPrivilegeCard(sess, edited, tool, prev)
	if shown != 0 {
		t.Fatalf("a save that widened nothing raised the card %d time(s)", shown)
	}

	// A flag turned on: shown.
	wider := rec
	wider.Fleet = true
	emitPrivilegeCard(sess, wider, tool, prev)
	if shown != 1 {
		t.Fatalf("a newly granted capability must raise the card, shown=%d", shown)
	}

	// A consequential tool added: shown.
	more := rec
	more.AllowedTools = []string{"post_update", "wipe_disk"}
	tools := append(tool, TempTool{Name: "wipe_disk", CommandTemplate: "rm -rf /", Credential: "loud_api"})
	emitPrivilegeCard(sess, more, tools, prev)
	if shown != 2 {
		t.Fatalf("a newly granted consequential tool must raise the card, shown=%d", shown)
	}

	// Narrowing — the flag turned off — is not a grant: quiet.
	narrower := rec
	narrower.Author = false
	emitPrivilegeCard(sess, narrower, tool, prev)
	if shown != 2 {
		t.Fatalf("a narrowing save must not raise the card, shown=%d", shown)
	}

	// A record that did not exist before (create/clone) keeps the old rule.
	emitPrivilegeCard(sess, rec, tool, nil)
	if shown != 3 {
		t.Fatalf("a created agent with powers must raise the card, shown=%d", shown)
	}
}
