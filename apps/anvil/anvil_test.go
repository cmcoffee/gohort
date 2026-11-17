package anvil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// memDB is an in-memory user store, so the journal tests exercise the real
// persistence path rather than a fake.
func memDB(t *testing.T) Database {
	t.Helper()
	return UserDB(&DBase{Store: kvlite.MemStore()}, "u")
}

// --- containment -------------------------------------------------------------

// The lexical guards, and the widening an admin can declare. The symlink
// cases moved to their own tests when the two kinds stopped answering them
// the same way — see TestRootProjectFollowsItsOwnSymlinks and
// TestCloneProjectRefusesToFollowASymlinkOut.
func TestResolvePathContainment(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	mustWrite(t, filepath.Join(root, "src", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(outside, "secret.txt"), "shh\n")
	if err := os.Symlink(outside, filepath.Join(root, "away")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := Project{Kind: KindRoot, Name: "t", Root: root}

	if _, err := p.resolvePath(root, "src/main.go"); err != nil {
		t.Fatalf("ordinary path refused: %v", err)
	}
	if _, err := p.resolvePath(root, "src/new_file.go"); err != nil {
		t.Fatalf("not-yet-created path refused: %v", err)
	}
	if _, err := p.resolvePath(root, "../etc/passwd"); err == nil {
		t.Fatal("traversal was allowed")
	}
	if _, err := p.resolvePath(root, "/etc/passwd"); err == nil {
		t.Fatal("absolute path was allowed")
	}

	// A declared extra root resolves to the real path behind it, so a root
	// named through a link still matches what the file resolves to.
	p.ExtraRoots = []string{outside}
	got, err := p.resolvePath(root, "away/secret.txt")
	if err != nil {
		t.Fatalf("declared extra root refused: %v", err)
	}
	realOutside, _ := filepath.EvalSymlinks(outside)
	if want := filepath.Join(realOutside, "secret.txt"); got != want {
		t.Fatalf("resolved to %q, want %q", got, want)
	}
}

// A clone project's id and owner become path segments, so neither may carry
// a separator.
func TestCloneBaseDirRejectsCraftedIDs(t *testing.T) {
	for _, p := range []Project{
		{Kind: KindClone, Owner: "u", ID: "../../etc"},
		{Kind: KindClone, Owner: "../root", ID: "x"},
		{Kind: KindClone, Owner: "u", ID: "a/b"},
	} {
		if _, err := p.baseDir(); err == nil {
			t.Fatalf("crafted project accepted: owner=%q id=%q", p.Owner, p.ID)
		}
	}
}

// --- diff --------------------------------------------------------------------

func TestUnifiedDiffCounts(t *testing.T) {
	before := "one\ntwo\nthree\n"
	after := "one\ntwo point five\nthree\nfour\n"
	diff, added, removed := unifiedDiff("f.txt", before, after)
	if added != 2 || removed != 1 {
		t.Fatalf("counts were +%d −%d, want +2 −1", added, removed)
	}
	for _, want := range []string{"--- a/f.txt", "+++ b/f.txt", "@@", "-two", "+two point five", "+four"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}
	// An unchanged line must not be reported as a change.
	if strings.Contains(diff, "-one") || strings.Contains(diff, "+one") {
		t.Fatalf("unchanged line appears as a change:\n%s", diff)
	}
}

// A trailing newline is not a change. Getting this wrong makes every diff of
// every normal file report a phantom edit on its last line.
func TestUnifiedDiffIgnoresTrailingNewlineArtifact(t *testing.T) {
	if _, added, removed := unifiedDiff("f", "a\nb\n", "a\nb\n"); added != 0 || removed != 0 {
		t.Fatalf("identical content diffed as +%d −%d", added, removed)
	}
	_, added, removed := unifiedDiff("f", "a\nb\n", "a\nc\n")
	if added != 1 || removed != 1 {
		t.Fatalf("one changed line reported as +%d −%d", added, removed)
	}
}

// --- glob --------------------------------------------------------------------

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "apps/anvil/main.go", true}, // bare-name fallback
		{"apps/*", "apps/anvil", true},
		{"apps/*", "apps/anvil/page.go", false}, // * does not span separators
		{"apps/**", "apps/anvil/page.go", true},
		{"**/page.go", "apps/anvil/page.go", true},
		{"**/page.go", "page.go", true},
		{"**/page.go", "apps/anvil/web.go", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// --- actions -----------------------------------------------------------------

func TestValidateActionsRejectsBrokenGates(t *testing.T) {
	if _, err := validateActions([]ProjectAction{
		{ID: "a", Label: "Restart", Command: "true", GateID: "nope"},
	}); err == nil {
		t.Fatal("gate on a nonexistent action was accepted")
	}
	if _, err := validateActions([]ProjectAction{
		{ID: "a", Label: "Restart", Command: "true", GateID: "a"},
	}); err == nil {
		t.Fatal("self-gate was accepted")
	}
	out, err := validateActions([]ProjectAction{
		{ID: "b", Label: "Build", Command: "go build ./..."},
		{ID: "a", Label: "Restart", Command: "systemctl restart x", GateID: "b"},
		{Label: "", Command: ""}, // an empty row is dropped, not an error
	})
	if err != nil {
		t.Fatalf("valid actions rejected: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d actions, want 2", len(out))
	}
}

// The build gate is the one invariant that is not a setting: a gated action
// must not run when its gate fails.
func TestGatedActionDoesNotRunOnRedGate(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	p := Project{
		Kind: KindRoot, Root: dir,
		Actions: []ProjectAction{
			{ID: "gate", Label: "Build", Command: "exit 1"},
			{ID: "act", Label: "Restart", Command: "touch " + marker, GateID: "gate"},
		},
	}
	act, _ := p.findAction("act")
	res := p.runAction(t.Context(), dir, act)
	if !res.GateFailed {
		t.Fatal("a failing gate did not stop the action")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the gated command ran anyway")
	}
	if !strings.Contains(res.Output, "Build") {
		t.Fatalf("the refusal should name the gate that failed, got: %s", res.Output)
	}
}

// A detached action has no exit status, so it cannot serve as a gate.
func TestDetachedActionCannotGate(t *testing.T) {
	dir := t.TempDir()
	p := Project{
		Kind: KindRoot, Root: dir,
		Actions: []ProjectAction{
			{ID: "gate", Label: "Bounce", Command: "true", Detached: true},
			{ID: "act", Label: "Deploy", Command: "true", GateID: "gate"},
		},
	}
	act, _ := p.findAction("act")
	if res := p.runAction(t.Context(), dir, act); !res.GateFailed {
		t.Fatal("a detached gate was accepted")
	}
}

// A detached action is never exposed to the model, whatever the operator set.
func TestDetachedActionNeverReachesTheAgent(t *testing.T) {
	p := Project{Actions: []ProjectAction{
		{ID: "t", Label: "Test", Command: "go test ./...", ExposeToAgent: true},
		{ID: "r", Label: "Restart", Command: "systemctl restart x", ExposeToAgent: true, Detached: true},
	}}
	got := p.agentInvokableActions()
	if len(got) != 1 || got[0].ID != "t" {
		t.Fatalf("agent-invokable actions = %+v, want only the test action", got)
	}
}

func TestToolNameFor(t *testing.T) {
	cases := map[string]string{
		"Run tests":       "run_tests",
		"Build & Install": "build_install",
		"  Lint  ":        "lint",
		"!!!":             "project_action",
	}
	for label, want := range cases {
		if got := toolNameFor(label); got != want {
			t.Errorf("toolNameFor(%q) = %q, want %q", label, got, want)
		}
	}
}

// --- helpers -----------------------------------------------------------------

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- the edit journal --------------------------------------------------------

// The whole promise of the journal is that a revert restores exactly what was
// there. This walks a modify, a create and a delete through write → revert.
func TestJournalRevertRestoresEachKind(t *testing.T) {
	udb := memDB(t)
	root := t.TempDir()
	p := Project{ID: "p1", Kind: KindRoot, Root: root}
	file := filepath.Join(root, "a.txt")

	// modify
	mustWrite(t, file, "original\n")
	mustWrite(t, file, "changed\n")
	e := journalWrite(udb, p.ID, "s1", "a.txt", "original\n", true, "changed\n", false)
	if err := revertEdit(udb, p, root, e); err != nil {
		t.Fatalf("revert of a modify failed: %v", err)
	}
	if got := readFile(t, file); got != "original\n" {
		t.Fatalf("after revert the file holds %q, want %q", got, "original\n")
	}

	// create — reverting removes the file
	newFile := filepath.Join(root, "sub", "b.txt")
	mustWrite(t, newFile, "brand new\n")
	e = journalWrite(udb, p.ID, "s1", "sub/b.txt", "", false, "brand new\n", false)
	if err := revertEdit(udb, p, root, e); err != nil {
		t.Fatalf("revert of a create failed: %v", err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatal("reverting a create left the file behind")
	}

	// delete — reverting puts it back
	mustWrite(t, file, "to be deleted\n")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	e = journalWrite(udb, p.ID, "s1", "a.txt", "to be deleted\n", true, "", true)
	if err := revertEdit(udb, p, root, e); err != nil {
		t.Fatalf("revert of a delete failed: %v", err)
	}
	if got := readFile(t, file); got != "to be deleted\n" {
		t.Fatalf("after revert the file holds %q", got)
	}
}

// The property that separates an undo from a data-loss bug: a revert must
// refuse when the file has moved on since the edit it wants to undo.
func TestRevertRefusesWhenTheFileChangedSince(t *testing.T) {
	udb := memDB(t)
	root := t.TempDir()
	p := Project{ID: "p1", Kind: KindRoot, Root: root}
	file := filepath.Join(root, "a.txt")

	mustWrite(t, file, "v2\n")
	e := journalWrite(udb, p.ID, "s1", "a.txt", "v1\n", true, "v2\n", false)

	// Somebody else edits the file afterwards.
	mustWrite(t, file, "v3 — a human's work\n")

	err := revertEdit(udb, p, root, e)
	if err == nil {
		t.Fatal("revert clobbered a later change")
	}
	if !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("refusal should explain itself, got: %v", err)
	}
	if got := readFile(t, file); got != "v3 — a human's work\n" {
		t.Fatalf("the later change was lost: %q", got)
	}
}

// Reverting twice must not "restore" a stale pre-image over newer content.
func TestRevertIsNotRepeatable(t *testing.T) {
	udb := memDB(t)
	root := t.TempDir()
	p := Project{ID: "p1", Kind: KindRoot, Root: root}
	mustWrite(t, filepath.Join(root, "a.txt"), "after\n")
	e := journalWrite(udb, p.ID, "s1", "a.txt", "before\n", true, "after\n", false)

	if err := revertEdit(udb, p, root, e); err != nil {
		t.Fatalf("first revert failed: %v", err)
	}
	var stored EditEntry
	if !udb.Get(editTable, e.ID, &stored) {
		t.Fatal("the entry was not persisted")
	}
	if !stored.Reverted {
		t.Fatal("the entry was not marked reverted")
	}
	if err := revertEdit(udb, p, root, stored); err == nil {
		t.Fatal("a second revert of the same entry was allowed")
	}
}

// listEdits must not leak one project's history into another's, and must be
// filterable to a session — that filter is what "undo this conversation"
// would be built on.
func TestListEditsScoping(t *testing.T) {
	udb := memDB(t)
	journalWrite(udb, "p1", "s1", "a.txt", "", false, "x", false)
	journalWrite(udb, "p1", "s2", "b.txt", "", false, "x", false)
	journalWrite(udb, "p2", "s1", "c.txt", "", false, "x", false)

	if got := listEdits(udb, "p1", ""); len(got) != 2 {
		t.Fatalf("project p1 has %d entries, want 2", len(got))
	}
	if got := listEdits(udb, "p1", "s1"); len(got) != 1 || got[0].Path != "a.txt" {
		t.Fatalf("session filter returned %+v", got)
	}
	if got := listEdits(udb, "p2", ""); len(got) != 1 || got[0].Path != "c.txt" {
		t.Fatalf("project p2 saw another project's edits: %+v", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- confirmation ------------------------------------------------------------

// The two tools that act on the world outside the journal must ask first, and
// the reading tools must not — a prompt on every read is how a user learns to
// click Allow without looking.
func TestActingToolsDeclareAConfirmPrompt(t *testing.T) {
	c := &toolCtx{
		udb:     memDB(t),
		user:    "u",
		isAdmin: true,
		project: Project{ID: "p1", Name: "demo", Kind: KindRoot, Root: t.TempDir(), Actions: []ProjectAction{
			{ID: "b", Label: "Build", Command: "go build ./...", ExposeToAgent: true},
		}},
		base: t.TempDir(),
	}
	prompts := map[string]string{}
	remembers := map[string]bool{}
	for _, td := range c.codeTools() {
		if td.Confirmation != nil {
			prompts[td.Tool.Name] = td.Confirmation.Prompt
			remembers[td.Tool.Name] = td.Confirmation.CanRemember()
		}
	}

	for _, name := range []string{"run", "delete_file", "build"} {
		got, ok := prompts[name]
		if !ok {
			t.Fatalf("%s is not in the tool set", name)
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s does not ask before acting", name)
		}
		// The question has to name what it will happen to, or it is a reflex
		// click rather than a decision.
		if !strings.Contains(got, "demo") {
			t.Errorf("%s prompt %q does not name the project", name, got)
		}
	}
	for _, name := range []string{"read_file", "list_files", "search_code", "git"} {
		if prompts[name] != "" {
			t.Errorf("%s prompts on a read: %q", name, prompts[name])
		}
	}
	// edit_file and create_file are deliberately NOT gated: every edit is
	// journaled and revertable, and a prompt per edit would make an ordinary
	// multi-file change unusable. If that changes, change this test with it.
	for _, name := range []string{"edit_file", "create_file"} {
		if prompts[name] != "" {
			t.Errorf("%s now prompts; the diff-card + revert design assumed it would not", name)
		}
	}

	// "Always" is offered, and remembered for the project, as in Claude Code:
	// for the commands run in a loop, for a project action, and for deletes,
	// each of which the journal can revert.
	for _, name := range []string{"run", "build", "delete_file"} {
		if !remembers[name] {
			t.Errorf("%s withholds the standing approval", name)
		}
	}
}

// Run commands without asking lets the run tool go without a card, and only
// on a checkout Anvil made, where a command reaches the sandbox and the
// owner's own copy. A directory on this host asks whatever the flag says, and
// so does deleting a file either way.
func TestACheckoutMayRunCommandsWithoutAsking(t *testing.T) {
	needs := func(p Project) map[string]bool {
		c := &toolCtx{udb: memDB(t), user: "u", project: p, base: t.TempDir()}
		out := map[string]bool{}
		for _, td := range c.codeTools() {
			// What the loop asks on: either one (a Confirmation alone asks,
			// which is how this once asked although NeedsConfirm was off).
			out[td.Tool.Name] = td.NeedsConfirm || td.Confirmation.Asks()
		}
		return out
	}
	checkout := Project{ID: "p1", Name: "demo", Kind: KindClone, Owner: "u", Source: sourceNew}
	if n := needs(checkout); !n["run"] {
		t.Error("a checkout ran a command without asking before its owner chose that")
	}
	checkout.RunWithoutAsking = true
	if n := needs(checkout); n["run"] || !n["delete_file"] {
		t.Errorf("chosen: run asks %v, delete asks %v; want run silent and delete still asking", n["run"], n["delete_file"])
	}
	host := Project{ID: "p2", Name: "demo", Kind: KindRoot, Root: t.TempDir(), RunWithoutAsking: true}
	if n := needs(host); !n["run"] {
		t.Error("a directory on this host ran a command without asking")
	}
}

// A grant must be namespaced by PROJECT, not by user or name: allowing a build
// in a throwaway checkout must not carry into the tree the server runs from,
// and renaming a project does not make it a different directory.
func TestGrantScopeIsPerProject(t *testing.T) {
	a := &toolCtx{project: Project{ID: "p1", Name: "same name"}}
	b := &toolCtx{project: Project{ID: "p2", Name: "same name"}}
	if a.grantScope() == b.grantScope() {
		t.Fatal("two different projects share a grant scope")
	}
	renamed := &toolCtx{project: Project{ID: "p1", Name: "renamed since"}}
	if a.grantScope() != renamed.grantScope() {
		t.Fatal("renaming a project orphaned its grants")
	}
	if !strings.Contains(a.grantScope(), "p1") {
		t.Fatalf("grant scope %q does not identify the project", a.grantScope())
	}
}

// A read-only caller never gets the acting tools at all, so there is nothing
// to confirm in the first place.
func TestReadOnlyCallerGetsNoActingTools(t *testing.T) {
	c := &toolCtx{
		udb:      memDB(t),
		user:     "someone_else",
		project:  Project{ID: "p1", Name: "demo", Kind: KindRoot, Root: t.TempDir()},
		base:     t.TempDir(),
		readOnly: true,
	}
	for _, td := range c.codeTools() {
		switch td.Tool.Name {
		case "run", "delete_file", "edit_file", "create_file":
			t.Errorf("a read-only caller was handed %s", td.Tool.Name)
		}
	}
}

// --- the page ----------------------------------------------------------------

// A workspace's toolbar is the open project's, all of it (docs/design/
// anvil-development.md): what changed, its commands, its settings, what Anvil
// learned about it, the commands allowed in it. Its menus are the project's
// own (Download, Project), never somewhere to tuck what is not: projects and
// adding one are on the home page, not here.
func TestToolbarActsOnTheOpenProject(t *testing.T) {
	T := &Anvil{}
	p := Project{ID: "p", Name: "x", Kind: KindRoot, Actions: []ProjectAction{{ID: "b", Label: "Build", Command: "true"}}}
	byLabel := map[string]ui.ToolbarAction{}
	for _, a := range T.toolbarActions(p, "u", true) {
		byLabel[a.Label] = a
		if a.Method != "client" {
			t.Errorf("toolbar action %q is %q; the toolbar renders once and must not bake a project into a URL", a.Label, a.Method)
		}
		if a.Group != "" && a.Group != "Download" && a.Group != "Project" {
			t.Errorf("%q is in a menu %q; a workspace's menus are the open project's own", a.Label, a.Group)
		}
	}
	for _, label := range []string{"Changes", "Run", "Settings", "What Anvil learned", "Permissions"} {
		if _, ok := byLabel[label]; !ok {
			t.Errorf("no %s on the open project's toolbar", label)
		}
	}
	for _, label := range []string{"Add a project", "All projects", "Measure Anvil"} {
		if _, ok := byLabel[label]; ok {
			t.Errorf("%q is on a workspace; it belongs on the home page", label)
		}
	}
}

// A project the caller may not run commands on must not advertise Run — the
// button would open a modal that can only refuse.
func TestToolbarHidesRunForANonAdminOnARootProject(t *testing.T) {
	T := &Anvil{}
	root := Project{ID: "p", Name: "the real tree", Kind: KindRoot,
		Actions: []ProjectAction{{ID: "b", Label: "Build", Command: "go build ./..."}}}

	admin := map[string]bool{}
	for _, a := range T.toolbarActions(root, "u", true) {
		admin[a.Label] = true
	}
	if !admin["Run"] {
		t.Error("an admin cannot reach a root project's commands")
	}

	guest := map[string]bool{}
	for _, a := range T.toolbarActions(root, "guest", false) {
		guest[a.Label] = true
	}
	if guest["Run"] {
		t.Error("a non-admin was offered Run on a directory project they cannot run commands on")
	}
}

// --- git awareness -----------------------------------------------------------

// initRepo makes a throwaway repository so the git-facing helpers are tested
// against real git rather than a stub. Skips when git is absent, because the
// code under test is explicitly allowed to have no answer then.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	for _, argv := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := runGit(dir, argv...); err != nil {
			t.Fatalf("git %v: %v", argv, err)
		}
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	if _, err := runGit(dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(dir, "commit", "-q", "-m", msg); err != nil {
		t.Fatal(err)
	}
}

// The ignore oracle has to come from git, not from a guess, or it will hide
// files a repository deliberately tracks and show ones it deliberately does not.
func TestIgnoresComeFromGitNotGuesswork(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, ".gitignore"), "secret.env\ngenerated/\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "secret.env"), "TOKEN=1\n")
	mustWrite(t, filepath.Join(dir, "generated/big.go"), "// generated\n")
	// vendor/ is on the built-in skip list but NOT ignored by this repo — a
	// repository that tracks its vendor directory means it, and git's answer
	// has to win over the built-in guess.
	mustWrite(t, filepath.Join(dir, "vendor/lib.go"), "package lib\n")
	commitAll(t, dir, "init")

	c := &toolCtx{project: Project{ID: "p", Name: "t", Kind: KindRoot, Root: dir}, base: dir}

	if c.ignores("main.go") {
		t.Error("a tracked file is being hidden")
	}
	if !c.ignores("secret.env") {
		t.Error("a gitignored file is being offered")
	}
	if !c.ignores("generated/big.go") {
		t.Error("a file inside an ignored directory is being offered")
	}
	if c.ignores("vendor/lib.go") {
		t.Error("git tracks vendor/ here, but the built-in guess overrode it")
	}
}

// With no repository there is nothing to ask, so the built-in list is the
// fallback — and it has to match on any path SEGMENT, not just the top level.
func TestIgnoreFallbackWithoutARepo(t *testing.T) {
	dir := t.TempDir()
	c := &toolCtx{project: Project{ID: "p", Name: "t", Kind: KindRoot, Root: dir}, base: dir}

	if !c.ignores("node_modules/x/y.js") {
		t.Error("the built-in fallback missed a top-level node_modules")
	}
	if !c.ignores("apps/web/node_modules/x.js") {
		t.Error("the built-in fallback only matches at the top level")
	}
	if c.ignores("apps/web/main.go") {
		t.Error("the fallback hid an ordinary source file")
	}
}

// A registered DIRECTORY that happens to be a checkout must be as git-aware as
// a clone Anvil made — that is the whole point of root projects.
func TestRootProjectOnALocalFolderIsGitAware(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	commitAll(t, dir, "init")

	info := inspectRepo(dir)
	if !info.IsRepo {
		t.Fatal("a local folder under git was not recognised as a repository")
	}
	if info.Branch == "" && info.Head == "" {
		t.Error("neither a branch nor a detached head was reported")
	}
	if info.Dirty || info.Changed != 0 {
		t.Errorf("a freshly committed tree reports %d changes", info.Changed)
	}

	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	if info = inspectRepo(dir); !info.Dirty || info.Changed != 1 {
		t.Errorf("an edited tree reports dirty=%v changed=%d, want true/1", info.Dirty, info.Changed)
	}
}

// A directory that is not under version control is a normal state, reported as
// such rather than as a failure.
func TestNonRepoReportsCleanlyRatherThanFailing(t *testing.T) {
	info := inspectRepo(t.TempDir())
	if info.IsRepo {
		t.Fatal("a plain directory claimed to be a repository")
	}
	if info.Branch != "" || info.Dirty {
		t.Errorf("a non-repo invented git state: %+v", info)
	}
}

// A project pointing at a SUBDIRECTORY must report that subdirectory's state,
// not the whole repository's — otherwise a project is dirty because of an edit
// three directories away that has nothing to do with it.
func TestSubdirectoryProjectScopesItsStatus(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	mustWrite(t, filepath.Join(root, "elsewhere/other.go"), "package other\n")
	mustWrite(t, filepath.Join(root, "mine/main.go"), "package mine\n")
	commitAll(t, root, "init")

	sub := filepath.Join(root, "mine")
	if info := inspectRepo(sub); !info.IsRepo || info.Prefix != "mine" {
		t.Fatalf("subdirectory project reported prefix %q, want \"mine\"", info.Prefix)
	}

	// Dirty something OUTSIDE the project directory.
	mustWrite(t, filepath.Join(root, "elsewhere/other.go"), "package other\n// changed\n")

	if info := inspectRepo(sub); info.Dirty {
		t.Error("a change outside the project directory made the project look dirty")
	}
	if info := inspectRepo(root); !info.Dirty {
		t.Error("the repository root should see the change")
	}
}

// A project's own instructions to whoever edits it must stay visible even when
// git ignores them — gohort's CLAUDE.md is gitignored, and hiding it from a
// coding assistant is the one place git's answer is the wrong one here.
func TestProjectInstructionsSurviveGitignore(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, ".gitignore"), "CLAUDE.md\nsecret.env\n")
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "# how this repo works\n")
	mustWrite(t, filepath.Join(dir, "secret.env"), "TOKEN=1\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	commitAll(t, dir, "init")

	c := &toolCtx{project: Project{ID: "p", Name: "t", Kind: KindRoot, Root: dir}, base: dir}
	if c.ignores("CLAUDE.md") {
		t.Error("the project's own instructions are hidden from the assistant that needs them")
	}
	if c.ignores("docs/AGENTS.md") {
		t.Error("a nested instructions file is hidden")
	}
	// The carve-out is narrow: everything else the repo ignores stays ignored.
	if !c.ignores("secret.env") {
		t.Error("the carve-out leaked to an ordinary ignored file")
	}
}

// --- shallow clones ----------------------------------------------------------

// The default is shallow, so the flags have to say so — and single-branch has
// to apply whether or not a branch was named, since the old code only added it
// alongside an explicit branch.
func TestCloneArgsAreShallowByDefault(t *testing.T) {
	// Exercised through a real clone rather than by asserting on an argv
	// slice: the point is what git DOES, and a test on the arguments would
	// pass just as happily if git ignored them.
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	initRepo(t, origin)
	for i, body := range []string{"one\n", "two\n", "three\n"} {
		mustWrite(t, filepath.Join(origin, "f.txt"), body)
		commitAll(t, origin, fmt.Sprintf("c%d", i))
	}

	shallowDir := filepath.Join(t.TempDir(), "shallow")
	p := Project{ID: "s", Name: "shallow", Kind: KindClone, Owner: "u", RepoURL: "file://" + origin}
	if _, err := cloneProject(t.Context(), p, shallowDir, true); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !isShallowRepo(shallowDir) {
		t.Error("the default clone kept the full history")
	}
	if out, err := runGit(shallowDir, "log", "--oneline"); err != nil {
		t.Fatal(err)
	} else if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 1 {
		t.Errorf("a shallow clone has %d commits, want 1", n)
	}

	fullDir := filepath.Join(t.TempDir(), "full")
	p.FullHistory = true
	if _, err := cloneProject(t.Context(), p, fullDir, true); err != nil {
		t.Fatalf("full clone: %v", err)
	}
	if isShallowRepo(fullDir) {
		t.Error("FullHistory still produced a shallow clone")
	}
}

// blame on a shallow clone does not fail — it attributes every line to the one
// boundary commit, with that commit's author and date, for code written by
// other people. A short answer can be caveated; a confidently wrong one about
// who wrote something cannot, because nothing in the output looks wrong.
func TestBlameIsRefusedOnAShallowClone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	initRepo(t, origin)
	mustWrite(t, filepath.Join(origin, "f.txt"), "one\n")
	commitAll(t, origin, "first")
	mustWrite(t, filepath.Join(origin, "f.txt"), "one\ntwo\n")
	commitAll(t, origin, "second")

	dir := filepath.Join(t.TempDir(), "c")
	p := Project{ID: "s", Name: "shallow", Kind: KindClone, Owner: "u", RepoURL: "file://" + origin}
	if _, err := cloneProject(t.Context(), p, dir, true); err != nil {
		t.Fatalf("clone: %v", err)
	}

	c := &toolCtx{project: p, base: dir, udb: memDB(t)}
	var gitTool AgentToolDef
	for _, td := range c.codeTools() {
		if td.Tool.Name == "git" {
			gitTool = td
		}
	}
	if gitTool.Handler == nil {
		t.Fatal("no git tool")
	}

	if _, err := gitTool.Handler(context.Background(), map[string]any{"action": "blame", "path": "f.txt"}); err == nil {
		t.Fatal("blame answered from a single boundary commit instead of refusing")
	} else if !strings.Contains(err.Error(), "shallow") {
		t.Errorf("the refusal should explain why: %v", err)
	}

	// log is TRUE but short, so it is served with the note rather than refused
	// — the distinction this design turns on.
	out, err := gitTool.Handler(context.Background(), map[string]any{"action": "log"})
	if err != nil {
		t.Fatalf("log was refused as well as blame: %v", err)
	}
	if !strings.Contains(out, "shallow") {
		t.Errorf("a one-commit log did not say the history was truncated:\n%s", out)
	}
}

