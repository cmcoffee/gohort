package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/ui"
)

// The whole-agent dialog, run: its real script under node, over a stand-in
// DOM and a stand-in conversation pane. A reply with changes lists them to
// tick; a reply that only answers leaves them standing; Apply sends the
// ticked ones, triggers as a list, and reloads. Skips where node is not
// installed.
func TestTheAgentDialogListsAndAppliesChanges(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	s := agentAssistHTML("a1")
	start := strings.Index(s, "<script>")
	end := strings.Index(s, "</script>")
	if start < 0 || end < 0 {
		t.Fatal("the dialog's script is gone")
	}
	harness := `
function node(tag) {
  var n = {tag: tag, kids: [], attrs: {}, style: {}, dataset: {}, textContent: '', className: '',
    set innerHTML(v) { this.kids = []; this.html = v; },
    appendChild: function(c) { this.kids.push(c); return c; },
    insertBefore: function(c) { this.kids.unshift(c); return c; },
    setAttribute: function(k, v) { this.attrs[k] = v; },
    querySelectorAll: function(sel) {
      var out = [];
      (function walk(x) { x.kids.forEach(function(k) {
        if (k.tag === 'input' && k.attrs.type === 'checkbox') out.push(k);
        walk(k);
      }); })(this);
      return out;
    }};
  return n;
}
var slot = node('span');
var document = {createElement: node, getElementById: function() { return null; },
  querySelector: function(sel) { return sel === '.ui-section .ui-section-h-r' ? slot : null; }};
function requestAnimationFrame() {}
var modal = null, chatOpts = null, sent = null, reloaded = false;
var location = {reload: function() { reloaded = true; }};
function fetch(url, o) { sent = {url: url, method: o.method, body: JSON.parse(o.body)}; return Promise.resolve({ok: true}); }
var window = {
  uiOpenModal: function(o) { modal = o; },
  uiAssistChat: function(o) { chatOpts = o; return {right: node('div'), focus: function() {}}; },
  uiAssistPost: function(url) { return function() {}; },
  uiToast: function() {}, uiAlert: function() {},
  editorDiffRender: function(a, b) { return 'DIFF[' + a + '|' + b + ']'; },
};
` + s[start+len("<script>"):end] + `
var fail = 0;
function check(label, cond) { if (!cond) { fail++; console.log('FAIL ' + label); } }
var b = slot.kids[0];
check('the button is in the header, labelled', b && b.textContent === '✨ Get assistance');
b.onclick();
var body = node('div');
modal.mount(body, {close: function() {}});
var changes = body.kids[1], apply = body.kids[2].kids[0];
check('nothing proposed yet: no Apply', apply.style.display === 'none');
check('an answer with no changes says nothing of its own', chatOpts.onResult({reply: 'It has no rules.'}) === '');
var said = chatOpts.onResult({reply: '', changes: [
  {field: 'rules', label: 'Rules', why: 'stops guessing', value: 'Cite every claim.', current: 'Be helpful.'},
  {field: 'triggers', label: 'Triggers', value: 'look up\n find out \n'}]});
check('proposed changes are listed to tick', changes.querySelectorAll().length === 2 && apply.style.display === '');
check('each shows as a diff of the field as it is now against the new value',
  changes.kids[0].kids[2].html === 'DIFF[Be helpful.|Cite every claim.]' && changes.kids[1].kids[1].html === 'DIFF[|look up\n find out \n]');
check('an empty reply with changes still says something', said !== '');
chatOpts.onResult({reply: 'Because it guesses.'});
check('a reply that only answers leaves the changes standing', changes.querySelectorAll().length === 2);
changes.querySelectorAll()[0].checked = false;
apply.onclick();
check('Apply sends the ticked ones as a PATCH', sent && sent.method === 'PATCH' && sent.url === '../api/agents/a1' &&
  !('rules' in sent.body) && JSON.stringify(sent.body.triggers) === '["look up","find out"]');
setTimeout(function() {
  check('and reloads to show what was stored', reloaded);
  if (fail) process.exit(1);
  console.log('ok');
}, 0);
`
	f := filepath.Join(t.TempDir(), "agent_dialog.js")
	os.WriteFile(f, []byte(harness), 0o644)
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// What the agent editor says about access other apps have granted.
//
// This lands in the "Capability" section's HELP rather than as its own
// field, because a header field starts a new page section — a row here would
// have cut that section in two and stranded the toggles below it.

func withGrantor(t *testing.T, grants ...AgentGrant) {
	t.Helper()
	RegisterAgentGrantor(AgentGrantor{
		Name: "servitor", Label: "Machines", ManageURL: "/servitor/manage",
		Granted: func(_, _ string) []AgentGrant { return grants },
	})
}

func TestGrantHelpNamesWhatIsHeldAndWhereToChangeIt(t *testing.T) {
	withGrantor(t, AgentGrant{Label: "Lab Box", Detail: "nothing without asking"})
	help := appGrantHelp("craig", "wren")

	for _, want := range []string{
		"GRANTED BY OTHER APPS",  // the heading someone scans for
		"Machines: Lab Box",      // what it holds
		"nothing without asking", // how much
		"/servitor/manage",       // where to change it
		"read-only here",         // why there are no controls
	} {
		if !strings.Contains(help, want) {
			t.Errorf("capability help should carry %q, got:\n%s", want, help)
		}
	}
}

// An app granting nothing still reports, so the capability is discoverable
// before it is needed rather than after.
func TestGrantHelpShowsAppsGrantingNothing(t *testing.T) {
	withGrantor(t)
	help := appGrantHelp("craig", "wren")
	if !strings.Contains(help, "Machines: none") {
		t.Errorf("an app granting nothing should still appear, got:\n%s", help)
	}
}

// An unsaved agent has no id, so it cannot hold a grant — and the section must
// not sprout an empty heading on the create form.
func TestGrantHelpIsEmptyForAnUnsavedAgent(t *testing.T) {
	withGrantor(t, AgentGrant{Label: "Lab Box"})
	if got := appGrantHelp("craig", ""); got != "" {
		t.Errorf("a new agent should get no grant text, got %q", got)
	}
}

// The text is appended to existing help, so it must start with a break rather
// than running into the sentence before it.
func TestGrantHelpAppendsCleanly(t *testing.T) {
	withGrantor(t, AgentGrant{Label: "Lab Box"})
	help := appGrantHelp("craig", "wren")
	if !strings.HasPrefix(help, "\n\n") {
		t.Errorf("appended help must separate itself from the copy above, got %q", help[:20])
	}
	if strings.HasSuffix(help, "\n") {
		t.Error("trailing newlines would show as a gap in the panel")
	}
}

// PATCH exists so ONE record can be edited from several forms. A FormPanel
// POSTs the fields IT holds as the whole record, so splitting a long editor
// across page-level sections used to mean each save wiped every field it
// didn't carry. The allowlist is what keeps that safe.
func TestPatchAllowlistCoversTheEditorsFields(t *testing.T) {
	// Every field the agent editor actually renders must be patchable, or
	// splitting the form would silently drop that control's saves.
	for _, f := range []string{
		"name", "description", "orchestrator_prompt", "plan_guidance", "rules",
		"triggers", "allowed_tools", "max_plan_steps", "max_worker_rounds",
		"think", "think_budget", "gap_check", "work_plan", "lead_model", "memory_mode",
		"context_depth", "channel", "fleet", "exposed", "hidden",
		"dispatch_mode", "intake_form",
	} {
		if !patchAgentFields[f] {
			t.Errorf("editor field %q is not patchable — its section's saves would be lost", f)
		}
	}
}

// The protected fields must be absent BY NAME. PATCH merges onto the stored
// record, so anything it accepts is something it can overwrite — and these
// each have their own owner-only endpoint precisely so an ordinary edit path
// cannot weaken them.
func TestPatchAllowlistExcludesProtectedFields(t *testing.T) {
	for _, f := range []string{
		"guardrails", "guardrail_hooks", "guardrail_fail_closed", "guardrail_declines",
		"locked", "id", "owner", "created",
	} {
		if patchAgentFields[f] {
			t.Errorf("%q is patchable — a partial save could reach a field that has its own protected endpoint", f)
		}
	}
}

// A guardrail is the one thing an agent must not be able to talk its way out
// of, so this is worth asserting on its own rather than trusting the list
// above to stay correct.
func TestPatchCannotTouchGuardrails(t *testing.T) {
	for k := range patchAgentFields {
		if len(k) >= 9 && k[:9] == "guardrail" {
			t.Fatalf("PATCH accepts %q — guardrails must only be settable through their owner-only endpoint", k)
		}
	}
}

// The split turns one long form into page-level sections so the page's own
// left rail navigates them, instead of eight accordions inside one section.
func TestSplitAgentFormSections(t *testing.T) {
	fields := []ui.FormField{
		{Field: "name", Type: "text"},
		{Field: "description", Type: "text"},
		{Type: "header", Label: "Budgets", Help: "how much it may spend"},
		{Field: "max_plan_steps", Type: "number"},
		{Type: "header", Label: "Memory"},
		{Field: "memory_mode", Type: "select"},
	}
	got := splitAgentFormSections("a1", "../api/agents/a1", fields, "identity")
	if len(got) != 2 {
		t.Fatalf("want 2 sections (Agent + Memory), got %d", len(got))
	}
	if got[0].Title != "Agent" || got[1].Title != "Memory" {
		t.Errorf("section titles = %q, %q", got[0].Title, got[1].Title)
	}
	// Identity fields lead the FIRST group rather than getting a rail entry of
	// their own — burying name/description behind a click hides the one thing
	// you always want visible.
	first := got[0].Body.(ui.FormPanel)
	if len(first.Fields) != 3 {
		t.Errorf("first section should hold name+description+Budgets fields, got %d", len(first.Fields))
	}
	// Every panel must PATCH. A POST would send only that section's fields as
	// the whole record and blank everything else.
	for i, sec := range got {
		p := sec.Body.(ui.FormPanel)
		if p.Method != "PATCH" {
			t.Errorf("section %d saves with %q — POST would wipe the fields it doesn't show", i, p.Method)
		}
		// The id must ride in the URL: a FormPanel's PATCH body is just the
		// changed field and names no record.
		if !strings.Contains(p.PostURL, "id=a1") {
			t.Errorf("section %d PostURL %q does not name the record", i, p.PostURL)
		}
	}
}

// No headers means nothing to split on — the caller keeps its single form.
func TestSplitAgentFormSectionsNoHeaders(t *testing.T) {
	got := splitAgentFormSections("a1", "s", []ui.FormField{{Field: "name"}}, "x")
	if got != nil {
		t.Errorf("a headerless form should not split, got %d sections", len(got))
	}
}

// The lead-model toggle chooses WHICH MODEL does the reasoning, so it belongs
// in Reasoning. It previously sat under "Autonomous runs" purely by position —
// a header owns the fields until the next header, and this one was appended to
// the end of the form — which read as an unattended-run option when it governs
// every turn.
func TestLeadModelFieldSitsInReasoning(t *testing.T) {
	fields := []ui.FormField{
		{Type: "header", Label: "Reasoning"},
		{Field: "think", Type: "select"},
		leadUseField(true, AgentRecord{}),
		{Type: "header", Label: "Autonomous runs"},
		{Field: "auto_approve_tools", Type: "tags"},
	}
	got := splitAgentFormSections("a1", "s", fields, "x")
	if len(got) != 2 {
		t.Fatalf("want Reasoning + Autonomous runs, got %d", len(got))
	}
	reasoning := got[0].Body.(ui.FormPanel)
	found := false
	for _, f := range reasoning.Fields {
		if f.Field == "lead_use" {
			found = true
		}
	}
	if !found {
		t.Error("lead_use is not in the Reasoning group")
	}
	autonomous := got[1].Body.(ui.FormPanel)
	for _, f := range autonomous.Fields {
		if f.Field == "lead_use" {
			t.Error("lead_use leaked into Autonomous runs")
		}
	}
}

// With no distinct lead wired the toggle would be a no-op, so it renders
// nothing — but it must still be a field, so it can hold its place INLINE in
// Reasoning rather than being appended to the end of the form (which is how it
// got mis-filed in the first place).
func TestLeadModelFieldHiddenWithoutDistinctLead(t *testing.T) {
	f := leadUseField(false, AgentRecord{})
	if f.Type != "hidden" {
		t.Errorf("without a distinct lead the toggle should render nothing, got type %q", f.Type)
	}
	if f.Default != "" {
		t.Error("the hidden placeholder must not contribute a value to the save payload")
	}
}

// The voice is heard before it is chosen: the section holding the Voice
// field previews it, and no other section gets the button.
func TestOnlyTheVoiceSectionPreviewsAVoice(t *testing.T) {
	fields := []ui.FormField{
		{Field: "name", Label: "Name"},
		{Type: "header", Label: "Capability"},
		{Field: "fleet", Label: "Conductor", Type: "toggle"},
		{Type: "header", Label: "Messages"},
		{Field: "tag_name", Label: "Sign", Type: "toggle"},
		{Type: "header", Label: "Voice"},
		{Field: "voice", Label: "Voice"},
	}
	secs := splitAgentFormSections("a1", "../api/agents?id=a1", fields, "")
	previews := map[string]string{}
	for _, s := range secs {
		if p, ok := s.Body.(ui.FormPanel); ok && p.TestURL != "" {
			previews[s.Title] = p.TestURL + " " + p.TestLabel
		}
	}
	if len(previews) != 1 || previews["Voice"] != media.ReadAloudPath+"/preview Preview voice" {
		t.Errorf("only the Voice section should preview: %v", previews)
	}
}
