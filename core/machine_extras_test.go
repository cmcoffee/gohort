package core

// The machine recipes shipped in extras/ are the first thing anyone
// pastes at /api/machines. A shipped example that 400s is worse than no
// example, so they are validated here rather than trusted.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtrasMachineRecipesValidate(t *testing.T) {
	paths, err := filepath.Glob("../extras/*.machine.json")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no machine recipes in extras/")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var def MachineDef
			if err := json.Unmarshal(raw, &def); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if err := def.Validate(); err != nil {
				t.Fatalf("does not validate:\n%v", err)
			}
			// A recipe with no description is a row in the picker that
			// says nothing about when to reach for it.
			if strings.TrimSpace(def.Description) == "" {
				t.Error("a shipped recipe should describe when to use it")
			}
			// And it should draw: the graph adapter is where a malformed
			// edge (a guard pointing nowhere, a router with no targets)
			// shows up as a picture nobody can read.
			if svg := def.Graph().SVG(nil); !strings.HasPrefix(svg, "<svg ") {
				t.Error("recipe does not render")
			}
		})
	}
}

// The troubleshooting recipe is St4's subject (docs/troubleshooting-machine.md),
// and its shape IS the experiment: gathering happens in the resident
// phase with the agent's own tools, NOT in a transient phase. If someone
// "fixes" it into Design A later, the test that follows should be the
// thing that makes them say so out loud.
func TestTroubleshootingRecipeIsDesignB(t *testing.T) {
	raw, err := os.ReadFile("../extras/troubleshooting.machine.json")
	if err != nil {
		t.Skip("recipe not present")
	}
	var def MachineDef
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("decode: %v", err)
	}

	work, ok := def.Phase("work")
	if !ok || !work.Resident {
		t.Fatal("the phase that gathers must be the RESIDENT one — that is the whole hypothesis")
	}
	// Design B's claim is that a resident phase with the agent's full
	// catalog can do the gathering, so the machine must not narrow it.
	if len(work.Tools) != 0 {
		t.Errorf("work must inherit the agent's whole catalog, got %v", work.Tools)
	}
	if strings.TrimSpace(work.Guard) == "" || work.GuardTo != "scope" {
		t.Error("a long investigation needs a way back out to re-scoping")
	}

	// The transient phases decide; they do not fetch. If either grows a
	// tool list, Design B has quietly become Design A.
	for _, name := range []string{"scope", "plan"} {
		p, ok := def.Phase(name)
		if !ok {
			t.Fatalf("missing phase %s", name)
		}
		if p.Resident {
			t.Errorf("%s should be transient", name)
		}
		if len(p.Tools) != 0 {
			t.Errorf("%s names tools, which transient phases cannot reach today (machineCatalog returns nil) — this is Design A wearing Design B's name", name)
		}
		if len(p.Output) == 0 {
			t.Errorf("%s must hand something forward, or it has done nothing", name)
		}
		// Both do genuine judgment, which is the case the think default
		// is wrong for. See the THINKING section in the machine tool help.
		if p.Think != "on" {
			t.Errorf("%s decides rather than transforms, so it should reason; got think=%q", name, p.Think)
		}
	}

	// scope routes: not every question needs the full plan step.
	scope, _ := def.Phase("scope")
	if scope.NextFrom == "" {
		t.Error("scope should be able to skip planning for a question that does not need it")
	}
}