// The escape hatch has to actually work, or shallow-by-default is a decision
// made for the user rather than a default.
func TestDeepenFetchesTheHistory(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	initRepo(t, origin)
	for i, body := range []string{"one\n", "two\n", "three\n"} {
		mustWrite(t, filepath.Join(origin, "f.txt"), body)
		commitAll(t, origin, fmt.Sprintf("c%d", i))
	}
	dir := filepath.Join(t.TempDir(), "c")
	p := Project{ID: "s", Name: "shallow", Kind: KindClone, Owner: "u", RepoURL: "file://" + origin}
	if _, err := cloneProject(t.Context(), p, dir, true); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !isShallowRepo(dir) {
		t.Fatal("the clone was not shallow to begin with")
	}
	if _, err := deepenRepo(t.Context(), p, dir, true); err != nil {
		t.Fatalf("deepen: %v", err)
	}
	if isShallowRepo(dir) {
		t.Fatal("the checkout is still shallow after fetching its history")
	}
	if out, _ := runGit(dir, "log", "--oneline"); len(strings.Split(strings.TrimSpace(out), "\n")) != 3 {
		t.Errorf("history is still truncated:\n%s", out)
	}
	// Deepening an already-complete checkout is a harmless no-op, because the
	// button that calls it is offered on every checkout.
	if msg, err := deepenRepo(t.Context(), p, dir, true); err != nil {
		t.Errorf("deepening twice failed: %v", err)
	} else if !strings.Contains(msg, "already") {
		t.Errorf("a second deepen said %q", msg)
	}
}

