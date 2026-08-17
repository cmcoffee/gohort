// The per-stage form.
//
// A pipeline page could show its stages and not change them, which made
// it a document. This is the other half: one form per stage, with the
// controls a stage actually has and only those — a fanout has a
// fan_over and a branch does not, and a form that shows every field to
// every stage teaches that they are all the same thing.
//
// Two rules carried over from the machine editor, both learned the hard
// way there:
//
//   - A control that is hidden must not be holding a value. Fields gate
//     on KIND, and a stage carrying something its kind does not use
//     keeps that control visible so it can be emptied. Hiding it is how
//     a finding becomes unactionable.
//   - Saves merge. A form holds part of a stage; posting the whole
//     record from a form that only loaded some of it is how a save
//     clobbers what it never showed.
//
// Unlike a machine, a pipeline REFUSES an edit that would not run —
// Validate is the same gate at every door. So the handler returns the
// validator's own sentence, which the form now shows inline.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// stageFormFields is the controls for one stage.
func stageFormFields(def PipelineDef, s PipelineStage, cat editorCatalog) []ui.FormField {
	kind := strings.TrimSpace(string(s.Kind))
	if kind == "" {
		kind = string(StageWorker)
	}
	fields := []ui.FormField{
		{Field: "name", Type: "text", Label: "Name",
			Help: "How later stages address it: {stage:" + s.Name + "} for everything it returned, {stage:" + s.Name + ".field} for one piece. Renaming rewrites every reference for you."},
		{Field: "kind", Type: "select", Label: "What it does", Options: []ui.SelectOption{
			{Value: "worker", Label: "Worker — one model call"},
			{Value: "agent", Label: "Agent — dispatch to one of your agents"},
			{Value: "fanout", Label: "Fanout — run once per item of an earlier list, in parallel"},
			{Value: "loop", Label: "Loop — repeat a body of stages"},
			{Value: "branch", Label: "Branch — read a bool and skip or stop (no model call)"},
			{Value: "tool", Label: "Tool — call a tool directly (no model, no tokens)"},
		}, Help: "Changing this changes which controls below apply. Anything the new kind does not use stays visible while it still holds a value, so you can clear it."},
	}

	// The prompt: every kind that makes a model call.
	fields = append(fields, ui.FormField{
		Field: "prompt", Type: "textarea", Rows: 6, Label: "Instructions",
		ShowWhen: keepWhileSet(s.Prompt, "kind:worker|agent|fanout"),
		Help: "The METHOD, not the shape of the answer. Declared fields below already say what to produce. " +
			"Templating: {input}, {prev}, {stage:NAME}, {stage:NAME.field}" +
			chIf(kind == string(StageFanout), ", and {item} for the element this branch got", "") + ".",
	})

	fields = append(fields,
		ui.FormField{Field: "agent", Type: "select", Label: "Which agent",
			ShowWhen: keepWhileSet(s.Agent, "kind:agent"),
			Options:  append([]ui.SelectOption{{Value: "", Label: "(none)"}}, cat.agents...),
			Help:     "It answers with its own persona, tools and memory; what comes back is shaped into this stage's declared fields."},
		ui.FormField{Field: "fan_over", Type: "text", Label: "Fan over",
			ShowWhen: keepWhileSet(s.FanOver, "kind:fanout"),
			Help:     "An earlier stage whose prompt emits a JSON array, or one of its declared list fields: \"plan.queries\". Capped at 12 items, 6 at a time."},
		ui.FormField{Field: "count", Type: "number", Label: "At most this many passes",
			ShowWhen: keepWhileSet(strconv.Itoa(s.Count), "kind:loop"),
			Help:     "1-25. The hard ceiling: a loop that never satisfies its stop condition ends here."},
		ui.FormField{Field: "until", Type: "text", Label: "Stop early when",
			ShowWhen: keepWhileSet(s.Until, "kind:loop"),
			Help:     "One bool field a BODY stage declares, by bare name: \"critic.satisfied\". Not an expression — no ==, no braces. A field from outside the loop never changes between passes."},
		ui.FormField{Field: "when", Type: "text", Label: "Take the branch when",
			ShowWhen: keepWhileSet(s.When, "kind:branch"),
			Help:     "A bool field an EARLIER stage declared, by bare name. True takes the branch."},
		ui.FormField{Field: "skip_to", Type: "select", Label: "…and skip to",
			ShowWhen: keepWhileSet(s.SkipTo, "kind:branch"),
			Options:  append([]ui.SelectOption{{Value: "", Label: "(end the pipeline)"}}, laterStageOptions(def, s.Name)...),
			Help:     "Forward only — repeating work is what a loop is for. Leave it empty and a taken branch ENDS the pipeline, returning the last stage's output."},
		ui.FormField{Field: "tool", Type: "select", Label: "Which tool",
			ShowWhen: keepWhileSet(s.Tool, "kind:tool"),
			Options:  append([]ui.SelectOption{{Value: "", Label: "(none)"}}, cat.tools...),
			Help:     "Called directly with the arguments you write. For computation rather than judgement: arithmetic, dedup, one specific API call."},
	)

	// How it runs. Collapsed, because most stages never touch it.
	fields = append(fields,
		ui.FormField{Type: "header", Label: "How this stage runs", Collapsed: true},
		ui.FormField{Field: "model", Type: "select", Label: "Which model",
			ShowWhen: keepWhileSet(s.Model, "kind:worker|fanout"),
			Options: []ui.SelectOption{
				{Value: "", Label: "Inherit the agent's routing"},
				{Value: "worker", Label: "Worker — the cheap, local one"},
				{Value: "lead", Label: "Lead — the precise, remote one"},
			},
			Help: "A transform or a routing decision is worker work; a stage that commits to an explanation is usually lead."},
		ui.FormField{Field: "think", Type: "select", Label: "Reasoning",
			ShowWhen: keepWhileSet(thinkValue(s.Think), "kind:worker|fanout"),
			Options: []ui.SelectOption{
				{Value: "", Label: "Inherit"},
				{Value: "on", Label: "On — this stage is a judgement"},
				{Value: "off", Label: "Off — this stage is a transform"},
			}},
		ui.FormField{Field: "tools", Type: "checklist", Label: "Only these tools",
			ShowWhen:    keepWhileList(s.Tools, "kind:worker|fanout"),
			Options:     toolChecklistOptions(cat.tools, s.Tools),
			Placeholder: "(no tools to offer)",
			Help:        "Check tools to NARROW what this stage can reach; none checked means it inherits the calling agent's whole catalog. A stage that must reach the web has to keep those tools."},
	)
	return fields
}

