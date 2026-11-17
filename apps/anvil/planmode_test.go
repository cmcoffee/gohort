package anvil

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func toolNames(c *toolCtx) string {
	var names []string
	for _, td := range c.codeTools() {
		names = append(names, td.Tool.Name)
	}
	return "," + strings.Join(names, ",") + ","
}

// In plan mode the assistant looks, plans and runs the check, and nothing
// that changes the project is offered: no edit, no command, no action.
func TestPlanModeOffersNothingThatChangesTheProject(t *testing.T) {
	p := Project{ID: "p", Name: "P", Kind: KindRoot, Root: t.TempDir(), CheckCommand: "go test ./...",
		Actions: []ProjectAction{{ID: "deploy", Label: "Deploy", ExposeToAgent: true, Command: "make deploy"}}}
	all := toolNames(&toolCtx{project: p, isAdmin: true})
	planning := toolNames(&toolCtx{project: p, isAdmin: true, planning: true})
	for _, n := range []string{"read_file", "search_code", "git", "plan", "check"} {
		if !strings.Contains(planning, ","+n+",") {
			t.Errorf("plan mode lacks %s: %s", n, planning)
		}
	}
	for _, n := range []string{"edit_file", "create_file", "delete_file", "run"} {
		if strings.Contains(planning, ","+n+",") || !strings.Contains(all, ","+n+",") {
			t.Errorf("%s: offered in plan mode %v, otherwise %v", n, strings.Contains(planning, ","+n+","), strings.Contains(all, ","+n+","))
		}
	}
	if deploy := "," + toolNameFor("Deploy") + ","; strings.Contains(planning, deploy) || !strings.Contains(all, deploy) {
		t.Errorf("the project's action: in plan mode %s, otherwise %s", planning, all)
	}
}

// The switch is each person's and stays as they left it.
func TestPlanModeIsKeptPerPerson(t *testing.T) {
	_, serve := bundleRoutes(t)
	get := func(user string) bool {
		w := serve(user, "GET", "/api/plan_mode", nil, "")
		var d map[string]bool
		json.Unmarshal(w.Body.Bytes(), &d)
		return d["plan_mode"]
	}
	if get("alice") {
		t.Fatal("plan mode starts on")
	}
	if w := serve("alice", "POST", "/api/plan_mode", []byte(`{"plan_mode":true}`), "application/json"); w.Code != 200 {
		t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
	}
	if !get("alice") || get("root") {
		t.Errorf("alice %v, root %v", get("alice"), get("root"))
	}
	serve("alice", "POST", "/api/plan_mode", []byte(`{"plan_mode":false}`), "application/json")
	if get("alice") {
		t.Error("plan mode did not turn off")
	}
}