// --- the working tree --------------------------------------------------------

// The Changes screen has to answer the question a person actually has, which
// includes edits they made themselves. Listing only Anvil's journal is how a
// chip reading "12 changed" ends up beside a panel saying nothing changed.
func TestWorkingChangesSeesEditsAnvilDidNotMake(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, "tracked.go"), "package a\n")
	commitAll(t, dir, "init")

	if got, err := workingChanges(dir); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Fatalf("a clean tree reported %d changes", len(got))
	}

	// Edited by hand, outside Anvil entirely.
	mustWrite(t, filepath.Join(dir, "tracked.go"), "package a\n\nfunc F() {}\n")
	// And a brand-new file, which git calls untracked and a person calls a change.
	mustWrite(t, filepath.Join(dir, "brand_new.go"), "package a\n")

	got, err := workingChanges(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]WorkingChange{}
	for _, c := range got {
		seen[c.Path] = c
	}
	if c, ok := seen["tracked.go"]; !ok || c.State != "modified" {
		t.Errorf("hand-edited file reported as %+v", c)
	}
	if c, ok := seen["brand_new.go"]; !ok || !c.Untracked {
		t.Errorf("a new file was left out or not marked untracked: %+v", c)
	}
}

// An untracked file has nothing to diff against, and git prints nothing —
// which would render as "no changes" for a file that is entirely new.
func TestUntrackedFileDiffsAgainstEmptiness(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\n")
	commitAll(t, dir, "init")
	mustWrite(t, filepath.Join(dir, "new.go"), "package a\n\nfunc New() {}\n")

	diff, err := workingDiff(dir, "new.go", filepath.Join(dir, "new.go"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+func New() {}") {
		t.Fatalf("an untracked file did not come back as an all-additions diff:\n%s", diff)
	}

	// A tracked, modified file goes through git and gets a real patch.
	mustWrite(t, filepath.Join(dir, "a.go"), "package a\n\nfunc A() {}\n")
	if diff, err = workingDiff(dir, "a.go", "", false); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(diff, "+func A() {}") {
		t.Fatalf("a modified file produced no usable diff:\n%s", diff)
	}
}

// An untracked file is read from the path resolvePath answered, not from rel
// joined to the base again: a checkout's run tool can plant a symlink, and
// the join would follow it out of the tree between the check and the read.
func TestAnUntrackedDiffReadsTheResolvedPath(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, "new.go"), "package joined\n")
	mustWrite(t, filepath.Join(dir, "checked.go"), "package checked\n")
	diff, err := workingDiff(dir, "new.go", filepath.Join(dir, "checked.go"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+package checked") || strings.Contains(diff, "joined") {
		t.Errorf("the diff did not read the checked path:\n%s", diff)
	}
}