// keepWhileSet is the hidden-control rule: gate on kind, unless the
// field is already holding something.
//
// The machine editor shipped three fields that hid values they were
// still storing, and each turned a reported finding into a dead end.
// The same shape here would be worse, because a pipeline's validator
// REFUSES what it cannot resolve — a hidden fan_over on a stage that is
// no longer a fanout is an unfixable save.
func keepWhileSet(stored, expr string) string {
	if strings.TrimSpace(stored) != "" && strings.TrimSpace(stored) != "0" {
		return ""
	}
	return expr
}

func keepWhileList(stored []string, expr string) string {
	if len(stored) > 0 {
		return ""
	}
	return expr
}

// thinkValue renders the tri-state pointer as the select's value.
func thinkValue(t *bool) string {
	if t == nil {
		return ""
	}
	if *t {
		return "on"
	}
	return "off"
}

// laterStageOptions is the stages a branch may jump to: forward only,
// which is what Validate enforces anyway.
func laterStageOptions(def PipelineDef, from string) []ui.SelectOption {
	var out []ui.SelectOption
	seen := false
	for _, s := range def.Stages {
		if strings.TrimSpace(s.Name) == strings.TrimSpace(from) {
			seen = true
			continue
		}
		if !seen {
			continue
		}
		out = append(out, ui.SelectOption{Value: s.Name, Label: s.Name})
	}
	return out
}

// stageRecord is what the form loads: the stage as flat values.
func stageRecord(s PipelineStage) map[string]any {
	kind := strings.TrimSpace(string(s.Kind))
	if kind == "" {
		kind = string(StageWorker)
	}
	return map[string]any{
		"name": s.Name, "kind": kind, "prompt": s.Prompt,
		"agent": s.Agent, "fan_over": s.FanOver, "count": s.Count,
		"until": s.Until, "when": s.When, "skip_to": s.SkipTo,
		"tool": s.Tool, "model": s.Model, "think": thinkValue(s.Think),
		"tools": s.Tools,
	}
}

