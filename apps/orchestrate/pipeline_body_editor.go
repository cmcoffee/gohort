// Editing the stages INSIDE a stage.
//
// A loop repeats a body and a fanout may run one per item, and until now
// neither body could be authored on the page at all: applyStageEdit knew
// no "body" key, so bodies arrived only from the `pipeline` tool, an
// import, or a revise. The most interesting thing a pipeline can do was
// the one thing the editor could not show.
//
// A body stage is addressed by PATH — "outer.inner" — which is
// unambiguous because a stage name may not contain a dot (the same rule
// that makes {stage:a.b} readable). Everything else is the top-level
// editor: the same form, the same save endpoint, the same validator.
//
// Scoped operations rather than def-wide ones, and the reason is the rule
// the validator already enforces: a body stage's name means something
// only inside that body. A rename must rewrite its siblings' references
// and nothing else, and a removal must be refused by a sibling that reads
// it rather than by a stage outside that could not have.

package orchestrate

import (
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// stageSlot is where one stage lives: the list holding it, its index in
// that list, and the stage that owns the list when it is a body.
type stageSlot struct {
	list []PipelineStage
	idx  int // -1 when the path resolves to nothing
	// parent is a pointer INTO def.Stages, so writing through it lands in
	// the definition without an index to carry around.
	parent *PipelineStage // nil at top level
}

// found reports whether the path landed on a stage.
func (sl stageSlot) found() bool { return sl.idx >= 0 }

// stage returns the addressed stage.
func (sl stageSlot) stage() PipelineStage { return sl.list[sl.idx] }

// splitStagePath separates "outer.inner" into its parts. A path with no
// dot is a top-level stage and returns an empty parent.
func splitStagePath(path string) (parent, name string) {
	path = strings.TrimSpace(path)
	if i := strings.IndexByte(path, '.'); i >= 0 {
		return strings.TrimSpace(path[:i]), strings.TrimSpace(path[i+1:])
	}
	return "", path
}

// resolveStagePath locates a stage by path against a definition.
func resolveStagePath(def *PipelineDef, path string) stageSlot {
	parentName, name := splitStagePath(path)
	if parentName == "" {
		return stageSlot{list: def.Stages, idx: indexIn(def.Stages, name)}
	}
	pi := indexIn(def.Stages, parentName)
	if pi < 0 {
		return stageSlot{idx: -1}
	}
	parent := &def.Stages[pi]
	return stageSlot{list: parent.Body, idx: indexIn(parent.Body, name), parent: parent}
}

// resolveBodyOwner locates the stage a new body stage would be added to.
func resolveBodyOwner(def *PipelineDef, parentName string) (*PipelineStage, bool) {
	pi := indexIn(def.Stages, strings.TrimSpace(parentName))
	if pi < 0 {
		return nil, false
	}
	return &def.Stages[pi], true
}

func indexIn(stages []PipelineStage, name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return -1
	}
	for i, s := range stages {
		if strings.TrimSpace(s.Name) == name {
			return i
		}
	}
	return -1
}

// bodyScope wraps a body as a definition of its own, so a rename or a
// removal inside it reuses the rules core already wrote rather than a
// second, subtly different copy of them.
//
// This is the whole trick that keeps nested editing small: a body IS a
// list of stages, and every operation on a list of stages already exists.
func bodyScope(body []PipelineStage) PipelineDef { return PipelineDef{Stages: body} }

// renameInBody renames a body stage and rewrites what its SIBLINGS call
// it, leaving the rest of the pipeline alone. Nothing outside may name a
// body stage (Validate refuses it), so nothing outside can need rewriting.
func renameInBody(parent *PipelineStage, from, to string) {
	scoped := bodyScope(parent.Body)
	scoped.RenameStage(from, to)
	parent.Body = scoped.Stages
}

// removeFromBody drops a body stage, refused by a sibling that reads it.
func removeFromBody(parent *PipelineStage, name string) error {
	scoped := bodyScope(parent.Body)
	if err := scoped.RemoveStage(name); err != nil {
		return err
	}
	parent.Body = scoped.Stages
	return nil
}

// bodyKindOptions are the kinds a BODY stage may be.
//
// No loop and no fanout: bodies do not nest, and the validator refuses
// them. Offering a choice that gets refused on save is worse than not
// offering it, because the refusal arrives after somebody has written the
// prompt to go with it.
func bodyKindOptions() []ui.SelectOption {
	return []ui.SelectOption{
		{Value: "worker", Label: "Worker — one model call"},
		{Value: "agent", Label: "Agent — dispatch to one of your agents"},
		{Value: "machine", Label: "Machine — run a whole machine for this item"},
		{Value: "branch", Label: "Branch — read a bool and skip ahead (no model call)"},
		{Value: "tool", Label: "Tool — call a tool directly (no model, no tokens)"},
		{Value: "synthesize", Label: "Synthesize — combine what earlier stages produced"},
	}
}

// savePipelineBodyStage writes one stage inside a body: an edit when the
// path resolved to an existing one, an add when the form named a parent.
//
// Returns nil on success having written the reply, and a non-nil error
// having written the refusal — so the caller returns either way and never
// writes twice.
func (T *OrchestrateApp) savePipelineBodyStage(w http.ResponseWriter, udb Database, user string, def PipelineDef, slot stageSlot, parentRef string, stage PipelineStage) error {
	parent := slot.parent
	if parent == nil {
		var ok bool
		if parent, ok = resolveBodyOwner(&def, parentRef); !ok {
			http.Error(w, "no stage called "+strconv.Quote(parentRef)+" to add a step to — it was renamed or removed; reload the page",
				http.StatusNotFound)
			return Error("no parent")
		}
	}
	if parent.Kind != StageLoop && parent.Kind != StageFanout {
		http.Error(w, "stage "+strconv.Quote(parent.Name)+" has no body: only a loop or a fanout runs stages of its own",
			http.StatusBadRequest)
		return Error("not a body-bearing stage")
	}

	if slot.found() {
		_, oldName := splitStagePath(strings.TrimSpace(parent.Name) + "." + strings.TrimSpace(slot.stage().Name))
		if oldName != stage.Name {
			if indexIn(parent.Body, stage.Name) >= 0 {
				http.Error(w, "a step called "+strconv.Quote(stage.Name)+" already exists in this body", http.StatusBadRequest)
				return Error("duplicate")
			}
			parent.Body[slot.idx] = stage
			renameInBody(parent, oldName, stage.Name)
		} else {
			parent.Body[slot.idx] = stage
		}
	} else {
		if indexIn(parent.Body, stage.Name) >= 0 {
			http.Error(w, "a step called "+strconv.Quote(stage.Name)+" already exists in this body", http.StatusBadRequest)
			return Error("duplicate")
		}
		parent.Body = append(parent.Body, stage)
	}

	// The same gate every other pipeline door uses, and it matters more
	// here: the body rules (no nesting, a branch that may only skip
	// within the pass) are exactly what somebody authoring one will trip.
	if err := def.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return Error("invalid")
	}
	saved := SavePipelineDef(udb, def)
	path := strings.TrimSpace(parent.Name) + "." + strings.TrimSpace(stage.Name)
	Log("[orchestrate.pipelines] user=%q edited body stage %q of %q", user, path, saved.Name)
	writeJSON(w, map[string]any{"ok": true, "id": saved.ID, "name": path,
		"slug": ui.SectionSlug(bodyStageSectionTitle(parent.Name, stage.Name))})
	return nil
}