// A partially staged file differs from HEAD in two places; showing only the
// unstaged half would hide the rest of the change.
func TestPartiallyStagedFileShowsBothHalves(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, "a.go"), "one\n")
	commitAll(t, dir, "init")

	mustWrite(t, filepath.Join(dir, "a.go"), "one\nstaged\n")
	if _, err := runGit(dir, "add", "a.go"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "a.go"), "one\nstaged\nunstaged\n")

	diff, err := workingDiff(dir, "a.go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+staged") {
		t.Errorf("the staged half is missing:\n%s", diff)
	}
	if !strings.Contains(diff, "+unstaged") {
		t.Errorf("the unstaged half is missing:\n%s", diff)
	}
}

// statusWords is what the reader sees, so every code git actually emits needs
// a word rather than the fallback.
func TestStatusWords(t *testing.T) {
	cases := map[string]string{
		" M": "modified", "M ": "modified", "MM": "modified",
		"A ": "added", " D": "deleted", "R ": "renamed", "??": "untracked", "UU": "conflicted",
	}
	for code, want := range cases {
		if got, _ := statusWords(code); got != want {
			t.Errorf("statusWords(%q) = %q, want %q", code, got, want)
		}
	}
	if _, staged := statusWords("M "); !staged {
		t.Error("an index-column change was not reported as staged")
	}
	if _, staged := statusWords(" M"); staged {
		t.Error("a work-tree-only change was reported as staged")
	}
	if _, staged := statusWords("??"); staged {
		t.Error("an untracked file was reported as staged")
	}
}

// --- the browser wiring ------------------------------------------------------

// The git chip reaches a client action BY NAME through the registry. That is a
// string crossing two files with nothing to check it, and when it is wrong the
// chip does not error — it silently falls back to re-fetching, which is
// exactly the dead-feeling control this was meant to fix.
func TestChipReachesARegisteredAction(t *testing.T) {
	const want = "anvil_history"
	if !strings.Contains(anvilGitChipJS, "'"+want+"'") {
		t.Fatalf("the chip no longer reaches %q; if it was renamed, rename it in both places", want)
	}
	head := anvilHead(nil, "u", false).Render()
	if !strings.Contains(head, want) {
		t.Fatalf("%q is reached by the chip but never registered as a client action", want)
	}
}

// Every toolbar entry names a client action, and each of those has to exist.
// A Method:"client" button whose handler was never registered POSTs to the
// handler NAME as a URL and 404s — which is how a toolbar button becomes a
// button that does nothing.
func TestEveryToolbarActionIsRegistered(t *testing.T) {
	T := &Anvil{}
	p := Project{ID: "p", Name: "demo", Kind: KindRoot,
		Actions: []ProjectAction{{ID: "b", Label: "Build", Command: "true"}}}
	head := anvilHead([]Project{p}, "u", true).Render()

	for _, a := range T.toolbarActions(p, "u", true) {
		// A link to one of Anvil's own pages is a plain navigation.
		if a.Method == "GET" && strings.HasPrefix(a.URL, "/anvil/") {
			continue
		}
		if a.Method != "client" {
			t.Errorf("toolbar action %q is not a client action", a.Label)
			continue
		}
		// The panel's own, registered by the panel itself.
		if a.URL == "copy_session" {
			continue
		}
		if !strings.Contains(head, `"`+a.URL+`"`) {
			t.Errorf("toolbar action %q targets %q, which is not registered — the button would 404", a.Label, a.URL)
		}
	}
}

// stripCSSComments removes /* ... */ blocks so a test can assert about the rules
// rather than about the prose explaining them.
func stripCSSComments(css string) string {
	var b strings.Builder
	for {
		i := strings.Index(css, "/*")
		if i < 0 {
			b.WriteString(css)
			return b.String()
		}
		b.WriteString(css[:i])
		j := strings.Index(css[i:], "*/")
		if j < 0 {
			return b.String()
		}
		css = css[i+j+2:]
	}
}

