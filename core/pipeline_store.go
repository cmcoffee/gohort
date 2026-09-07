package core

import (
	"sort"
	"time"
)

// --- storage (per-user) ---------------------------------------------

// SavePipelineDef writes a pipeline def to the user's store, minting
// an ID on first save and stamping Updated. Returns the saved record.
func SavePipelineDef(udb Database, d PipelineDef) PipelineDef {
	if udb == nil {
		return d
	}
	if d.ID == "" {
		d.ID = UUIDv4()
	}
	if d.Created.IsZero() {
		d.Created = time.Now()
	}
	d.Updated = time.Now()
	// Written in the new shape whatever came in: the legacy field is
	// cleared here, so a record migrates permanently the first time it is
	// saved rather than being folded on every read forever.
	normalizeStageThink(d.Stages)
	udb.Set(PipelineDefsTable, d.ID, d)
	// Every save path funnels through here — the HTTP editor, the pipeline
	// tool, revise, undo, import, duplicate — which is why the share index is
	// synced from a hook rather than at each of those call sites. A grant that
	// silently fails to register on one path is a grant the owner believes they
	// made.
	if PipelineSavedHook != nil {
		PipelineSavedHook(d)
	}
	return d
}

// PipelineSavedHook, when set by an app that keeps an index over pipelines, is
// called after every save with the stored record. Mirrors PipelineDeletedHook.
var PipelineSavedHook func(def PipelineDef)

// LoadPipelineDef reads a pipeline def by ID. Returns ok=false when
// absent or when the record's owner doesn't match (defensive — a
// guessed ID from another user's space doesn't resolve).
func LoadPipelineDef(udb Database, owner, id string) (PipelineDef, bool) {
	if udb == nil || id == "" {
		return PipelineDef{}, false
	}
	var d PipelineDef
	if !udb.Get(PipelineDefsTable, id, &d) {
		return PipelineDef{}, false
	}
	// Migrate on read: a record written before think became a string folds
	// its legacy field in here, once, and is written back in the new shape
	// the next time it is saved. Doing it at the door means nothing
	// downstream has to know there were ever two fields.
	normalizeStageThink(d.Stages)
	if owner != "" && d.Owner != "" && d.Owner != owner {
		return PipelineDef{}, false
	}
	return d, true
}

// ListPipelineDefs returns the user's pipeline defs, most-recently-
// updated first.
func ListPipelineDefs(udb Database, owner string) []PipelineDef {
	if udb == nil {
		return nil
	}
	var out []PipelineDef
	for _, k := range udb.Keys(PipelineDefsTable) {
		var d PipelineDef
		if !udb.Get(PipelineDefsTable, k, &d) {
			continue
		}
		if owner != "" && d.Owner != "" && d.Owner != owner {
			continue
		}
		normalizeStageThink(d.Stages)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// DeletePipelineDef removes a pipeline def by ID.
func DeletePipelineDef(udb Database, id string) {
	if udb == nil || id == "" {
		return
	}
	def, existed := LoadPipelineDef(udb, "", id)
	udb.Unset(PipelineDefsTable, id)
	// Tell whoever depends on it, the way deleting a credential does
	// (CredentialDeletedHook). A schedule can target a pipeline, and
	// without this its only notice is the next fire — the agent path
	// marks its schedules broken the moment the agent goes, and the two
	// should not differ on how long a dependent stays wrong.
	if existed && PipelineDeletedHook != nil {
		PipelineDeletedHook(def.Owner, def.ID, def.Name)
	}
}

// PipelineDeletedHook, when set by an app that owns dependents, is called
// after a pipeline is removed: owner, id, and the name it had (the name
// is gone from storage by then, and a reason reading "runs deleted
// pipeline \"\"" helps nobody).
var PipelineDeletedHook func(owner, id, name string)

// --- export / import (portable recipe) ------------------------------

// ExportPipeline returns a portable copy of a pipeline def with
// storage/identity metadata stripped — the shareable recipe. Marshal
// the result to JSON for a downloadable artifact.
//
// ID is the ONE identity field that DOES travel (same rule as
// collections): agents reference pipelines by ID (AttachedPipelines),
// so preserving it is what lets an agent+pipeline bundle land with its
// wiring intact. Owner and timestamps are reassigned on import, never
// travel.
//
// Note on portability: a recipe whose stages are all kind=worker is
// fully self-contained. Agent stages reference an agent by id/name;
// the "pipeline" artifact type (orchestrate) normalizes those refs to
// agent NAMES on export and folds the agents into the bundle — this
// plain export doesn't rewrite them, so it's on the importer to have
// the referenced agents.
func ExportPipeline(d PipelineDef) PipelineDef {
	d.Owner = ""
	d.Created = time.Time{}
	d.Updated = time.Time{}
	d.Global = false // scope is a local decision; imported pipelines land non-global
	// Names of users in the exporting deployment. In another one they are
	// somebody else or nobody, and an import that silently carried a grant to
	// a name that got reused is the worst possible way to find that out.
	d.AllowedUsers = nil
	// A recipe carries a pipeline, not its history — and an undo
	// snapshot would double every bundle for something the importer can
	// never take back.
	d.Previous = nil
	return d
}

// ImportPipeline takes a recipe (from ExportPipeline or an uploaded
// JSON file), assigns it to owner, validates it, and saves it to the
// user's store. Returns the saved def. The traveled ID is KEPT when
// it's free — it's what an agent in the same bundle references via
// AttachedPipelines — and reminted when it would collide, so importing
// the same recipe twice makes a copy instead of clobbering. Owner and
// timestamps are always the importer's.
func ImportPipeline(udb Database, owner string, recipe PipelineDef) (PipelineDef, error) {
	if recipe.ID != "" {
		if _, exists := LoadPipelineDef(udb, "", recipe.ID); exists {
			recipe.ID = ""
		}
	}
	recipe.Owner = owner
	recipe.Created = time.Time{}
	recipe.Updated = time.Time{}
	if err := recipe.Validate(); err != nil {
		return PipelineDef{}, err
	}
	return SavePipelineDef(udb, recipe), nil
}

// StarterPipeline is a small pipeline that RUNS, for somebody who
// clicked "new" rather than arriving with a design.
//
// The rules it has to teach are easier to read off something correct
// than out of a rejection: a later stage reads an earlier one by name,
// declaring output IS the structured-output mechanism (so no prompt
// here asks for JSON), and a stage that declares nothing is a prose
// stage, which is right for the one that answers. It uses no tools, so
// it runs in any deployment.
//
// Lives here rather than in a page's JavaScript so there is one copy
// and a test can prove the claim in this comment.
func StarterPipeline() PipelineDef {
	return PipelineDef{
		Name:        "New pipeline",
		Description: "What this pipeline is for, and when an agent should reach for it.",
		Stages: []PipelineStage{
			{
				Name:   "plan",
				Kind:   StageWorker,
				Prompt: "Work out what is actually being asked, and what a good answer would have to cover.",
				Output: []PipelineField{
					{Name: "focus", Type: FieldString, Required: true,
						Desc: "what the answer must actually address, in one sentence"},
				},
			},
			{
				Name:   "answer",
				Kind:   StageWorker,
				Prompt: "Answer the question, keeping to {stage:plan.focus} and saying plainly where you are unsure.",
			},
		},
	}
}
