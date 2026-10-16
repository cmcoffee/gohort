package orchestrate

// A tool made from a saved pipeline keeps its own copy, and the original is
// folded in (removed) when nothing else uses it. The tool side lives in
// tools/temptool; this is the half that knows what can use a pipeline.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// toolPipelineSource resolves a pipeline for a tool being made from it, from
// the turn's user's pipelines. Wired into ToolSession.PipelineSource.
func (t *chatTurn) toolPipelineSource(ref string) (PipelineDef, func() string, error) {
	return pipelineForTool(t.udb, t.user, ref)
}

// pipelineForTool finds the pipeline ref names (name first, then id or a
// share) and the fold that removes the original once a tool holds it.
func pipelineForTool(udb Database, user, ref string) (PipelineDef, func() string, error) {
	if udb == nil || user == "" {
		return PipelineDef{}, nil, fmt.Errorf("from_pipeline needs a signed-in session")
	}
	def, ok := findPipelineByNameOrID(udb, user, ref)
	if !ok {
		var names []string
		for _, d := range ListPipelineDefs(udb, user) {
			names = append(names, d.Name)
		}
		return PipelineDef{}, nil, fmt.Errorf("no pipeline %q. Your pipelines: %s", ref, strings.Join(names, ", "))
	}
	tp := toolPipelineFold{udb: udb, user: user, def: def}
	return def, tp.fold, nil
}

// toolPipelineFold removes the original of a pipeline a tool now carries.
type toolPipelineFold struct {
	udb  Database
	user string
	def  PipelineDef
}

// fold deletes the original when it is the user's own and nothing else uses
// it, and says what it did either way. Asked at fold time, not when the
// pipeline was read: the tool's save came in between.
func (tp toolPipelineFold) fold() string {
	name := tp.def.Name
	if tp.def.Owner != "" && tp.def.Owner != tp.user {
		return fmt.Sprintf("The pipeline %q belongs to %s, so the tool holds a copy and theirs stays.", name, tp.def.Owner)
	}
	if uses := pipelineUses(tp.udb, tp.user, tp.def); len(uses) > 0 {
		return fmt.Sprintf("The saved pipeline %q stays, because %s; the tool's copy is separate from it.", name, strings.Join(uses, "; "))
	}
	DeletePipelineDef(tp.udb, tp.def.ID)
	Log("[orchestrate.tool_pipeline] %s: pipeline %q (%s) folded into a tool and removed", tp.user, name, tp.def.ID)
	return fmt.Sprintf("The saved pipeline %q was folded into this tool and removed from your pipelines: nothing else used it.", name)
}

// pipelineUses says what, besides a tool, uses a pipeline: an agent that runs
// it or may dispatch it, an app, a schedule, a machine phase, or sharing.
// Empty means it can go. Anything this cannot see counts as a use, so a doubt
// keeps the pipeline.
func pipelineUses(udb Database, owner string, def PipelineDef) []string {
	var uses []string
	refers := func(ref string) bool {
		ref = strings.TrimSpace(ref)
		return ref != "" && (ref == def.ID || strings.EqualFold(ref, strings.TrimSpace(def.Name)))
	}
	if def.Global {
		uses = append(uses, "it is global (every agent of yours can run it)")
	}
	if def.Published || len(def.AllowedUsers) > 0 {
		uses = append(uses, "it is shared")
	}
	if def.OwningApp != "" {
		uses = append(uses, fmt.Sprintf("it belongs to the app %q", def.OwningApp))
	}
	if adb := agentStore(owner); adb != nil {
		for _, a := range listAgents(adb, owner) {
			for _, ref := range append(append([]string{}, a.AttachedPipelines...), a.AllowedDispatchTargets...) {
				if refers(ref) {
					uses = append(uses, fmt.Sprintf("the agent %q runs it", a.Name))
					break
				}
			}
		}
	} else {
		uses = append(uses, "your agents could not be checked")
	}
	for _, s := range ListAppSpecs(owner) {
		if refers(s.PipelineID) {
			uses = append(uses, fmt.Sprintf("the app %q runs it", s.Name))
		}
	}
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if refers(sa.PipelineID) {
			uses = append(uses, fmt.Sprintf("the schedule %q runs it", sa.Name))
		}
	}
	for _, m := range ListMachineDefs(udb, owner) {
		for _, ph := range m.Phases {
			if refers(ph.Pipeline) {
				uses = append(uses, fmt.Sprintf("the machine %q runs it", m.Name))
				break
			}
		}
	}
	return uses
}

// runToolPipeline runs the pipeline a tool carries, as an attached pipeline
// runs, under the same recursion cap as every other pipeline tool. Wired into
// ToolSession.PipelineRunner.
func (t *chatTurn) runToolPipeline(ctx context.Context, def PipelineDef, input string) (string, error) {
	if t.pipelineDepth >= maxPipelineDepth {
		return "", fmt.Errorf("pipeline recursion depth exceeded (limit %d): check that a pipeline tool isn't calling itself directly or transitively", maxPipelineDepth)
	}
	t.pipelineDepth++
	defer func() { t.pipelineDepth-- }()
	return t.runPipelineDefInline(def, input)
}