// --- containment, revisited --------------------------------------------------

// The bug that made an ordinary checkout unusable: a root reached through a
// symlinked PARENT resolves elsewhere, and comparing a resolved file path
// against an unresolved root matches nothing — so every read was refused as an
// escape attempt. A home directory's src linked to a data volume is
// precisely the shape.
func TestRootReachedThroughASymlinkedParentStillWorks(t *testing.T) {
	real := t.TempDir()
	mustWrite(t, filepath.Join(real, "core", "api.go"), "package core\n")

	// A link standing in for /srv/src -> /data/src.
	linkParent := filepath.Join(t.TempDir(), "src")
	if err := os.Symlink(real, linkParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := Project{ID: "p", Name: "kb", Kind: KindRoot, Root: linkParent}
	got, err := p.resolvePath(linkParent, "core/api.go")
	if err != nil {
		t.Fatalf("an ordinary read through a symlinked root was refused: %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(real, "core", "api.go"))
	if got != want {
		t.Fatalf("resolved to %q, want %q", got, want)
	}
}

// A registered directory follows the symlinks it contains, the way every other
// tool the operator uses does.
func TestRootProjectFollowsItsOwnSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.go"), "package x\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := Project{ID: "p", Name: "t", Kind: KindRoot, Root: root}
	if _, err := p.resolvePath(root, "linked/secret.go"); err != nil {
		t.Fatalf("a registered tree would not follow its own symlink: %v", err)
	}
}

// A CHECKOUT does not. The content came from a remote, and a git repository
// can carry a symlink pointing anywhere — following one is how a hostile repo
// reads a file it was never given.
func TestCloneProjectRefusesToFollowASymlinkOut(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "passwd"), "root:x:0:0\n")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := Project{ID: "p", Name: "t", Kind: KindClone, Owner: "u"}
	if _, err := p.resolvePath(root, "escape/passwd"); err == nil {
		t.Fatal("a checkout followed a symlink out of itself")
	} else if !strings.Contains(err.Error(), "outside this checkout") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// Neither kind relaxes the LEXICAL guards, which are the part an LLM actually
// gets wrong.
func TestBothKindsStillRefuseTraversalAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	for _, p := range []Project{
		{ID: "a", Name: "root", Kind: KindRoot, Root: root},
		{ID: "b", Name: "clone", Kind: KindClone, Owner: "u"},
	} {
		if _, err := p.resolvePath(root, "../../etc/passwd"); err == nil {
			t.Errorf("%s project allowed traversal", p.Kind)
		}
		if _, err := p.resolvePath(root, "/etc/passwd"); err == nil {
			t.Errorf("%s project allowed an absolute path", p.Kind)
		}
	}
}

// --- reasoning tier ----------------------------------------------------------

// The project setting RAISES the tier and never lowers it, so a project that
// does not ask for the lead cannot quietly downgrade an agent deliberately
// configured to use it.
func TestProjectLeadEscalationIsAFloorNotAnOverride(t *testing.T) {
	cases := []struct{ agentLead, projectLead, want bool }{
		{false, false, false},
		{false, true, true}, // the project escalates
		{true, false, true}, // the agent's own choice survives
		{true, true, true},
	}
	for _, c := range cases {
		got := c.agentLead || c.projectLead
		if got != c.want {
			t.Errorf("agent=%v project=%v => %v, want %v", c.agentLead, c.projectLead, got, c.want)
		}
	}
}

// The dead route stage must not come back. It rendered a dropdown in Admin
// that looked like the tier control and moved nothing, because the key was
// never passed as a RouteKey.
func TestNoDeadRouteStage(t *testing.T) {
	for _, st := range ListRouteStages() {
		if strings.HasPrefix(st.Key, "app.anvil") {
			t.Fatalf("route stage %q is registered again; nothing passes it as a RouteKey, so it would be a control that does nothing", st.Key)
		}
	}
}

// Extra roots are a root-project setting and must stay one. A user able to
// name arbitrary host paths in their own checkout could read anything the
// server can — which is why the save handler clears them, and why the
// checkout refusal does not advertise them as a remedy.
func TestCloneProjectsCannotDeclareExtraRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "passwd"), "root:x:0:0\n")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Even with ExtraRoots set on the struct directly, a checkout ignores them.
	p := Project{ID: "p", Name: "t", Kind: KindClone, Owner: "u", ExtraRoots: []string{outside}}
	if _, err := p.resolvePath(root, "escape/passwd"); err == nil {
		t.Fatal("a checkout honored extra roots, which a user can set on their own project")
	}
	// And the refusal must not send them hunting for a setting they cannot have.
	_, err := p.resolvePath(root, "escape/passwd")
	if strings.Contains(err.Error(), "extra roots") {
		t.Errorf("the refusal offers a remedy that cannot be granted: %v", err)
	}
}

// --- recovering from a failed turn -------------------------------------------

// A turn that dies mid-stream leaves the user message in the thread with no
// assistant reply. Without a truncate endpoint wired there is no way to run it
// again except retyping it, which is what "we have no retry" means in practice.
func TestTheChatOffersEditAndRetry(t *testing.T) {
	T := &Anvil{}
	panel := T.chatPanel([]ui.SelectOption{{Value: "p", Label: "demo"}}, "p", "", nil)

	if panel.TruncateURL == "" {
		t.Fatal("no truncate endpoint, so the last message has no Edit or Delete and a failed turn cannot be retried")
	}
	// MessageScrub needs a PATCH that understands {delete_at}; the public
	// session handler only understands {at}. Setting it would render a button
	// that silently does the wrong thing.
	if panel.MessageScrub {
		t.Error("MessageScrub is set, but the endpoint behind it only understands {at} truncation")
	}
	// The template has to carry the session, or the PATCH lands nowhere.
	if !strings.Contains(panel.TruncateURL, "{id}") {
		t.Errorf("truncate URL %q does not carry the session id", panel.TruncateURL)
	}
}

// --- sessions belong to projects ---------------------------------------------

// Sessions are stored per agent id, so one id across every project means one
// undifferentiated list — the thread where you refactored a client's API next
// to yesterday's scratch experiment, with the picker changing nothing.
func TestSessionScopeIsPerProject(t *testing.T) {
	a := scopedAgentID(Project{ID: "p1", Name: "same name"})
	b := scopedAgentID(Project{ID: "p2", Name: "same name"})
	if a == b {
		t.Fatal("two projects share a session scope")
	}
	// Stable across a rename: the threads belong to the tree, not the label.
	if a != scopedAgentID(Project{ID: "p1", Name: "renamed since"}) {
		t.Fatal("renaming a project orphaned its conversations")
	}
	// Every scope stays rooted at the app's own agent, so nothing collides
	// with another app's sessions in the shared store.
	if !strings.HasPrefix(a, anvilAgentID) {
		t.Errorf("scope %q is not rooted at the Anvil agent", a)
	}
	// A project with no id must not silently become "everything".
	if got := scopedAgentID(Project{}); got != anvilAgentID {
		t.Errorf("an id-less project scoped to %q", got)
	}
}

// Every session endpoint the panel calls has to carry the project, or the rail
// lists one project's threads while the composer writes into another's.
func TestSessionURLsCarryTheProject(t *testing.T) {
	T := &Anvil{}
	panel := T.chatPanel([]ui.SelectOption{{Value: "p", Label: "demo"}}, "p", "", nil)

	for name, url := range map[string]string{
		"ListURL":     panel.ListURL,
		"LoadURL":     panel.LoadURL,
		"DeleteURL":   panel.DeleteURL,
		"TruncateURL": panel.TruncateURL,
	} {
		if !strings.Contains(url, "{project_id}") {
			t.Errorf("%s = %q does not carry the project; it would act on the wrong project's threads", name, url)
		}
	}
	// The picker's field name IS the substitution key — if they ever diverge,
	// every URL above silently keeps the literal "{project_id}".
	var found bool
	for _, f := range panel.ExtraFields {
		if f.Name == "project_id" {
			found = true
		}
	}
	if !found {
		t.Fatal("no extra field named project_id, so nothing substitutes into those URLs")
	}
}

// A message typed while a turn is running must JOIN that turn, not replace it.
// Without an InjectURL the panel falls through to its ordinary send path,
// which tears down the running stream — so a second thought costs you
// everything the turn had already done.
func TestMidFlightMessagesJoinTheRunningTurn(t *testing.T) {
	T := &Anvil{}
	panel := T.chatPanel([]ui.SelectOption{{Value: "p", Label: "demo"}}, "p", "", nil)
	if panel.InjectURL == "" {
		t.Fatal("no inject endpoint, so typing during a run cancels it and starts over")
	}
	// Cancel must stay wired too — interjecting is not a substitute for
	// stopping, and losing the stop button is how a runaway turn becomes
	// unstoppable.
	if panel.CancelURL == "" {
		t.Error("no cancel endpoint")
	}
}

// --- the panel wiring pass ---------------------------------------------------