// applyStageEdit merges a form's fields onto a stage. Only keys the form
// actually sent are applied, so a panel that shows half a stage cannot
// blank the other half.
func applyStageEdit(s *PipelineStage, body map[string]any) {
	str := func(k string) (string, bool) {
		v, ok := body[k]
		if !ok {
			return "", false
		}
		return strings.TrimSpace(fmt.Sprint(v)), true
	}
	if v, ok := str("name"); ok && v != "" {
		s.Name = v
	}
	if v, ok := str("kind"); ok && v != "" {
		s.Kind = PipelineStageKind(v)
	}
	for _, f := range []struct {
		key string
		set func(string)
	}{
		{"prompt", func(v string) { s.Prompt = v }},
		{"agent", func(v string) { s.Agent = v }},
		{"fan_over", func(v string) { s.FanOver = v }},
		{"until", func(v string) { s.Until = v }},
		{"when", func(v string) { s.When = v }},
		{"skip_to", func(v string) { s.SkipTo = v }},
		{"tool", func(v string) { s.Tool = v }},
		{"model", func(v string) { s.Model = v }},
	} {
		if v, ok := str(f.key); ok {
			f.set(v)
		}
	}
	if v, ok := str("count"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.Count = n
		}
	}
	if v, ok := str("think"); ok {
		switch v {
		case "on":
			yes := true
			s.Think = &yes
		case "off":
			no := false
			s.Think = &no
		default:
			s.Think = nil
		}
	}
	if _, ok := body["tools"]; ok {
		s.Tools = stringSliceFromArgs(body, "tools")
	}
}