// Undo from a message puts back every change below it, newest first, so a
// file changed twice returns to what it was before both; a file changed
// since stops it there, and what was put back is said.
func TestUndoPutsBackTheChangesFromAMessageOn(t *testing.T) {
	T, serve := bundleRoutes(t)
	udb := UserDB(T.DB, "root")
	root := t.TempDir()
	p := Project{ID: "r1", Name: "R", Kind: KindRoot, Root: root, Owner: "root"}
	T.saveProject(udb, p)
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	mustWrite(t, a, "two\n")
	e1 := journalWrite(udb, p.ID, "s", "a.txt", "zero\n", true, "one\n", false)
	e2 := journalWrite(udb, p.ID, "s", "a.txt", "one\n", true, "two\n", false)
	mustWrite(t, b, "new\n")
	e3 := journalWrite(udb, p.ID, "s", "b.txt", "", false, "new\n", false)
	body, _ := json.Marshal(map[string]any{"ids": []string{e1.ID, e2.ID, e3.ID, e1.ID}})
	w := serve("root", "POST", "/api/undo", body, "application/json")
	if w.Code != 200 {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if got := readFile(t, a); got != "zero\n" {
		t.Errorf("a.txt holds %q", got)
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Error("the created file is still there")
	}
	if !strings.Contains(w.Body.String(), "Put back 3 changes") {
		t.Errorf("said %s", w.Body.String())
	}
	// Undo halts the session that made them, as a single revert does
	// (halt.go).
	t.Cleanup(func() { liftHalt("s") })
	if got := haltedPaths("s"); len(got) != 2 {
		t.Errorf("the session is halted on %v, want a.txt and b.txt", got)
	}

	// A file changed since its edit stops the undo at that change.
	mustWrite(t, a, "one\n")
	e4 := journalWrite(udb, p.ID, "s", "a.txt", "zero\n", true, "one\n", false)
	mustWrite(t, b, "x\n")
	e5 := journalWrite(udb, p.ID, "s", "b.txt", "", false, "x\n", false)
	mustWrite(t, b, "x, and typed since\n")
	body, _ = json.Marshal(map[string]any{"ids": []string{e4.ID, e5.ID}})
	w = serve("root", "POST", "/api/undo", body, "application/json")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Nothing was put back") || readFile(t, a) != "one\n" {
		t.Errorf("undo past a later change: %d %s; a.txt %q", w.Code, w.Body.String(), readFile(t, a))
	}

	// Someone who may not change the project cannot undo in it.
	if w := serve("alice", "POST", "/api/undo", body, "application/json"); w.Code == 200 && strings.Contains(w.Body.String(), "Put back") {
		t.Errorf("alice undid root's changes: %s", w.Body.String())
	}
}

// A "#" line is added to the notes; a second follows it; more than one line,
// or notes already full, are refused.
func TestARememberedLineJoinsTheNotes(t *testing.T) {
	udb := memDB(t)
	if err := rememberLine(udb, "ns", "# the build needs CGO_ENABLED=0"); err != nil {
		t.Fatal(err)
	}
	if err := rememberLine(udb, "ns", "tests live beside the code"); err != nil {
		t.Fatal(err)
	}
	if got := LoadOperatingNotes(udb, "ns").Text; got != "- the build needs CGO_ENABLED=0\n- tests live beside the code" {
		t.Errorf("notes %q", got)
	}
	if rememberLine(udb, "ns", "#  ") == nil || rememberLine(udb, "ns", "# one\ntwo") == nil {
		t.Error("an empty or two-line note was kept")
	}
	SaveOperatingNotes(udb, "full", strings.Repeat("x", OperatingNotesCap-5))
	if err := rememberLine(udb, "full", "# one more thing"); err == nil || !strings.Contains(err.Error(), "full") {
		t.Errorf("full notes: %v", err)
	}
}

// The page takes a one-line "#" message for the notes and sends anything
// else; Undo is offered on your messages.
func TestThePageTakesARememberLine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := `var takers = [], actions = [], posted = [];
var window = {uiRegisterSendInterceptor: function(f) { takers.push(f); },
  uiRegisterBubbleAction: function(a) { actions.push(a); },
  anvilProject: function() { return 'p1'; },
  anvilFetchJSON: function(url, o) { posted.push([url, o.body]); return Promise.resolve({message: 'ok'}); }};
function setTimeout() {}
` + anvilHabitsJS + `
function take(text, att) { return takers[0]({text: text, attachments: att || 0, toast: function() {}}); }
var bad = [];
if (!take('# the build needs CGO_ENABLED=0')) bad.push('a # line was sent');
if (take('fix the build')) bad.push('a message was taken');
if (take('# Title\nand a body')) bad.push('a pasted document was taken');
if (take('# with a picture', 1)) bad.push('a message with an attachment was taken');
if (posted.length !== 1 || posted[0][0] !== 'api/notes/add?project_id=p1' || JSON.parse(posted[0][1]).text !== 'the build needs CGO_ENABLED=0') bad.push('posted ' + JSON.stringify(posted));
if (actions.length !== 1 || actions[0].role !== 'user' || actions[0].label !== 'Undo') bad.push('actions ' + JSON.stringify(actions));
bad.forEach(function(b) { console.log(b); });
process.exit(bad.length ? 1 : 0);
`
	f := filepath.Join(t.TempDir(), "habits.js")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