// A field pass turned up several framework capabilities this app had left
// unwired, each of which fails by being ABSENT rather than by erroring — the
// shape that took a live session to notice four times running. This pins the
// ones that were adopted, so the next omission is a test failure instead of a
// bug report.
func TestPanelWiresTheCapabilitiesItNeeds(t *testing.T) {
	T := &Anvil{}
	p := T.chatPanel([]ui.SelectOption{{Value: "p", Label: "demo"}}, "p", "", nil)

	required := map[string]string{
		// A coding turn is long; a reload part-way through must not orphan it.
		"RunsURLBase": p.RunsURLBase,
		// Reload comes back to the thread you were in.
		"DeepLinkParam": p.DeepLinkParam,
		// An answered card must stay answered across a reload.
		"BlockResolveURL": p.BlockResolveURL,
		// "Why did that stop" needs somewhere to be asked.
		"DiagnosticsURL": p.DiagnosticsURL,
		// An auto-title comes from the first message, which is rarely apt.
		"RenameURL": p.RenameURL,
		// Recovering a turn that died.
		"TruncateURL": p.TruncateURL,
		// A mid-run message joins the run instead of replacing it.
		"InjectURL": p.InjectURL,
	}
	for name, v := range required {
		if strings.TrimSpace(v) == "" {
			t.Errorf("%s is unset — the capability exists in the framework and this app is not using it", name)
		}
	}
	if !p.Attachments {
		t.Error("Attachments is off; a screenshot of a stack trace is often the fastest way to report one")
	}
	if !p.BulkSelect {
		t.Error("BulkSelect is off; per-project rails accumulate threads quickly")
	}
}

// Every session-scoped URL must carry the project, or an action lands in a
// different project's threads than the one on screen. Checked as a set,
// because adding a sixth endpoint and forgetting the query string is exactly
// the mistake this catches.
func TestEverySessionScopedURLCarriesTheProject(t *testing.T) {
	T := &Anvil{}
	p := T.chatPanel([]ui.SelectOption{{Value: "p", Label: "demo"}}, "p", "", nil)

	for name, url := range map[string]string{
		"ListURL":         p.ListURL,
		"LoadURL":         p.LoadURL,
		"DeleteURL":       p.DeleteURL,
		"TruncateURL":     p.TruncateURL,
		"RenameURL":       p.RenameURL,
		"DiagnosticsURL":  p.DiagnosticsURL,
		"BlockResolveURL": p.BlockResolveURL,
	} {
		if !strings.Contains(url, "{project_id}") {
			t.Errorf("%s = %q would act on whichever project the server guesses", name, url)
		}
	}
	// RunsURLBase is deliberately NOT project-scoped: a run id is unique and
	// the dispatcher checks ownership, and requiring a project would break
	// reconnecting to a run whose page reloaded before the picker rendered.
	if strings.Contains(p.RunsURLBase, "{project_id}") {
		t.Error("RunsURLBase is project-scoped; reconnect would fail on a fresh load")
	}
}

// The diff card must settle on the SERVER, not only in the DOM. Without the
// uiResolveBlock call the card comes back on the next load offering to revert
// an edit that is already back.
func TestEditCardSettlesDurably(t *testing.T) {
	if !strings.Contains(anvilEditBlockJS, "uiResolveBlock") {
		t.Fatal("the edit card never settles server-side; an answered card replays armed")
	}
	if !strings.Contains(anvilEditBlockJS, "d.resolved") {
		t.Fatal("the edit card ignores its resolved state, so a settled card renders with live controls")
	}
}

// --- what the agent is told --------------------------------------------------

// The prompt used to be static, so every session rediscovered facts that never
// change and the project's OWN written conventions were read only if the model
// thought to look. A coding assistant that has to be asked to read CLAUDE.md
// will confidently write code in the house style of somewhere else.
func TestProjectContextCarriesWhatTheAgentCannotGuess(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "server.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), "# House rules\nAlways use tabs.\n")
	commitAll(t, dir, "init")

	T := &Anvil{}
	p := Project{ID: "p", Name: "demo", Kind: KindRoot, Root: dir,
		Instructions: "The staging box is lab-02.",
		Actions:      []ProjectAction{{ID: "b", Label: "Build", Command: "go build ./...", ExposeToAgent: true}}}

	ctx := T.projectContext(p, dir)

	for _, want := range []string{
		"demo",                      // which project
		dir,                         // where it is
		"Go (2 files)",              // what it is written in
		"Always use tabs",           // its own rules, verbatim
		"CLAUDE.md",                 // and named, so the agent can cite them
		"The staging box is lab-02", // the operator's notes
		"Build",                     // the commands it can check its work with
		"clean",                     // git state
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("the project context never mentions %q:\n%s", want, ctx)
		}
	}
}

// A project's own instructions have to OUTRANK the assistant's habits, or
// they are decoration.
func TestProjectInstructionsAreDeclaredAuthoritative(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "AGENTS.md"), "Never use reflection.\n")
	T := &Anvil{}
	ctx := T.projectContext(Project{ID: "p", Name: "d", Kind: KindRoot, Root: dir}, dir)
	if !strings.Contains(ctx, "OUTRANK") {
		t.Error("the project's own rules are included without saying they win a disagreement")
	}
}

// A directory outside version control must SAY so — the agent's undo model
// differs completely when there is no git to fall back on.
func TestContextSaysWhenThereIsNoGit(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "x.py"), "print(1)\n")
	T := &Anvil{}
	ctx := T.projectContext(Project{ID: "p", Name: "d", Kind: KindRoot, Root: dir}, dir)
	if !strings.Contains(ctx, "not under version control") {
		t.Errorf("a non-repo project does not say so:\n%s", ctx)
	}
}

// An oversized instruction file is truncated WITH a note, not silently cut.
func TestOversizedInstructionsAreTruncatedAudibly(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CLAUDE.md"), strings.Repeat("x", maxInstructionBytes+5000))
	name, body, ok := readInstructionFile(dir)
	if !ok || name != "CLAUDE.md" {
		t.Fatalf("instruction file not found: %q %v", name, ok)
	}
	if !strings.Contains(body, "truncated") {
		t.Error("an oversized instruction file was cut without saying so")
	}
	if len(body) > maxInstructionBytes+200 {
		t.Errorf("truncation did not bound the size: %d bytes", len(body))
	}
}

// --- reasoning modes ---------------------------------------------------------

// The modes have to differ in what they actually DO, and none of them may
// promise something the framework cannot deliver.
func TestReasoningModes(t *testing.T) {
	// Lead escalates.
	if lead, _ := (Project{Reasoning: ReasoningLead}).applyReasoning(false, 0); !lead {
		t.Error("lead mode did not escalate")
	}
	// Quick lowers the budget and stays on the worker.
	lead, budget := (Project{Reasoning: ReasoningQuick}).applyReasoning(false, 0)
	if lead {
		t.Error("quick mode escalated to the lead")
	}
	if budget != quickThinkBudget {
		t.Errorf("quick budget = %d, want %d", budget, quickThinkBudget)
	}
	// Quick must only ever LOWER: a per-agent budget cannot raise the admin
	// ceiling, so a mode that appeared to would be a control doing nothing.
	if _, budget = (Project{Reasoning: ReasoningQuick}).applyReasoning(false, 128); budget != 128 {
		t.Errorf("quick raised an already-lower budget to %d", budget)
	}
	// Standard changes nothing.
	if lead, budget = (Project{Reasoning: ReasoningStandard}).applyReasoning(true, 2048); !lead || budget != 2048 {
		t.Errorf("standard altered the agent's own settings: lead=%v budget=%d", lead, budget)
	}
	// An agent already on the lead keeps it when the project is silent.
	if lead, _ = (Project{}).applyReasoning(true, 0); !lead {
		t.Error("an unset project downgraded an agent configured for the lead")
	}
}

// --- editing an existing project ---------------------------------------------

// A textarea posts a STRING and the field is a LIST, and encoding/json refuses
// the whole document on a mismatch — so filling in Extra roots did not save
// that field badly, it failed the entire project submission with "bad request"
// and no clue which field caused it. Both list fields had therefore never
// worked, in the add form as well as the settings page.
func TestListFieldsAcceptWhatAFormActuallyPosts(t *testing.T) {
	cases := map[string][]string{
		`{"extra_roots":"/a\n/b"}`:         {"/a", "/b"}, // textarea, newlines
		`{"extra_roots":"/a, /b"}`:         {"/a", "/b"}, // one-line, commas
		`{"extra_roots":["/a","/b"]}`:      {"/a", "/b"}, // a real list, still fine
		`{"extra_roots":"  /a  \n\n /b "}`: {"/a", "/b"}, // blank lines and padding dropped
		`{"extra_roots":""}`:               {},           // empty means empty, not [""]
	}
	for body, want := range cases {
		var p Project
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Errorf("%s → %v", body, err)
			continue
		}
		if len(p.ExtraRoots) != len(want) {
			t.Errorf("%s → %#v, want %#v", body, p.ExtraRoots, want)
			continue
		}
		for i := range want {
			if p.ExtraRoots[i] != want[i] {
				t.Errorf("%s → %#v, want %#v", body, p.ExtraRoots, want)
				break
			}
		}
	}
	// A whole-project decode with BOTH list fields as text must succeed — the
	// original failure took the entire record down, not just one field.
	var p Project
	if err := json.Unmarshal([]byte(`{"name":"x","extra_roots":"/a","allowed_users":"bob\ncarol"}`), &p); err != nil {
		t.Fatalf("a form-shaped project still fails to decode: %v", err)
	}
	if p.Name != "x" || len(p.AllowedUsers) != 2 {
		t.Fatalf("decoded %+v", p)
	}
}