// handlePipelineStages is the per-stage form's endpoint.
//
//	GET    /api/pipelines/{id}/stages?name=X → the stage as flat values
//	POST   /api/pipelines/{id}/stages?name=X → merge and save
//	POST   /api/pipelines/{id}/stages        → add a stage (name in body)
//	DELETE /api/pipelines/{id}/stages?name=X → remove it
func (T *OrchestrateApp) handlePipelineStages(w http.ResponseWriter, r *http.Request, udb Database, user string, def PipelineDef) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	idx := -1
	for i, s := range def.Stages {
		if strings.TrimSpace(s.Name) == name {
			idx = i
			break
		}
	}

	switch r.Method {
	case http.MethodGet:
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, stageRecord(def.Stages[idx]))

	case http.MethodPost, http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if name != "" && idx < 0 {
			// A form still addressing a stage that was renamed or
			// removed. Treating its save as a create would resurrect the
			// old stage beside the new one.
			http.Error(w, "no stage called "+strconv.Quote(name)+" — it was renamed or removed; reload the page",
				http.StatusNotFound)
			return
		}
		var stage PipelineStage
		if idx >= 0 {
			stage = def.Stages[idx]
		} else {
			stage = PipelineStage{Kind: StageWorker}
		}
		applyStageEdit(&stage, body)
		if strings.TrimSpace(stage.Name) == "" {
			http.Error(w, "give the stage a name", http.StatusBadRequest)
			return
		}
		if idx >= 0 {
			// A rename rewrites every reference, or the save is refused
			// by the validator for a reason the author did not cause.
			if old := strings.TrimSpace(def.Stages[idx].Name); old != stage.Name {
				for _, other := range def.Stages {
					if strings.TrimSpace(other.Name) == stage.Name {
						http.Error(w, "a stage called "+strconv.Quote(stage.Name)+" already exists",
							http.StatusBadRequest)
						return
					}
				}
				def.Stages[idx] = stage
				def.RenameStage(old, stage.Name)
			} else {
				def.Stages[idx] = stage
			}
		} else {
			for _, other := range def.Stages {
				if strings.TrimSpace(other.Name) == stage.Name {
					http.Error(w, "a stage called "+strconv.Quote(stage.Name)+" already exists",
						http.StatusBadRequest)
					return
				}
			}
			def.Stages = append(def.Stages, stage)
		}
		// The same gate every other pipeline door uses. Its sentence is
		// the reply, because the form shows it inline and the validator
		// says what is wrong better than "bad request" does.
		if err := def.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved := SavePipelineDef(udb, def)
		Log("[orchestrate.pipelines] user=%q edited stage %q of %q", user, stage.Name, saved.Name)
		writeJSON(w, map[string]any{"ok": true, "id": saved.ID, "name": stage.Name,
			"slug": ui.SectionSlug(stageSectionTitle(indexOfStage(saved, stage.Name), stage.Name))})

	case http.MethodDelete:
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		if err := def.RemoveStage(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := def.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved := SavePipelineDef(udb, def)
		Log("[orchestrate.pipelines] user=%q removed stage %q from %q", user, name, saved.Name)
		writeJSON(w, map[string]any{"deleted": name, "id": saved.ID})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// indexOfStage is where a stage sits, for the anchor a save redirects to.
func indexOfStage(def PipelineDef, name string) int {
	for i, s := range def.Stages {
		if strings.TrimSpace(s.Name) == strings.TrimSpace(name) {
			return i
		}
	}
	return 0
}

// handlePipelineAgents is the "Who can call it" checklist.
//
//	GET  /api/pipelines/{id}/agents → {agents: [id, ...]}
//	POST /api/pipelines/{id}/agents {agents: [...]} → attach/detach
//
// A pipeline attaches to an agent as a callable tool (run_<name>), and
// an agent holds a LIST of them — so unlike a machine, checking one
// here adds to that list rather than replacing what the agent runs.
func (T *OrchestrateApp) handlePipelineAgents(w http.ResponseWriter, r *http.Request, udb Database, user string, def PipelineDef) {
	switch r.Method {
	case http.MethodGet:
		var ids []string
		for _, ag := range listAgents(udb, user) {
			for _, pid := range ag.AttachedPipelines {
				if pid == def.ID {
					ids = append(ids, ag.ID)
					break
				}
			}
		}
		writeJSON(w, map[string]any{"agents": ids})

	case http.MethodPost, http.MethodPut:
		var body struct {
			Agents []string `json:"agents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		want := make(map[string]bool, len(body.Agents))
		for _, id := range body.Agents {
			want[strings.TrimSpace(id)] = true
		}
		var attached, detached []string
		for _, ag := range listAgents(udb, user) {
			// The checklist never shows app agents or hidden ones, so a
			// whole-set POST must not touch them: an agent that was
			// never ON the list would be silently unplugged by every
			// save of the agents that are. Same rule the machine
			// version follows, for the same reason.
			if isAppAgent(ag.ID) || ag.Hidden {
				continue
			}
			has := false
			kept := ag.AttachedPipelines[:0:0]
			for _, pid := range ag.AttachedPipelines {
				if pid == def.ID {
					has = true
					continue
				}
				kept = append(kept, pid)
			}
			switch {
			case want[ag.ID] && !has:
				ag.AttachedPipelines = append(kept, def.ID)
				if _, err := saveAgent(udb, ag); err == nil {
					attached = append(attached, chFirst(ag.Name, ag.ID))
				}
			case !want[ag.ID] && has:
				// Only this pipeline is removed; the agent's other
				// pipelines are its own business.
				ag.AttachedPipelines = kept
				if _, err := saveAgent(udb, ag); err == nil {
					detached = append(detached, chFirst(ag.Name, ag.ID))
				}
			}
		}
		if len(attached)+len(detached) > 0 {
			Log("[orchestrate.pipelines] user=%q pipeline %q: attached %v, detached %v",
				user, def.Name, attached, detached)
		}
		writeJSON(w, map[string]any{"ok": true, "attached": attached, "detached": detached})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// attachPipelineAgentOptions lists the agents a pipeline can be given
// to, with what each one is for — a list of bare names makes somebody
// guess which is which.
func attachPipelineAgentOptions(udb Database, user string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, ag := range listAgents(udb, user) {
		if isAppAgent(ag.ID) || ag.Hidden {
			continue
		}
		label := chFirst(ag.Name, ag.ID)
		if d := strings.TrimSpace(ag.Description); d != "" {
			label += " — " + d
		}
		out = append(out, ui.SelectOption{Value: ag.ID, Label: label})
	}
	return out
}

// pipelineCostText derives what one run costs, in model calls.
//
// Derived rather than written: the definition knows exactly which
// stages call a model and which do not, and a fanout or a loop turns
// one line of a stage list into twelve calls. Somebody choosing between
// three stages and six should see the price where they are choosing.
func pipelineCostText(def PipelineDef) string {
	var plain, free []string
	var multiplied []string
	for _, s := range def.Stages {
		name := strings.TrimSpace(s.Name)
		switch PipelineStageKind(strings.TrimSpace(string(s.Kind))) {
		case StageBranch:
			free = append(free, name)
		case StageTool:
			free = append(free, name)
		case StageFanout:
			multiplied = append(multiplied, name+" (once per item, up to "+strconv.Itoa(FanoutMaxItems)+")")
		case StageLoop:
			inner := 0
			for _, b := range s.Body {
				switch PipelineStageKind(strings.TrimSpace(string(b.Kind))) {
				case StageBranch, StageTool:
				default:
					inner++
				}
			}
			multiplied = append(multiplied, name+" ("+strconv.Itoa(inner)+" per pass, up to "+strconv.Itoa(s.Count)+" passes)")
		case StageAgent:
			multiplied = append(multiplied, name+" (a whole agent turn, with its own tools)")
		default:
			plain = append(plain, name)
		}
	}
	var parts []string
	if len(plain) > 0 {
		parts = append(parts, strings.Join(plain, ", ")+" cost one model call each")
	}
	if len(multiplied) > 0 {
		parts = append(parts, strings.Join(multiplied, "; "))
	}
	if len(free) > 0 {
		parts = append(parts, strings.Join(free, ", ")+" make no model call at all")
	}
	if len(parts) == 0 {
		return "Nothing — this pipeline has no stages yet."
	}
	return strings.Join(parts, ". ") + ". A stage that declares output may pay one extra repair call when a reply comes back malformed."
}
