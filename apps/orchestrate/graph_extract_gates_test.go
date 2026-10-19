package orchestrate

// Automatic extraction writes into an agent's graph only where inferred memory
// may accumulate, and files entities, not code. Both checks hold whatever the
// extractor returns.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

func TestGraphExtractionKeepsToTheMemorySwitches(t *testing.T) {
	for _, c := range []struct {
		name         string
		agent        AgentRecord
		keepsNothing bool
		want         bool
	}{
		{"an ordinary conversation", AgentRecord{}, false, true},
		{"an agent with inferred memory off", AgentRecord{DisableInferred: true}, false, false},
		{"an incognito session or a Clean turn", AgentRecord{}, true, false},
		{"both", AgentRecord{DisableInferred: true}, true, false},
	} {
		if got := graphExtractAllowed(c.agent, c.keepsNothing); got != c.want {
			t.Errorf("%s: allowed = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every place that runs an extraction asks the rule first, so a new caller
// cannot write into a graph the switches say keeps nothing.
func TestEveryExtractionCallerAsksTheRule(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	callers := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !strings.Contains(l, "maybeExtractGraph(") && !strings.Contains(l, "extractGraphFromFold(") {
				continue
			}
			callers++
			asked := false
			for j := i - 1; j >= 0 && j >= i-3; j-- {
				if strings.Contains(lines[j], "graphExtractAllowed(") {
					asked = true
				}
			}
			if !asked {
				t.Errorf("%s:%d runs an extraction without asking graphExtractAllowed first", f, i+1)
			}
		}
	}
	if callers < 2 {
		t.Errorf("found %d extraction callers, want the per-turn one and the fold one", callers)
	}
}

func TestCodeShapedNamesAreNotEntities(t *testing.T) {
	for _, code := range []string{"operator_tools.go", "main.go", "tune_graph_cap", "/api/send", "RunAgentLoop()", "x=1", "`ls -la`", `C:\temp`} {
		if !graphNameLooksLikeCode(code) {
			t.Errorf("%q should not be filed as an entity", code)
		}
	}
	for _, name := range []string{"Santa Cruz", "Hansel", "ARC Raiders", "Robin Vale", "Dr. Smith", "U.S.", "St.Louis", "Moltbook"} {
		if graphNameLooksLikeCode(name) {
			t.Errorf("%q is a name, and was taken for code", name)
		}
	}
}

func TestExtractionFilesEntitiesNotCode(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	ns := "test-ns"
	chat := fakeChat(`[
		{"subject":"Robin","subject_kind":"person","relation":"owns","object":"Hansel","object_kind":"thing"},
		{"subject":"Robin","subject_kind":"person","relation":"edited","object":"operator_tools.go","object_kind":"thing"},
		{"subject":"tune_graph_cap","subject_kind":"thing","relation":"limits","object":"Robin","object_kind":"person"}
	]`)
	if n := extractGraphFromText(db, ns, "Robin owns Hansel and edited operator_tools.go.", chat); n != 1 {
		t.Fatalf("want only the pet filed, %d edge(s) written", n)
	}
	if _, ok := FindGraphEntity(db, ns, "operator_tools.go"); ok {
		t.Error("a file name was filed as an entity")
	}
	if _, ok := FindGraphEntity(db, ns, "Hansel"); !ok {
		t.Error("a named pet was not filed")
	}
}