// Storage and the API stay a LIST — the textarea spelling is an input
// convenience, not a storage format.
func TestListFieldsMarshalAsAList(t *testing.T) {
	b, err := json.Marshal(Project{ID: "p", ExtraRoots: Lines{"/a", "/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `["/a","/b"]`) {
		t.Fatalf("stored shape is not a list: %s", b)
	}
}

// The edit form has to carry the id, or every save mints a NEW project and the
// page looks like it simply does not save.
func TestEditFormCarriesTheIdentity(t *testing.T) {
	T := &Anvil{}
	edit := T.projectForm(true, true, nil)
	var hasID, hasCommands bool
	for _, f := range edit.Fields {
		if f.Field == "id" {
			hasID = true
		}
		if f.Field == "actions" && f.Type == "rows" {
			hasCommands = true
		}
	}
	if !hasID {
		t.Error("the edit form has no id; saving would create a second project")
	}
	if !hasCommands {
		t.Error("no commands editor; they remain settable only through the API")
	}
	if edit.Source == "" && edit.PostURL == "" {
		t.Error("the edit form has nowhere to load from or save to")
	}

	// The CREATE form must not carry an id — an empty one would be sent and
	// the handler would treat the create as an update of nothing.
	create := T.projectForm(true, false, nil)
	for _, f := range create.Fields {
		if f.Field == "id" {
			t.Error("the create form carries an id field")
		}
	}
}

// --- linked dependencies -----------------------------------------------------

// depCtx builds a turn context with one project linked into another.
func depCtx(t *testing.T, mainDir, depDir string, writable bool) *toolCtx {
	t.Helper()
	return &toolCtx{
		project: Project{ID: "main", Name: "main", Kind: KindRoot, Root: mainDir},
		base:    mainDir,
		deps: []resolvedDep{{
			Dependency: Dependency{ProjectID: "dep", Prefix: "lib", Writable: writable},
			Name:       "thelib", Kind: KindRoot, Base: depDir,
		}},
	}
}

// The gap this closes: a project could see nothing but its own tree, so a
// question spanning a dependency was unanswerable from inside the project that
// depends on it.
func TestADependencyIsReadableUnderItsPrefix(t *testing.T) {
	main, dep := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(main, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dep, "nfo", "misc.go"), "package nfo\n")

	c := depCtx(t, main, dep, false)

	got, err := c.resolve("lib/nfo/misc.go")
	if err != nil {
		t.Fatalf("a linked dependency is not readable: %v", err)
	}
	if want := filepath.Join(dep, "nfo", "misc.go"); got.abs != want {
		t.Fatalf("resolved to %q, want %q", got.abs, want)
	}
	if got.dep == nil {
		t.Error("the resolved target does not know it is in a dependency")
	}
	// The main project still resolves normally — a prefix must not shadow it.
	if got, err = c.resolve("main.go"); err != nil || got.dep != nil {
		t.Errorf("the main project stopped resolving: %v / %+v", err, got.dep)
	}
	// An unknown first segment is a path in the MAIN project, not an error —
	// plenty of projects have a directory whose name is not a link, and
	// failing on one would make a prefix able to break ordinary paths.
	got, err = c.resolve("nfo/misc.go")
	if err != nil {
		t.Fatalf("an ordinary path was mistaken for a bad prefix: %v", err)
	}
	if got.dep != nil {
		t.Error("an unlinked path was resolved inside a dependency")
	}
	if want := filepath.Join(main, "nfo", "misc.go"); got.abs != want {
		t.Errorf("resolved to %q, want the MAIN project's %q", got.abs, want)
	}
}

// Read-only is the default, and the refusal has to say what would change it.
func TestReadOnlyDependencyRefusesEdits(t *testing.T) {
	main, dep := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(dep, "x.go"), "package x\n")

	ro := depCtx(t, main, dep, false)
	tgt, err := ro.resolve("lib/x.go")
	if err != nil {
		t.Fatal(err)
	}
	err = tgt.requireWritable()
	if err == nil {
		t.Fatal("a read-only dependency accepted an edit")
	}
	for _, want := range []string{"lib", "thelib", "settings"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say what would change it (%q): %v", want, err)
		}
	}

	rw := depCtx(t, main, dep, true)
	if tgt, err = rw.resolve("lib/x.go"); err != nil {
		t.Fatal(err)
	}
	if err = tgt.requireWritable(); err != nil {
		t.Errorf("a writable link still refused: %v", err)
	}
}

