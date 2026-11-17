package orchestrate

import (
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func grantDB(t *testing.T) Database {
	t.Helper()
	return UserDB(&DBase{Store: kvlite.MemStore()}, "u")
}

// The reason the whole mechanism exists: the second build in an
// edit-build-fix loop must not ask the question the first one already
// answered.
func TestAGrantAnswersTheNextCallSilently(t *testing.T) {
	udb := grantDB(t)
	saveToolGrant(udb, ToolGrant{Scope: "proj-1", Tool: "run", Prefix: "go build"})

	if _, ok := findToolGrant(udb, "proj-1", "run", "go build ./..."); !ok {
		t.Fatal("the command the grant was made for still asks")
	}
	// The point of a prefix over an exact match: the next iteration is
	// rarely the identical command.
	if _, ok := findToolGrant(udb, "proj-1", "run", "go build ./apps/anvil/"); !ok {
		t.Fatal("a sibling command under the same verb still asks")
	}
	if _, ok := findToolGrant(udb, "proj-1", "run", "go test ./..."); ok {
		t.Fatal("a grant for one verb covered a different one")
	}
}

// A grant is scoped, and a scope is the app's way of saying "these are not the
// same situation". Allowing a build in a throwaway checkout must not allow one
// in the tree the server runs from.
func TestGrantsDoNotCrossScopes(t *testing.T) {
	udb := grantDB(t)
	saveToolGrant(udb, ToolGrant{Scope: "sandbox", Tool: "run", Prefix: "go build"})

	if _, ok := findToolGrant(udb, "sandbox", "run", "go build ./..."); !ok {
		t.Fatal("the granting scope does not honor its own grant")
	}
	if _, ok := findToolGrant(udb, "the-real-tree", "run", "go build ./..."); ok {
		t.Fatal("a grant leaked into another scope")
	}
}

// A chained command must not be answered by a grant made for its first
// clause. This is the same property core pins, checked here through the
// STORE, because that is the path a real call takes.
func TestChainedCommandIsNotCoveredByAGrant(t *testing.T) {
	udb := grantDB(t)
	saveToolGrant(udb, ToolGrant{Scope: "proj-1", Tool: "run", Prefix: "go build"})

	if _, ok := findToolGrant(udb, "proj-1", "run", "go build ./... ; rm -rf /"); ok {
		t.Fatal("a chained command rode in on a grant for its first clause")
	}
}

// A tool-wide grant (no prefix) covers the tool within its scope — correct
// only for a tool with no varying argument, which is why the prefix-less form
// exists at all.
func TestToolWideGrantCoversTheTool(t *testing.T) {
	udb := grantDB(t)
	saveToolGrant(udb, ToolGrant{Scope: "proj-1", Tool: "build"})

	if _, ok := findToolGrant(udb, "proj-1", "build", ""); !ok {
		t.Fatal("a tool-wide grant does not cover its own tool")
	}
	if _, ok := findToolGrant(udb, "proj-1", "deploy", ""); ok {
		t.Fatal("a tool-wide grant covered a different tool")
	}
}

// Clicking "always" twice on the same command should leave ONE entry saying
// what you allowed. A list that repeats itself is one nobody audits.
func TestSavingAbsorbsRedundantGrants(t *testing.T) {
	udb := grantDB(t)
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "go build"})
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "go build"})
	if got := len(listToolGrants(udb, "p")); got != 1 {
		t.Fatalf("the same grant twice left %d entries, want 1", got)
	}

	// A BROADER grant absorbs the narrower ones it makes unreachable —
	// otherwise the manager shows entries that can never be the reason a
	// call went through.
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "go build extra"})
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "go"})
	got := listToolGrants(udb, "p")
	if len(got) != 1 || got[0].Prefix != "go" {
		t.Fatalf("a broader grant did not absorb its narrower ones: %+v", got)
	}

	// A tool-wide grant absorbs every prefix under it.
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run"})
	if got := listToolGrants(udb, "p"); len(got) != 1 || got[0].Prefix != "" {
		t.Fatalf("a tool-wide grant did not absorb its prefixes: %+v", got)
	}
}

// Revoking has to actually stop answering calls — a Permissions screen whose
// Revoke button leaves the grant in force is worse than no screen.
func TestRevokeStopsAnsweringCalls(t *testing.T) {
	udb := grantDB(t)
	g := saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "go build"})
	if _, ok := findToolGrant(udb, "p", "run", "go build ./..."); !ok {
		t.Fatal("the grant never took effect")
	}
	if !deleteToolGrant(udb, g.ID) {
		t.Fatal("revoke reported failure")
	}
	if _, ok := findToolGrant(udb, "p", "run", "go build ./..."); ok {
		t.Fatal("a revoked grant still answers calls")
	}
}

// The confirm hook reads its grant argument back out of the rendered argument
// preview, so a value it cannot find must yield "" — which makes the call
// ungrantable and therefore asked about, the safe direction for a failed parse.
func TestArgFromPreview(t *testing.T) {
	preview := "command: go build ./...\npath: main.go"
	if got := argFromPreview(preview, "command"); got != "go build ./..." {
		t.Errorf("command = %q", got)
	}
	if got := argFromPreview(preview, "path"); got != "main.go" {
		t.Errorf("path = %q", got)
	}
	if got := argFromPreview(preview, "absent"); got != "" {
		t.Errorf("a missing argument yielded %q, want empty", got)
	}
	if got := argFromPreview(preview, ""); got != "" {
		t.Errorf("an unnamed argument yielded %q, want empty", got)
	}
}

