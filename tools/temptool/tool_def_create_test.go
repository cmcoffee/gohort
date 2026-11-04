// What a toolbox action is required to be given when its author didn't say.
//
// The old answer was "all of it", and it was the largest source of tool errors
// in production: 371 in two days on one toolbox, ~360 of them a model bounced
// for omitting "cursor", "limit" or "sort" on a feed read. A cursor is the
// handle for the NEXT page — on a first call it cannot exist — so the action
// was uncallable by construction and the model had no way to find that out
// except by failing. It failed 260 times on one action.
package temptool

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func params(names ...string) map[string]ToolParam {
	m := make(map[string]ToolParam, len(names))
	for _, n := range names {
		m[n] = ToolParam{Type: "string"}
	}
	return m
}

func TestOnlyWhatTheURLCannotBeBuiltWithoutIsRequired(t *testing.T) {
	// The failing shape. Nothing in the path, so nothing is mandatory: a feed
	// read with no arguments at all is the FIRST page, which is exactly what a
	// caller means when it says nothing.
	if got := defaultRequiredParams("https://api.example.com/feed", params("cursor", "limit", "sort")); len(got) != 0 {
		t.Errorf("pagination params must not be required, got %v", got)
	}

	// A path placeholder is required whatever the endpoint thinks — without it
	// there is no URL to request.
	got := defaultRequiredParams("https://api.example.com/posts/{post_id}/comments", params("post_id", "cursor", "limit"))
	if !slices.Equal(got, []string{"post_id"}) {
		t.Errorf("required = %v, want just the path placeholder", got)
	}

	// A placeholder that is not a declared param was never the caller's to
	// supply — it is spliced in from somewhere else.
	if got := defaultRequiredParams("https://api.example.com/{workspace}/feed", params("cursor")); len(got) != 0 {
		t.Errorf("an undeclared placeholder is not the caller's problem, got %v", got)
	}

	// Query-string placeholders are not path placeholders: the request still
	// resolves without them.
	if got := defaultRequiredParams("https://api.example.com/feed?sort={sort}", params("sort")); len(got) != 0 {
		t.Errorf("a query placeholder must not be required, got %v", got)
	}
}

func TestAStoredToolboxRepairsItselfOnRegistration(t *testing.T) {
	// Authoring-time defaults only help tools authored from now on. The ones
	// doing the damage are already saved.
	act := TempToolAction{
		Name:        "get_feed",
		URLTemplate: "https://api.example.com/feed",
		Params:      params("cursor", "limit", "sort"),
		Required:    []string{"cursor", "limit", "sort"}, // what the old default wrote
	}
	if got := liveRequired(act); len(got) != 0 {
		t.Errorf("a saved all-required feed read must be narrowed, got %v", got)
	}
}

func TestARepairNeverOverridesAnAuthorWhoSaidWhatTheyMeant(t *testing.T) {
	base := params("cursor", "limit", "sort")

	// Partial list — deliberate by construction, since the old default could
	// never have produced it.
	act := TempToolAction{Name: "get_feed", URLTemplate: "https://x/feed", Params: base, Required: []string{"sort"}}
	if got := liveRequired(act); !slices.Equal(got, []string{"sort"}) {
		t.Errorf("a partial list is the author speaking, got %v", got)
	}

	// Nothing required stays nothing required.
	act.Required = nil
	if got := liveRequired(act); len(got) != 0 {
		t.Errorf("no requirements must stay none, got %v", got)
	}

	// Every param IS a path placeholder: all-required is correct here, and
	// narrowing it would be a no-op that still churns the definition.
	act = TempToolAction{
		Name:        "get_comment",
		URLTemplate: "https://x/posts/{post_id}/comments/{comment_id}",
		Params:      params("post_id", "comment_id"),
		Required:    []string{"post_id", "comment_id"},
	}
	if got := liveRequired(act); len(got) != 2 {
		t.Errorf("genuinely-required params must survive, got %v", got)
	}

	// Required naming something that isn't a declared param is not the old
	// default's handiwork — leave it alone rather than guess.
	act = TempToolAction{Name: "odd", URLTemplate: "https://x/feed", Params: params("a"), Required: []string{"ghost"}}
	if got := liveRequired(act); !slices.Equal(got, []string{"ghost"}) {
		t.Errorf("an unrecognized shape must be left alone, got %v", got)
	}
}

func TestAWriteStillCarriesItsPayload(t *testing.T) {
	// The trap in narrowing `required`: the body scaffold used to be built FROM
	// required, so shrinking it to path ids would post a body holding the ids
	// and leave the content behind — a write that silently sends nothing.
	got := writeBodyParams("https://x/posts/{post_id}/reply", params("post_id", "content", "notify"))
	if !slices.Equal(got, []string{"content", "notify"}) {
		t.Errorf("body params = %v, want everything the URL doesn't already carry", got)
	}
	// Including the optional one: it drops out cleanly when omitted
	// (TestSubstituteJSONOptionalDrop), so carrying it costs nothing and NOT
	// carrying it makes it unsendable.
	tpl := scaffoldBodyTemplate(got)
	for _, want := range []string{"content", "notify"} {
		if !strings.Contains(tpl, "{"+want+"}") {
			t.Errorf("scaffolded body drops %q: %s", want, tpl)
		}
	}
	if strings.Contains(tpl, "{post_id}") {
		t.Errorf("the path id must not be duplicated into the body: %s", tpl)
	}
}