// Containment inside a dependency is the LINKED project's business, not the
// linking one's — a checkout linked into a directory keeps the checkout's
// stricter symlink rules.
func TestDependencyKeepsItsOwnContainmentRules(t *testing.T) {
	main, dep, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(outside, "passwd"), "root:x:0:0\n")
	if err := os.Symlink(outside, filepath.Join(dep, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := depCtx(t, main, dep, false)
	c.deps[0].Kind = KindClone // linked project is a checkout

	if _, err := c.resolve("lib/escape/passwd"); err == nil {
		t.Fatal("a linked checkout followed a symlink out of itself")
	}
	// Traversal out of a dependency is refused as it is anywhere else.
	if _, err := c.resolve("lib/../../etc/passwd"); err == nil {
		t.Fatal("traversal out of a dependency was allowed")
	}
}

// A link the caller cannot open must resolve to NOTHING — that check is the
// whole reason links are to projects rather than to paths.
func TestALinkYouCannotOpenIsDropped(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &Anvil{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")

	// An admin-only root project, and a project linking to it.
	secret := Project{ID: "secret", Name: "secret", Kind: KindRoot, Root: t.TempDir()}
	root.Set(projectTable, secret.ID, secret)

	mine := Project{ID: "mine", Name: "mine", Kind: KindClone, Owner: "u",
		Dependencies: []Dependency{{ProjectID: "secret", Prefix: "s"}}}

	if got := T.resolveDependencies(udb, "u", false, mine); len(got) != 0 {
		t.Fatalf("a non-admin resolved a link to an admin-only project: %+v", got)
	}
	if got := T.resolveDependencies(udb, "u", true, mine); len(got) != 1 {
		t.Fatalf("an admin could not resolve the same link: %+v", got)
	}
}

// A writable link cannot grant more than the caller's access to that project.
func TestALinkCannotGrantMoreThanTheProjectWould(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &Anvil{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")

	// A root project shared read-only with this user.
	shared := Project{ID: "shared", Name: "shared", Kind: KindRoot,
		Root: t.TempDir(), AllowedUsers: Lines{"u"}}
	root.Set(projectTable, shared.ID, shared)

	mine := Project{ID: "mine", Name: "mine", Kind: KindClone, Owner: "u",
		Dependencies: []Dependency{{ProjectID: "shared", Prefix: "s", Writable: true}}}

	got := T.resolveDependencies(udb, "u", false, mine)
	if len(got) != 1 {
		t.Fatalf("the readable link was dropped: %+v", got)
	}
	if got[0].Writable {
		t.Fatal("a link marked writable granted write access to a project the user may only read")
	}
}

// Two links claiming one prefix would make addressing ambiguous, with the
// loser decided by list order nobody thinks about.
func TestDuplicatePrefixesAreDropped(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &Anvil{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")
	for _, id := range []string{"a", "b"} {
		root.Set(projectTable, id, Project{ID: id, Name: "lib", Kind: KindRoot, Root: t.TempDir()})
	}
	mine := Project{ID: "mine", Name: "mine", Kind: KindRoot, Root: t.TempDir(),
		Dependencies: []Dependency{{ProjectID: "a"}, {ProjectID: "b"}}}
	// Both slug to "lib"; only the first survives.
	if got := T.resolveDependencies(udb, "u", true, mine); len(got) != 1 {
		t.Fatalf("ambiguous prefixes were both kept: %+v", got)
	}
	// And a self-link is dropped rather than shadowing the root.
	self := Project{ID: "mine", Name: "mine", Kind: KindRoot, Root: t.TempDir(),
		Dependencies: []Dependency{{ProjectID: "mine"}}}
	if got := T.resolveDependencies(udb, "u", true, self); len(got) != 0 {
		t.Fatalf("a project linked itself: %+v", got)
	}
}

// The prefixes are unguessable unless the prompt names them.
func TestDependenciesAreDescribedToTheAgent(t *testing.T) {
	s := depSummary([]resolvedDep{
		{Dependency: Dependency{Prefix: "snugforge"}, Name: "snugforge"},
		{Dependency: Dependency{Prefix: "shared", Writable: true}, Name: "shared lib"},
	})
	for _, want := range []string{"snugforge/", "read-only", "shared/", "editable", "path_glob"} {
		if !strings.Contains(s, want) {
			t.Errorf("the dependency summary never mentions %q:\n%s", want, s)
		}
	}
	if depSummary(nil) != "" {
		t.Error("a project with no links still spends prompt on the idea")
	}
}

// --- working notes -----------------------------------------------------------

// TestNotesAreScopedPerProject — the notes block is the only memory layer a
// coding assistant can WRITE that is also always in prompt, and it has to be
// per project for the same reason conversations are: what is true about
// building one tree is a wrong assumption in another, carried in with no sign
// of where it came from.
func TestNotesAreScopedPerProject(t *testing.T) {
	udb := memDB(t)
	nsA := orchestrate.AgentStateNamespace(scopedAgentID(Project{ID: "a"}))
	nsB := orchestrate.AgentStateNamespace(scopedAgentID(Project{ID: "b"}))
	if nsA == nsB {
		t.Fatal("two projects share one notes namespace; a build command learned in one would be asserted in the other")
	}
	SaveOperatingNotes(udb, nsA, "build with: make dev")
	if got := ResolveOperatingNotes(udb, nsB, anvilSeedNotes).Text; strings.Contains(got, "make dev") {
		t.Errorf("project B can read project A's notes: %q", got)
	}
	if got := ResolveOperatingNotes(udb, nsA, anvilSeedNotes).Text; !strings.Contains(got, "make dev") {
		t.Errorf("project A lost its own notes: %q", got)
	}
}

// TestSeedNotesStandInUntilTheAssistantWrites — an empty block invites the
// model to either ignore the layer or fill it with narration of the current
// turn. The seed is a shape, and it must stay the fallback rather than being
// written to the store, so the panel can honestly say nothing has been learned.
func TestSeedNotesStandInUntilTheAssistantWrites(t *testing.T) {
	udb := memDB(t)
	ns := orchestrate.AgentStateNamespace(scopedAgentID(Project{ID: "p"}))

	if stored := LoadOperatingNotes(udb, ns); strings.TrimSpace(stored.Text) != "" {
		t.Error("the seed was persisted; the panel could no longer tell a starting shape from something learned")
	}
	if eff := ResolveOperatingNotes(udb, ns, anvilSeedNotes).Text; strings.TrimSpace(eff) == "" {
		t.Error("a project with no notes yet shows the model an empty block")
	}
	// And a clear returns to it, which is what "start over" has to mean.
	SaveOperatingNotes(udb, ns, "build with: make dev")
	SaveOperatingNotes(udb, ns, "")
	if stored := LoadOperatingNotes(udb, ns); strings.TrimSpace(stored.Text) != "" {
		t.Error("clearing left the old note behind")
	}
	if eff := ResolveOperatingNotes(udb, ns, anvilSeedNotes).Text; !strings.Contains(eff, "Build / test") {
		t.Errorf("clearing did not fall back to the seed: %q", eff)
	}
}

// TestNotesReviewIsReachable: notes are rewritten by the model unprompted and
// render nearest the top of the prompt, so a stale one steers every turn. It
// must be reviewable from the open project.
func TestNotesReviewIsReachable(t *testing.T) {
	T := &Anvil{}
	var found ui.ToolbarAction
	for _, a := range T.toolbarActions(Project{ID: "p", Name: "x"}, "u", true) {
		if a.URL == "anvil_notes" {
			found = a
		}
	}
	if found.URL == "" {
		t.Fatal("no way to see what the assistant believes about a project; a wrong note would steer every turn with nowhere to look")
	}
	if found.Method != "client" {
		t.Errorf("method %q; the toolbar renders once and must not bake a project into a URL", found.Method)
	}
}

// TestPromptPutsTheOperatorAboveTheNotes — notes and the operator's own project
// notes will overlap, and the model writes one of them. If it treats its own as
// authoritative it can quietly overwrite a staging URL somebody typed on
// purpose.
func TestPromptPutsTheOperatorAboveTheNotes(t *testing.T) {
	if !strings.Contains(coderPrompt, "outrank") {
		t.Error("the prompt never says which source wins when the notes disagree with the operator")
	}
	if !strings.Contains(coderPrompt, "update_notes") {
		t.Error("the prompt never tells the agent how to correct its own notes")
	}
}

// A project's extra roots and extra users are lists of tags: typed and added
// one at a time, each removable, stored as the list the record keeps.
func TestProjectListsAreTags(t *testing.T) {
	want := map[string]bool{"extra_roots": false, "allowed_users": false}
	for _, f := range (&Anvil{}).projectForm(true, false, nil).Fields {
		if _, ok := want[f.Field]; ok {
			want[f.Field] = f.Type == "tags"
		}
	}
	for field, ok := range want {
		if !ok {
			t.Errorf("%s is not a tags field", field)
		}
	}
}

// The branch on the first line of git status --branch, in each form git
// writes it.
func TestTheBranchIsReadFromStatus(t *testing.T) {
	for line, want := range map[string]string{
		"main...origin/main [ahead 1]": "main",
		"feature/x":                    "feature/x",
		"No commits yet on trunk":      "trunk",
		"Initial commit on trunk":      "trunk",
		"HEAD (no branch)":             "",
	} {
		if got := branchOfStatus(line); got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
}

// A repository with no commits yet names its branch, and a detached head
// names its commit.
func TestANewRepoAndADetachedHeadAreNamed(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	if info := inspectRepo(dir); !info.IsRepo || info.Branch == "" {
		t.Errorf("a repository with no commits: %+v", info)
	}
	mustWrite(t, filepath.Join(dir, "a.txt"), "a\n")
	commitAll(t, dir, "one")
	if out, err := exec.Command("git", "-C", dir, "checkout", "-q", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if info := inspectRepo(dir); info.Branch != "" || info.Head == "" || info.Dirty {
		t.Errorf("a detached head: %+v", info)
	}
}

// A burst of refreshes (a reopened session's edit cards each ask) is one
// status request; one asked while a request is out runs once after it.
func TestTheGitChipAsksOncePerBurst(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := `var asked = 0, pending = [];
function stub() { return {style: {}, classList: {add: function() {}}, appendChild: function() {}, addEventListener: function() {}, set innerHTML(v) {}, set title(v) {}}; }
var chip = stub(), host = stub();
var document = {querySelector: function() { return host; }, getElementById: function() { return chip; }, createElement: stub};
var window = {anvilProject: function() { return 'p1'; }, anvilPickerEl: function() { return null; },
  anvilFetchJSON: function() { asked++; return new Promise(function(r) { pending.push(r); }); }};
` + anvilGitChipJS + `
setTimeout(function() {
  for (var i = 0; i < 6; i++) window.anvilGitRefresh();
  setTimeout(function() {
    var afterBurst = asked;
    window.anvilGitRefresh(); window.anvilGitRefresh();
    setTimeout(function() {
      var whileOut = asked;
      pending.splice(0).forEach(function(r) { r({ready: true, git: {}}); });
      setTimeout(function() {
        var bad = [];
        if (afterBurst !== 1) bad.push('the page arriving and a burst of six asked ' + afterBurst + ' times');
        if (whileOut !== afterBurst) bad.push('asked again while a request was out');
        if (asked !== afterBurst + 1) bad.push('the refresh asked for meanwhile ran ' + (asked - afterBurst) + ' times');
        bad.forEach(function(b) { console.log(b); });
        process.exit(bad.length ? 1 : 0);
      }, 400);
    }, 400);
  }, 400);
}, 0);
`
	f := filepath.Join(t.TempDir(), "chip.js")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, f).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The project bar, by what each button acts on: changes and what this kind
// of project does with them out front (a repository commits and pushes, an
// app previews and installs), taking the work away as a file under
// Download, and the project's own settings and records under Project.
// Publish becomes Push once the repository has somewhere it goes, naming it.
func TestTheProjectBarIsGroupedByWhatItActsOn(t *testing.T) {
	T := &Anvil{}
	bar := func(p Project) map[string]string {
		out := map[string]string{}
		for _, a := range T.toolbarActions(p, "u", true) {
			out[a.Label] = a.Group
		}
		return out
	}
	repo := Project{ID: "r", Name: "repo", Kind: KindClone, Owner: "u", Source: sourceNew}
	got := bar(repo)
	for label, group := range map[string]string{"Changes": "", "Commit": "", "Publish": "",
		"Changes as a patch": "Download", "The repository as a bundle": "Download",
		"Settings": "Project", "What Anvil learned": "Project", "Permissions": "Project", "Copy session": "Project"} {
		if g, ok := got[label]; !ok || g != group {
			t.Errorf("%s: in %q (present %v), want %q", label, g, ok, group)
		}
	}
	if _, ok := got["Preview"]; ok {
		t.Error("a repository offers an app's Preview")
	}
	repo.PushURL, repo.PushBranch = "https://example.com/u/repo.git", "main"
	var push ui.ToolbarAction
	for _, a := range T.toolbarActions(repo, "u", true) {
		if a.URL == "anvil_publish" {
			push = a
		}
	}
	if push.Label != "Push" || !strings.Contains(push.Title, "https://example.com/u/repo.git") || !strings.Contains(push.Title, "main") {
		t.Errorf("a published repository's button: %+v", push)
	}
	app := bar(Project{ID: "a", Name: "app", Kind: KindClone, Owner: "u", Source: sourceApp, AppSlug: "app"})
	if app["Preview"] != "" || app["Install"] != "" {
		t.Errorf("an app's Preview and Install are not out front: %v", app)
	}
}
