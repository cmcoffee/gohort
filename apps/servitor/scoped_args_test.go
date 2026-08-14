package servitor

// The dispatch has to hand the CALLING agent to the path-scope check.
// Passing only the user is what let an agent nobody had linked a store to
// run a command against it — the tool's own gate (the appliance
// connection) is a different grant from the folder's.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestScopedArgsCarryTheCallingAgent(t *testing.T) {
	prevHolds := AgentHoldsReference
	t.Cleanup(func() { AgentHoldsReference = prevHolds })
	AgentHoldsReference = func(user, agentID, kind, itemID string) bool { return agentID == "linked" }

	var sawUser, sawName, sawValue string
	RegisterPathScope("testscope", PathScope{
		Resolve: func(u, n, v string) (string, error) {
			sawUser, sawName, sawValue = u, n, v
			return "/abs/" + v, nil
		},
	})
	// The gate only applies to kinds that are ALSO attachable sources —
	// otherwise there is nothing to attach and requiring it would break a
	// scope with no picker behind it.
	RegisterReferenceSource(scopeTestSource{})

	tool := ApplianceTool{
		Name:   "parse_bundle",
		Params: map[string]ToolParam{"dir": {Type: "string", PathScope: "testscope:bundles"}},
	}
	args := map[string]any{"dir": "scan-1", "other": "left alone"}

	out, err := resolveScopedArgs("alice", "linked", tool, args)
	if err != nil {
		t.Fatalf("linked agent refused: %v", err)
	}
	if out["dir"] != "/abs/scan-1" {
		t.Errorf("scoped arg not substituted: %v", out["dir"])
	}
	if out["other"] != "left alone" {
		t.Errorf("unscoped arg was touched: %v", out["other"])
	}
	// The caller's map is what gets logged and echoed back; rewriting it
	// in place would make the record disagree with what the model asked.
	if args["dir"] != "scan-1" {
		t.Errorf("the caller's map was mutated: %v", args["dir"])
	}
	if sawUser != "alice" || sawName != "bundles" || sawValue != "scan-1" {
		t.Errorf("resolver got (%q,%q,%q)", sawUser, sawName, sawValue)
	}

	if _, err := resolveScopedArgs("alice", "unlinked", tool, args); err == nil {
		t.Fatal("an unlinked agent resolved a scoped path")
	} else if !strings.Contains(err.Error(), "dir") {
		t.Errorf("the refusal should name the parameter, got %q", err)
	}
}

type scopeTestSource struct{}

func (scopeTestSource) Kind() string                                         { return "testscope" }
func (scopeTestSource) Label() string                                        { return "Test scope" }
func (scopeTestSource) List(string) []ReferenceItem                          { return nil }
func (scopeTestSource) Fetch(context.Context, string, string, string) string { return "" }

// The constraint has to be visible to the model that has to satisfy it.
// Without the names in the description the parameter reads "which bundle
// to parse", the scope is enforced somewhere the model cannot see, and
// the first call is a guess that gets refused.
func TestScopedParamDescriptionCarriesTheFolders(t *testing.T) {
	RegisterPathScope("listscope", PathScope{
		Resolve: func(u, n, v string) (string, error) { return "/abs/" + v, nil },
		Values:  func(u, n string) []string { return []string{"scan-b", "scan-a"} },
	})
	params := map[string]ToolParam{
		"dir":   {Type: "string", Description: "Which bundle to parse.", PathScope: "listscope:bundles"},
		"quiet": {Type: "boolean", Description: "Less output."},
	}
	got := describeScopedParams("u", params, map[string][]string{})

	if got["dir"].Description == params["dir"].Description {
		t.Fatal("the scoped parameter's description was not enriched")
	}
	for _, want := range []string{"Which bundle to parse", "scan-a", "scan-b"} {
		if !strings.Contains(got["dir"].Description, want) {
			t.Errorf("description missing %q: %s", want, got["dir"].Description)
		}
	}
	// Sorted by PathScopeChoices, so the list does not reshuffle between
	// turns and invalidate the prompt cache for no reason.
	if strings.Index(got["dir"].Description, "scan-a") > strings.Index(got["dir"].Description, "scan-b") {
		t.Error("folder list should be sorted")
	}
	// A snapshot that admits it is one. A model handed a list treats it as
	// exhaustive otherwise, and refuses the folder somebody just named.
	if !strings.Contains(got["dir"].Description, "still works") {
		t.Error("description should say a newer folder still resolves")
	}
	if got["quiet"].Description != "Less output." {
		t.Errorf("an unscoped parameter was rewritten: %q", got["quiet"].Description)
	}
	// The stored record must not be touched — it is the frozen tool, and
	// this text is one moment's listing of a directory.
	if params["dir"].Description != "Which bundle to parse." {
		t.Errorf("the stored params were mutated: %q", params["dir"].Description)
	}
	// Second call for the same root reuses the memo rather than re-listing
	// once per tool on the same machine.
	memo := map[string][]string{"listscope:bundles": {"cached-only"}}
	if d := describeScopedParams("u", params, memo)["dir"].Description; !strings.Contains(d, "cached-only") {
		t.Errorf("memo not consulted: %s", d)
	}
}

// An empty root is not "no valid values". A model told that concludes the
// tool is broken and stops; told what is true, it can say so.
func TestEmptyScopeSaysWhatIsActuallyTrue(t *testing.T) {
	hint := scopeHint(nil)
	if strings.Contains(strings.ToLower(hint), "no valid") {
		t.Errorf("hint reads as a broken tool: %s", hint)
	}
	if !strings.Contains(hint, "until a folder appears") {
		t.Errorf("hint should say what would change it: %s", hint)
	}
	// And a long list is capped, with the remainder acknowledged rather
	// than silently dropped.
	many := make([]string, maxListedFolders+7)
	for i := range many {
		many[i] = "f" + strconv.Itoa(i)
	}
	long := scopeHint(many)
	if !strings.Contains(long, "7 more not listed") {
		t.Errorf("truncation should say how many were dropped: %s", long)
	}
}