// A chained line runs unasked when every command in it is covered, as Claude
// Code checks one; a cd needs no grant of its own. One part not covered, a
// part only the first clause's grant reaches, or a line that cannot be split
// with certainty, and it is asked about.
func TestAChainedLineIsAnsweredWhenEveryPartIs(t *testing.T) {
	udb := grantDB(t)
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "go test"})
	saveToolGrant(udb, ToolGrant{Scope: "p", Tool: "run", Prefix: "tail"})

	g, ok := compoundGrant(udb, "p", "run", "cd sub && go test ./... 2>&1 | tail -20")
	if !ok || g.Prefix != "go test, tail" {
		t.Fatalf("a line of granted commands asked: %+v %v", g, ok)
	}
	for _, cmd := range []string{
		"go test ./... ; rm -rf /",
		"go test ./... | grep FAIL",
		"go test $(ls) | tail",
		"cd sub",
		"cd sub && cd other",
		"go test ./... | tail > out.txt",
	} {
		if g, ok := compoundGrant(udb, "p", "run", cmd); ok {
			t.Errorf("%q ran on %q", cmd, g.Prefix)
		}
	}
	if _, ok := compoundGrant(udb, "elsewhere", "run", "go test ./... | tail"); ok {
		t.Error("a chained line crossed scopes")
	}
}

// "Always" on a chained line's card remembers each command in it, so the next
// line built of them runs unasked, as Claude Code's does.
func TestAlwaysOnAChainedLineRemembersEachPart(t *testing.T) {
	udb := grantDB(t)
	turn := &chatTurn{user: "chainer", udb: udb, sse: &sseWriter{live: &syncBuf{}}}
	spec := &ToolConfirmation{Prompt: "Run a command?", Scope: "p", GrantArg: "command"}
	done := answerNextCard("chainer", "always")
	if !turn.confirmAppToolCall("run", spec, "command: cd sub && go build ./... 2>&1 | head -40") {
		t.Fatal("the card was not allowed")
	}
	<-done
	var prefixes []string
	for _, g := range listToolGrants(udb, "p") {
		prefixes = append(prefixes, g.Prefix)
	}
	sort.Strings(prefixes)
	if strings.Join(prefixes, ",") != "go build,head" {
		t.Fatalf("remembered %q", prefixes)
	}
	// The next line of the same commands asks nothing: no card is answered,
	// and it is allowed.
	if !turn.confirmAppToolCall("run", spec, "command: go build ./cmd/... | head -5") {
		t.Error("a line of remembered commands asked again")
	}
}

// A chained line splits into the commands it runs, so each can be checked
// against a grant; anything that cannot be split with certainty is not split
// at all, and is asked about whole.
func TestACompoundCommandSplitsIntoWhatItRuns(t *testing.T) {
	for cmd, want := range map[string][]string{
		"go build ./...":                               {"go build ./..."},
		"cd sub && go test ./...":                      {"cd sub", "go test ./..."},
		"go build ./... 2>&1 | head -50":               {"go build ./...", "head -50"},
		"go vet ./... ; go test ./... >/dev/null 2>&1": {"go vet ./...", "go test ./..."},
		"ls 2> /dev/null || echo none":                 {"ls", "echo none"},
		"grep -rn 'a|b' .":                             {"grep -rn 'a|b' ."},
		"ls > /dev/null; rm -rf /":                     {"ls", "rm -rf /"},
	} {
		got, ok := commandParts(cmd)
		if !ok || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("%q: %q %v, want %q", cmd, got, ok, want)
		}
	}
	for _, cmd := range []string{
		"go build $(cat x)", "echo `id`", "echo \"$HOME\"", "go build > out.txt", "cat < secrets",
		"sleep 5 &", "(cd x; make)", "{ ls; }", "go build;", "&& ls", "a 'unclosed", "ls\nrm -rf x",
		"ls >/dev/nullx", "echo a\\;b", "ls >> log.txt", "", "rm -rf $HOME", "go test ./... | tail -n $N",
	} {
		if got, ok := commandParts(cmd); ok {
			t.Errorf("%q split as %q; it should be asked about whole", cmd, got)
		}
	}
}

// A card for a chained line offers to remember each part's verb, once each,
// with a cd needing none; a part no grant can describe offers nothing.
func TestACompoundCommandOffersEachPartsPrefix(t *testing.T) {
	if got := grantPrefixesFor("cd sub && go test ./... 2>&1 | tail -20"); strings.Join(got, ",") != "go test,tail" {
		t.Errorf("prefixes: %q", got)
	}
	if got := grantPrefixesFor("go build ./... && go build ./cmd/..."); strings.Join(got, ",") != "go build" {
		t.Errorf("a verb named twice: %q", got)
	}
	if got := grantPrefixesFor("grep -rn 'a|b' . | head"); got != nil {
		t.Errorf("a part with a quoted separator was offered: %q", got)
	}
	if got := grantPrefixesFor("go build $(x)"); got != nil {
		t.Errorf("a substitution was offered: %q", got)
	}
	if !isDirChange("cd sub") || !isDirChange("cd") || isDirChange("cd a b") || isDirChange("cdx") {
		t.Error("isDirChange")
	}
}