// scriptedToolbox creates a toolbox with an HTTP action and a command action
// that runs the toolbox's script, and returns the stored record.
func scriptedToolbox(t *testing.T, sess *ToolSession, extra map[string]any) (*TempTool, error) {
	t.Helper()
	args := map[string]any{
		"action": "create", "name": "attachments", "description": "Attachments of an issue tracker.", "mode": "toolbox",
		"actions": []any{
			map[string]any{"name": "list", "description": "List an issue's attachments.", "url_template": "https://tracker.example.test/issue/{issue_key}/attachments",
				"params": map[string]any{"issue_key": map[string]any{"type": "string"}}},
			map[string]any{"name": "save", "description": "Save one attachment into the workspace.",
				"command_template": "python3 {workspace_dir}/attachments.py save {issue_key} {attachment_id}",
				"params":           map[string]any{"issue_key": map[string]any{"type": "string"}, "attachment_id": map[string]any{"type": "string"}}},
		},
		"script_body":       "import sys\nprint('saved', sys.argv[2:])\n",
		"script_name":       "attachments.py",
		"hook_capabilities": []any{"fetch_via:tracker_test"},
	}
	for k, v := range extra {
		args[k] = v
	}
	if _, err := createGrouped(args, sess); err != nil {
		return nil, err
	}
	for _, tt := range sess.TempTools {
		if tt.Name == "attachments" {
			return tt, nil
		}
	}
	t.Fatal("the toolbox was not stored")
	return nil, nil
}

func toolboxSession(t *testing.T) *ToolSession {
	return &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}, CanScopeGlobal: true}
}

// A toolbox action can run a command, and the toolbox carries the one script
// its command actions run and what that script may reach. Builder could not
// fold "save the attachment to a file" into a toolbox: create demanded a
// url_template on every action, and a script set on the action was dropped.
func TestAToolboxActionCanRunItsScript(t *testing.T) {
	sess := toolboxSession(t)
	tt, err := scriptedToolbox(t, sess, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var save TempToolAction
	for _, a := range tt.Actions {
		if a.Name == "save" {
			save = a
		}
	}
	if save.CommandTemplate == "" || save.URLTemplate != "" || len(save.Required) != 2 {
		t.Errorf("the save action must be a command with both params required: %+v", save)
	}
	if tt.ScriptName != "attachments.py" || tt.CanonicalScriptName == "" || !slices.Contains(tt.HookCapabilities, "fetch_via:tracker_test") {
		t.Errorf("the toolbox must keep its script and what it may reach: name %q canonical %q caps %v", tt.ScriptName, tt.CanonicalScriptName, tt.HookCapabilities)
	}
	if _, err := os.Stat(filepath.Join(sess.WorkspaceDir, tt.CanonicalScriptName)); err != nil {
		t.Errorf("the script must be deployed into the workspace: %v", err)
	}
	// What the action runs as carries the script and its reach.
	run := toolboxShellAction(tt, &save)
	if run.ScriptBody != tt.ScriptBody || run.CanonicalScriptName != tt.CanonicalScriptName || !slices.Contains(run.HookCapabilities, "fetch_via:tracker_test") {
		t.Errorf("the command action must run with the toolbox's script and capabilities: %+v", run)
	}
}

// What an author gets wrong is refused with what to do, not dropped.
func TestAToolboxScriptGoesWhereItWorks(t *testing.T) {
	cases := map[string]struct {
		extra map[string]any
		want  string
	}{
		"a script inside an action": {map[string]any{"actions": []any{map[string]any{"name": "save", "description": "d",
			"command_template": "python3 {workspace_dir}/x.py", "script_body": "print(1)"}}}, "belongs on the toolbox"},
		"both templates on an action": {map[string]any{"actions": []any{map[string]any{"name": "save", "description": "d",
			"url_template": "https://tracker.example.test/x", "command_template": "echo hi"}}}, "not both"},
		"a script with no command action": {map[string]any{"actions": []any{map[string]any{"name": "list", "description": "d",
			"url_template": "https://tracker.example.test/x"}}}, "this toolbox has none"},
		"an HTTP field on a command action": {map[string]any{"actions": []any{map[string]any{"name": "save", "description": "d",
			"command_template": "python3 {workspace_dir}/attachments.py", "method": "POST"}}}, "a command action has none"},
	}
	for name, c := range cases {
		_, err := scriptedToolbox(t, toolboxSession(t), c.extra)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want an error saying %q, got %v", name, c.want, err)
		}
	}
}

// An update turns an HTTP action into a command one cleanly, with the script
// set on the toolbox: the merge kept the old url_template beside the new
// command, and update told the author to put script_body inside the action.
func TestAnUpdateTurnsAnHTTPActionIntoACommand(t *testing.T) {
	sess := toolboxSession(t)
	if _, err := createGrouped(map[string]any{
		"action": "create", "name": "attachments", "description": "Attachments.", "mode": "toolbox",
		"actions": []any{map[string]any{"name": "download", "description": "Download one.", "url_template": "https://tracker.example.test/a/{attachment_id}",
			"params": map[string]any{"attachment_id": map[string]any{"type": "string"}}}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := updateGrouped(map[string]any{
		"name":        "attachments",
		"script_body": "import sys\nprint(sys.argv)\n", "script_name": "attachments.py",
		"actions": []any{map[string]any{"name": "download", "command_template": "python3 {workspace_dir}/attachments.py {attachment_id}"}},
	}, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	for _, tt := range sess.TempTools {
		if tt.Name != "attachments" {
			continue
		}
		a := tt.Actions[0]
		if a.CommandTemplate == "" || a.URLTemplate != "" || a.Method != "" || tt.ScriptName != "attachments.py" {
			t.Errorf("the action must now be only a command, with the toolbox's script: action %+v script %q", a, tt.ScriptName)
		}
		return
	}
	t.Fatal("the toolbox is gone after the update")
}
