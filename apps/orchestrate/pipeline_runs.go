// Orchestrate's half of the pipeline run surface.
//
// The store, the SSE bridge, the transcript, and the panel protocol are in
// core (core/pipeline_runs.go) — none of that knows what an agent is. What
// stays here is exactly what does: the dispatch hook that sends an agent stage
// to RunAgentSync, and the tool catalog a page-launched run inherits.
//
// Routes remain orchestrate's too (/api/pipelines/{id}/…, see pipelines_http.go)
// and the host-app surface it exposes for customapps (PublicHandlePipeline).
package orchestrate

import (
	"context"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// pipelineRunSurface assembles what core needs to serve one pipeline's runs
// for this user: orchestrate's own store, the agent-dispatch hook, and the
// catalog the definition's declared tool names resolve against.
func (T *OrchestrateApp) pipelineRunSurface(ctx context.Context, user string, def PipelineDef) PipelineRunSurface {
	return PipelineRunSurface{
		DB:   T.DB,
		User: user,
		Def:  def,
		// The one genuinely agent-shaped thing in a run. A pipeline with no
		// agent stages never calls it, which is why core can take it as a hook
		// and stay ignorant of the whole concept.
		Dispatch: func(ctx context.Context, agentID, stageInput string) (string, error) {
			// A panel's voices are agent names OR plain roles, and the
			// interpreter cannot tell which — so say so here, where the
			// agent store is. Anything that does resolve dispatches as
			// before; a name that does not comes back as ErrNoSuchAgent and
			// the stage answers it as a role on the worker.
			if _, found := findAgentByNameOrID(UserDB(T.DB, user), user, agentID); !found {
				return "", ErrNoSuchAgent
			}
			return T.RunAgentSync(ctx, user, user, agentID, stageInput)
		},
		Tools:   T.pipelineStandaloneTools(ctx, user, def),
		Timeout: knowledgeIngestTimeout() * 8,
	}
}

// handlePipelineRuns serves the panel protocol for one of the caller's own
// pipelines, under /api/pipelines/{id}/…
func (T *OrchestrateApp) handlePipelineRuns(w http.ResponseWriter, r *http.Request, user string, def PipelineDef, sub string) {
	T.ServePipelineRuns(w, r, T.pipelineRunSurface(r.Context(), user, def), sub)
}

// pipelineStandaloneTools builds the tool catalog for a run with no calling
// agent behind it — a page launching its own pipeline, rather than an agent
// invoking one of its attached ones.
//
// Those two entries used to be very different runs of the same recipe. An
// agent-invoked pipeline inherits that agent's resolved catalog, so a stage
// declaring tools:["web_search"] gets it; a page-launched one inherited nil,
// and resolveStageTools filters the declared names against the inherited pool
// — an empty pool matches nothing. The stage then took runWorkerStage's
// tool-less "cheap path" and answered from the model alone. Nothing failed:
// research came back fluent, sourceless and wrong, and a tool stage reported
// "the caller supplied no tool catalog" for a tool the user plainly owns.
//
// So the DEFINITION supplies the pool here: every name any stage declares
// (including a tool stage's own Tool and the stages nested in a loop Body),
// resolved for this user. That keeps the per-stage contract exactly as
// authored — a stage that declares nothing still gets nothing, deliberately —
// while making a declared name mean the same thing from either entry point.
// Resolution is per-name so one stale name costs its own stage's tool, not the
// whole run's catalog.
func (T *OrchestrateApp) pipelineStandaloneTools(ctx context.Context, user string, def PipelineDef) []AgentToolDef {
	ordered := pipelineDeclaredToolNames(def)
	if len(ordered) == 0 {
		return nil
	}
	// Username + Ctx, mirroring the attached-pipeline path: per-user tools (an
	// OAuth-backed MCP tool, a credentialed fetch) resolve THIS user's token.
	// No live callbacks — there is no conversation to raise a Connect prompt
	// into, so an unauthorized tool fails its stage rather than prompting.
	sess := &ToolSession{Username: user, Ctx: ctx}
	var out []AgentToolDef
	var missing []string
	for _, n := range ordered {
		td, err := GetAgentToolsWithSession(sess, n)
		if err != nil || len(td) == 0 {
			missing = append(missing, n)
			continue
		}
		out = append(out, td[0])
	}
	if len(missing) > 0 {
		Log("[orchestrate.pipelines] run of %q: %d declared tool(s) did not resolve for user=%q: %s",
			def.Name, len(missing), user, strings.Join(missing, ", "))
	}
	return out
}

// pipelineDeclaredToolNames is every tool name a definition asks for, sorted:
// each stage's Tools, a tool stage's own Tool, and the same for the stages
// nested in a loop Body — a loop is where the tool-calling stages of a
// refinement pass usually live, so missing them would leave exactly the
// iterative pipelines tool-less.
//
// Sorted rather than map-ranged so a run's catalog is built in a stable order:
// the same definition should produce the same catalog every time, which is the
// difference between a reproducible run and one that varies by map seed.
func pipelineDeclaredToolNames(def PipelineDef) []string {
	names := map[string]bool{}
	var walk func(stages []PipelineStage)
	walk = func(stages []PipelineStage) {
		for _, s := range stages {
			for _, n := range s.Tools {
				if n = strings.TrimSpace(n); n != "" {
					names[n] = true
				}
			}
			if n := strings.TrimSpace(s.Tool); n != "" {
				names[n] = true
			}
			walk(s.Body)
		}
	}
	walk(def.Stages)
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
