package orchestrate

import (
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
