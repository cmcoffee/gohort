package orchestrate

// What an app ACTUALLY contains, rendered for the author about to describe it.
//
// Three builds in a row ended with a summary promising something absent: a
// history table nothing wrote to, runs "saved" to a store never touched, and a
// save button on an app with no actions at all. None were verification
// failures — every check passed, because the page was fine. They were
// summaries written from intent instead of from the stored spec.
//
// A prompt rule asking the author to go and re-read what it saved is a step
// that gets skipped by a model that feels finished. So the save and the verify
// hand it back the inventory unprompted, in the result it is already reading.
// Then a claim of a button is contradicted by the line directly above it,
// rather than by a user opening the app tomorrow.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appInventoryLine lists the app's stored parts and forbids going beyond them.
// Derived from the spec, so it cannot be optimistic.
func (t *chatTurn) appInventoryLine(spec AppSpec) string {
	var parts []string

	// Sections, by kind, in page order, with a count when repeated.
	secs := appSpecSections(spec)
	var kinds []string
	seen := map[string]int{}
	for _, m := range secs {
		k := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
		if k == "" {
			continue
		}
		if seen[k] == 0 {
			kinds = append(kinds, k)
		}
		seen[k]++
	}
	var rendered []string
	for _, k := range kinds {
		if seen[k] > 1 {
			rendered = append(rendered, fmt.Sprintf("%s×%d", k, seen[k]))
			continue
		}
		rendered = append(rendered, k)
	}
	if len(rendered) > 0 {
		parts = append(parts, "sections: "+strings.Join(rendered, ", "))
	} else {
		parts = append(parts, "NO sections")
	}

	// Buttons are the single most over-claimed thing, so name them or say
	// plainly that there are none.
	if len(spec.Actions) > 0 {
		labels := make([]string, 0, len(spec.Actions))
		for _, a := range spec.Actions {
			labels = append(labels, strconv.Quote(firstNonEmptyStr(a.Label, a.Name)))
		}
		sort.Strings(labels)
		parts = append(parts, "action buttons: "+strings.Join(labels, ", "))
	} else {
		parts = append(parts, "NO action buttons")
	}

	if n := len(spec.DataSources); n > 0 {
		parts = append(parts, fmt.Sprintf("%d data source(s)", n))
	} else {
		parts = append(parts, "no data sources")
	}
	// The pipeline by SHAPE, not by id. A bare UUID told the author nothing,
	// and the claim a summary makes about a pipeline is never "it is bound" —
	// it is "it runs five passes". Stage kinds are what makes that checkable:
	// one tool stage cannot be five rounds of anything, and an app whose whole
	// pipeline is a single wrapped tool is the shape that shipped a stub.
	if id := strings.TrimSpace(spec.PipelineID); id != "" {
		def, ok := t.app.LookupAppPipeline(t.user, id)
		if ok {
			kinds := make([]string, 0, len(def.Stages))
			for _, st := range def.Stages {
				k := string(st.Kind)
				if k == "" {
					k = "worker"
				}
				if st.Kind == StageLoop && st.Count > 0 {
					k = fmt.Sprintf("loop×%d", st.Count)
				}
				kinds = append(kinds, k)
			}
			parts = append(parts, fmt.Sprintf("pipeline: %q — %d stage(s): %s",
				def.Name, len(def.Stages), strings.Join(kinds, " → ")))
		} else {
			parts = append(parts, "pipeline: "+id+" (DOES NOT RESOLVE)")
		}
	}
	if id := strings.TrimSpace(spec.AgentID); id != "" {
		parts = append(parts, "agent: "+id)
	}

	return "STORED — " + strings.Join(parts, "; ") + ".\nDescribe ONLY what this line contains. If the user asked for something that is not in it, either add it now or SAY it is missing; a button, a saved history, or a table you describe and did not build is the first thing they will go looking for. The pipeline's stage list is part of this: do not describe rounds, passes or steps the stages do not actually perform."
}
