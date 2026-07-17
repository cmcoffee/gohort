// Attached pipelines as per-agent tools.
//
// AgentRecord.AttachedPipelines lists pipeline def IDs the agent can run.
// Each one surfaces as its OWN named, callable tool on the agent's
// catalog — a first-class capability of the agent, the way
// AttachedCollections are first-class reference corpora.
//
// Why a dedicated tool per pipeline (instead of leaning on the generic
// `pipeline` grouped tool's run action):
//   - The agent's prompt advertises "you can run X" by name, so the LLM
//     reaches for the right workflow for the agent's domain without
//     having to discover it through pipeline(action=list).
//   - A locked-down agent that lacks the authoring `pipeline` tool can
//     still RUN its attached pipelines — running is a capability, not an
//     authoring privilege.
//   - It keeps agent + its pipelines as a single attachable bundle
//     (export/import travels together).
//
// These are DIRECT tools (in the catalog from the start), not lazy
// load_tool entries: a pipeline tool's schema is one `input` string, so
// there's no schema-bloat reason to defer it, and the attachment is
// curated (the user deliberately bolted it on) — meant to be reached for.
// They run SYNC via runPipelineDefInline, same as pipeline(action=run).

package orchestrate

import (
	"errors"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// buildAttachedPipelineToolDefs returns one callable tool per pipeline
// in the agent's AttachedPipelines list. Missing/cross-owner pipeline
// IDs are skipped silently (the attachment outlived the def, or the
// def belongs to someone else). Tool names are de-duplicated so two
// pipelines that sanitize to the same handle don't shadow each other.
func (t *chatTurn) buildAttachedPipelineToolDefs() []AgentToolDef {
	if t == nil {
		return nil
	}
	// Effective set = this agent's explicit attachments UNION the owner's
	// GLOBAL pipelines, minus this agent's opt-outs (DisabledPipelines). The
	// global scope is the pipeline analogue of a user-wide tool pool: a
	// pipeline marked Global reaches every agent that hasn't denied it.
	ids := make([]string, 0, len(t.agent.AttachedPipelines))
	seenID := make(map[string]bool, len(t.agent.AttachedPipelines))
	for _, pid := range t.agent.AttachedPipelines {
		if !seenID[pid] {
			seenID[pid] = true
			ids = append(ids, pid)
		}
	}
	for _, d := range ListPipelineDefs(t.udb, t.user) {
		if !d.Global || seenID[d.ID] || containsString(t.agent.DisabledPipelines, d.ID) {
			continue
		}
		seenID[d.ID] = true
		ids = append(ids, d.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	out := make([]AgentToolDef, 0, len(ids))
	usedNames := make(map[string]bool, len(ids))
	for _, pid := range ids {
		def, ok := LoadPipelineDef(t.udb, t.user, pid)
		if !ok {
			continue
		}
		name := attachedPipelineToolName(def.Name, usedNames)
		usedNames[name] = true

		d := def // capture per-iteration for the closure
		desc := "Run the " + d.Name + " pipeline"
		if s := strings.TrimSpace(d.Description); s != "" {
			desc += " — " + s
		}
		desc += ". A saved multi-stage workflow; pass the starting input and it returns the final synthesized output."

		out = append(out, AgentToolDef{
			Tool: Tool{
				Name:        name,
				Description: desc,
				Parameters: map[string]ToolParam{
					"input": {Type: "string", Description: "The input fed to the pipeline's first stage."},
				},
				Required: []string{"input"},
				// CapNetwork: a run may dispatch agent stages whose tools
				// reach the network. Tag so private mode filters it, same
				// as the `pipeline` grouped tool.
				Caps: []Capability{CapNetwork},
			},
			Handler: func(args map[string]any) (string, error) {
				input := strings.TrimSpace(stringArg(args, "input"))
				if input == "" {
					return "", errors.New("input is required")
				}
				return t.runPipelineDefInline(d, input)
			},
		})
	}
	return out
}

// attachedPipelineToolName derives a stable, collision-resistant tool
// name from a pipeline's display name: snake_cased and prefixed with
// run_ so it reads as an action and can't shadow a built-in tool. On
// collision (or an empty/garbage display name) it suffixes _2, _3, …
func attachedPipelineToolName(display string, used map[string]bool) string {
	base := SnakeFromDisplay(display)
	base = strings.Trim(base, "_")
	if base == "" {
		base = "pipeline"
	}
	name := "run_" + base
	candidate := name
	for n := 2; used[candidate]; n++ {
		candidate = name + "_" + strconv.Itoa(n)
	}
	return candidate
}
