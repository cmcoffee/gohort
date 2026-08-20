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
	"io"
)

// stageFormFields is the controls for one stage.
// bodyStageFormFields is the stage form for a step INSIDE a body: the
// same controls, minus the kinds a body may not be.
//
// Not a separate form. A body step declares output, narrows tools, pins a
// model and reads {stage:...} exactly as any other stage does, and a
// second form would drift from this one within a month.
func bodyStageFormFields(def PipelineDef, s PipelineStage, cat editorCatalog) []ui.FormField {
	fields := stageFormFields(def, s, cat)
	for i := range fields {
		if fields[i].Field == "kind" {
			fields[i].Options = bodyKindOptions()
			fields[i].Help = "Bodies do not nest, so a loop or a fanout is not offered here. " +
				"Put inner work in its own pipeline and call it with a machine step, or flatten it."
		}
	}
	return fields
}

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
			{Value: "machine", Label: "Machine — run a whole machine as this stage"},
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
		ui.FormField{Field: "machine", Type: "select", Label: "Which machine",
			ShowWhen: keepWhileSet(s.Machine, "kind:machine"),
			Options:  append([]ui.SelectOption{{Value: "", Label: "(none)"}}, cat.machines...),
			Help: "A whole run as this stage: its own steps, its own working set, and its last step's result becomes this stage's. " +
				"Only machines marked \"this RUNS instead of converses\" can be used, because a stage has nobody waiting in it. " +
				"In a fanout body it becomes one child run per item."},
	)

	// What it returns. Every kind that makes a model call can declare
	// output, and a stage that declares none returns prose — which is
	// right for a draft or a summary and wrong for anything a later
	// stage has to read a PIECE of.
	fields = append(fields,
		ui.FormField{Type: "header", Label: "What this stage returns",
			Help: "Declare fields and the framework asks for them, validates the reply, and exposes each one as {stage:" + s.Name + ".field} — that is what makes fan_over-a-field, a loop's until, and a branch's when possible. " +
				"Never ask for JSON in the instructions as well: two sets of formatting rules is how a model ends up returning a JSON string inside a JSON field. Declare nothing for a prose stage."},
		stageOutputRows(def, s),
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
		// The coarse control, and the one that stays true: a pipeline is
		// invoked by whichever agent attached it, so the catalog it
		// inherits differs between callers and a name list describes one
		// of them. Same three settings a machine step carries.
		ui.FormField{Field: "reach", Type: "select", Label: "Tools this stage may reach",
			ShowWhen: keepWhileSet(StageReach(s), "kind:worker|fanout"),
			Options: []ui.SelectOption{
				{Value: ReachAll, Label: "Everything the calling agent has",
					Help: "The default. A pipeline attached to an agent with web_search inherits it."},
				{Value: ReachRead, Label: "Read-only — nothing that writes or reaches the network",
					Help: "For a stage that gathers and reports. Searching, listing and reading stay; posting, running and fetching go."},
				{Value: ReachNone, Label: "Nothing — this stage only reshapes what it was handed",
					Help: "For a synthesizer or a formatter that should not be tempted to go and fetch."},
			},
			Help: "Prefer this to naming tools. A pipeline is run by whatever agent attached it, and its catalog is assembled fresh each turn — an MCP server publishes its tools when it connects, a credential mints its own per session — so a name can stop resolving without anybody changing this pipeline."},
		ui.FormField{Type: "header", Label: stageNameNarrowingLabel(s),
			Collapsed: len(s.Tools) == 0,
			ShowWhen:  "kind:worker|fanout;reach:!none"},
		ui.FormField{Field: "tools", Type: "checklist", Label: "Only these tools",
			ShowWhen:    keepWhileList(s.Tools, "kind:worker|fanout;reach:!none"),
			Options:     toolChecklistOptions(cat.tools, s.Tools),
			Placeholder: "(no tools to offer)",
			Help:        "Names on TOP of the reach above; none checked means the reach alone decides. A stage that must reach one particular thing and not its neighbours is what this is for."},
	)
	return fields
}

// stageNameNarrowingLabel titles the by-name section, carrying its count
// when it has one — so a collapsed header still states the restriction.
// Same rule the machine editor's steps follow.
func stageNameNarrowingLabel(s PipelineStage) string {
	if n := len(s.Tools); n > 0 {
		return "Narrow by name — " + strconv.Itoa(n) + " named"
	}
	return "Narrow by name (advanced)"
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
		"reach": StageReach(s), "tools": s.Tools, "output": stageOutputRecord(s),
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
		{"machine", func(v string) { s.Machine = v }},
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
	if v, ok := body["reach"]; ok {
		s.Reach = strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
	}
	if _, ok := body["tools"]; ok {
		s.Tools = stringSliceFromArgs(body, "tools")
	}
	if raw, ok := body["output"]; ok {
		applyStageOutput(s, raw)
	}
}

// handlePipelineStages is the per-stage form's endpoint.
//
//	GET    /api/pipelines/{id}/stages?name=X → the stage as flat values
//	POST   /api/pipelines/{id}/stages?name=X → merge and save
//	POST   /api/pipelines/{id}/stages        → add a stage (name in body)
//	DELETE /api/pipelines/{id}/stages?name=X → remove it
func (T *OrchestrateApp) handlePipelineStages(w http.ResponseWriter, r *http.Request, udb Database, user string, def PipelineDef) {
	// name is a PATH: "stage" at the top level, "outer.inner" for a stage
	// inside a body. A stage name may not contain a dot, so the two can
	// never be confused (pipeline_body_editor.go).
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	slot := resolveStagePath(&def, name)
	idx := slot.idx

	switch r.Method {
	case http.MethodGet:
		if !slot.found() {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, stageRecord(slot.stage()))

	case http.MethodPost, http.MethodPut, http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if name != "" && !slot.found() {
			// A form still addressing a stage that was renamed or
			// removed. Treating its save as a create would resurrect the
			// old stage beside the new one.
			http.Error(w, "no stage called "+strconv.Quote(name)+" — it was renamed or removed; reload the page",
				http.StatusNotFound)
			return
		}
		var stage PipelineStage
		if slot.found() {
			stage = slot.stage()
		} else {
			stage = PipelineStage{Kind: StageWorker}
		}
		applyStageEdit(&stage, body)
		if strings.TrimSpace(stage.Name) == "" {
			http.Error(w, "give the stage a name", http.StatusBadRequest)
			return
		}
		// A stage inside a body: same form, same validator, scoped
		// rename and scoped uniqueness. Handled before the top-level
		// paths below because everything they do reads def.Stages.
		if parentRef := strings.TrimSpace(r.URL.Query().Get("parent")); parentRef != "" || slot.parent != nil {
			if err := T.savePipelineBodyStage(w, udb, user, def, slot, parentRef, stage); err != nil {
				return // savePipelineBodyStage wrote the refusal
			}
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
		if !slot.found() {
			http.NotFound(w, r)
			return
		}
		if slot.parent != nil {
			// Refused by a SIBLING that reads it, not by anything
			// outside: nothing outside may name a body stage.
			_, child := splitStagePath(name)
			if err := removeFromBody(slot.parent, child); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := def.Validate(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			saved := SavePipelineDef(udb, def)
			Log("[orchestrate.pipelines] user=%q removed body stage %q from %q", user, name, saved.Name)
			writeJSON(w, map[string]any{"deleted": name, "id": saved.ID})
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

// handlePipelineAgents is the "Assign to agents" checklist.
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
		// ?pills=1 — the shape uiRenderScopePills wants, so a pipeline
		// can be handed to an agent from the LIST as well as from its
		// own page.
		if r.URL.Query().Get("pills") == "1" {
			writeJSON(w, pipelineAgentPills(udb, user, def))
			return
		}
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
		// A single toggle, as the pills send it.
		var one struct {
			Target string `json:"target"`
			On     *bool  `json:"on"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
		if json.Unmarshal(raw, &one) == nil && one.On != nil && strings.TrimSpace(one.Target) != "" {
			ag, ok := loadAgent(udb, strings.TrimSpace(one.Target))
			if !ok || ag.Owner != user {
				http.Error(w, "no such agent", http.StatusNotFound)
				return
			}
			kept := ag.AttachedPipelines[:0:0]
			for _, pid := range ag.AttachedPipelines {
				if pid != def.ID {
					kept = append(kept, pid)
				}
			}
			if *one.On {
				kept = append(kept, def.ID)
			}
			ag.AttachedPipelines = kept
			if _, err := saveAgent(udb, ag); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			Log("[orchestrate.pipelines] user=%q pipeline %q: %s %q", user, def.Name,
				chIf(*one.On, "attached to", "detached from"), chFirst(ag.Name, ag.ID))
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		var body struct {
			Agents []string `json:"agents"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
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

// pipelineAgentPills is the scope-pill view of "who can call this".
//
// Unlike a machine, an agent holds a LIST — so a pill going on adds
// this pipeline to that agent rather than displacing anything, and the
// note says so. Getting that backwards is how somebody unplugs a
// pipeline they never touched.
func pipelineAgentPills(udb Database, user string, def PipelineDef) map[string]any {
	items := []map[string]any{}
	for _, ag := range listAgents(udb, user) {
		if isAppAgent(ag.ID) || ag.Hidden {
			continue
		}
		on := false
		others := 0
		for _, pid := range ag.AttachedPipelines {
			if pid == def.ID {
				on = true
				continue
			}
			others++
		}
		label := chFirst(ag.Name, ag.ID)
		if others > 0 {
			label += " (+" + strconv.Itoa(others) + " other)"
		}
		items = append(items, map[string]any{"key": ag.ID, "label": label, "on": on})
	}
	return map[string]any{
		"items": items,
		"note":  "An agent can hold several pipelines, so switching one on adds this one and leaves the rest alone. It reaches the agent as a tool named run_<name>.",
	}
}

// stageOutputRows is the declared-output editor.
//
// It was left as a read-only card when the stage forms landed, on the
// reasoning that the tool writes this shape well and a hand-rolled
// control would write it badly. That was right about the SHAPE and wrong
// about the audience: declaring output is how a pipeline stops being a
// chain of prose, and leaving it to the tool means the one thing that
// makes a stage useful to the next stage is the one thing the page
// cannot change.
//
// The two questions are asked in ORDER, the way the machine editor
// learned to ask them: is this something the stage WORKS OUT, or a value
// the pipeline already HOLDS — and only then what to call it. A single
// combo box asked both at once and hid the second answer behind a
// dropdown arrow nobody looks for.
//
// What it deliberately does not edit: `enum` (a list inside a row) and
// nested `fields` (a shape inside a shape). Those keep their card, and
// the card says the tool owns them, because a control that half-edits a
// structure is worse than one that says it does not.
func stageOutputRows(def PipelineDef, s PipelineStage) ui.FormField {
	return ui.FormField{
		Field: "output", Type: "rows", Label: "", AddLabel: "+ Add field",
		Placeholder: "(nothing — this stage returns prose)",
		Columns: []ui.FormField{
			{Field: "kind", Type: "select", Label: "What is this?", Width: 4,
				Options: []ui.SelectOption{
					{Value: "", Label: "Choose…"},
					{Value: "asked", Label: "Something this stage works out"},
					{Value: "filled", Label: "A value the pipeline already holds"},
				},
				Help: "A value the pipeline already holds is FILLED: it is left out of what the model is asked for entirely and merged into the result afterwards. Asking for something you already have invites a paraphrase."},
			{Field: "name", Type: "text", Label: "Name", Width: 3, HideWhen: "!kind",
				Placeholder: "short_lowercase_name",
				Help:        "Later stages read it as {stage:" + s.Name + ".<name>}."},
			// The source of a filled field, from the values that actually
			// exist at this point in the run — computed rather than typed,
			// which is the difference between a picker and a place to make
			// a typo the validator then refuses.
			{Field: "from", Type: "select", Label: "Filled from", Width: 4, ShowWhen: "kind:filled",
				Options: stageFillOptions(def, s.Name),
				Help:    "Only values from EARLIER in the run: stages run strictly in order, so a reference forward resolves to nothing and the save is refused."},
			// A filled field holds text, always, so its type and its
			// instruction are controls that would do nothing.
			{Field: "type", Type: "select", Label: "Type", Width: 2, ShowWhen: "kind:asked", Options: []ui.SelectOption{
				{Value: "string", Label: "text"},
				{Value: "list", Label: "list"},
				{Value: "number", Label: "number"},
				{Value: "bool", Label: "yes/no"},
				{Value: "object", Label: "object"},
			}},
			{Field: "required", Type: "toggle", Label: "Required", ShowWhen: "kind:asked",
				Help: "A required field the model omits fails the stage. An optional one resolves to empty."},
			{Field: "desc", Type: "textarea", Rows: 3, OwnLine: true, ShowWhen: "kind:asked",
				Label:       "What to find — write it as the instruction for this field",
				Placeholder: "e.g. the specific claim the sources actually support, not a summary of what they discuss."},
		},
	}
}

// stageFillOptions is what a filled field can be filled FROM at this
// point in the run: the pipeline's input, the previous stage's whole
// output, and every field an EARLIER stage declares.
//
// Earlier only, because stages run strictly in order and Validate
// refuses a forward reference — offering one would be offering a save
// that cannot succeed.
func stageFillOptions(def PipelineDef, upTo string) []ui.SelectOption {
	out := []ui.SelectOption{
		{Value: "", Label: "Choose a value…"},
		{Value: "{input}", Label: "{input} — what the run was started with"},
		{Value: "{prev}", Label: "{prev} — the previous stage's whole output"},
	}
	for _, s := range def.Stages {
		if strings.TrimSpace(s.Name) == strings.TrimSpace(upTo) {
			break
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		out = append(out, ui.SelectOption{
			Value: "{stage:" + name + "}", Label: "{stage:" + name + "} — all of " + name + "'s output",
		})
		for _, f := range s.ModelOutput() {
			out = append(out, ui.SelectOption{
				Value: "{stage:" + name + "." + f.Name + "}",
				Label: "{stage:" + name + "." + f.Name + "} — " + previewText(f.Desc, 40),
			})
		}
	}
	return out
}

// stageOutputRecord renders a stage's declared fields for the rows
// control, answering the kind question from what the field IS.
func stageOutputRecord(s PipelineStage) []map[string]any {
	out := make([]map[string]any, 0, len(s.Output))
	for _, f := range s.Output {
		kind := "asked"
		if strings.TrimSpace(f.From) != "" {
			kind = "filled"
		}
		out = append(out, map[string]any{
			"kind": kind, "name": f.Name, "from": f.From,
			"type": string(f.Type), "required": f.Required, "desc": f.Desc,
		})
	}
	return out
}

// applyStageOutput merges the rows control back onto the stage.
//
// Nested `fields` and `enum` are PRESERVED from the stored field of the
// same name rather than dropped: the control does not edit them, and a
// form that silently deletes what it cannot show is the worst of the
// three possible behaviours.
func applyStageOutput(s *PipelineStage, raw any) {
	rows, ok := raw.([]any)
	if !ok {
		return
	}
	keep := map[string]PipelineField{}
	for _, f := range s.Output {
		keep[strings.TrimSpace(f.Name)] = f
	}
	out := make([]PipelineField, 0, len(rows))
	for _, item := range rows {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(mapStr(m, "name"))
		if name == "" {
			continue
		}
		f := PipelineField{Name: name}
		if prev, had := keep[name]; had {
			f.Enum, f.Fields = prev.Enum, prev.Fields
		}
		if strings.TrimSpace(mapStr(m, "kind")) == "filled" {
			// Filled: no type, no instruction, no required — the value is
			// already known and everything a variable carries is text.
			f.From = strings.TrimSpace(mapStr(m, "from"))
			f.Type = FieldString
			out = append(out, f)
			continue
		}
		f.Type = PipelineFieldType(strings.ToLower(strings.TrimSpace(mapStr(m, "type"))))
		if f.Type == "" {
			f.Type = FieldString
		}
		f.Desc = mapStr(m, "desc")
		f.Required = mapBool(m, "required")
		out = append(out, f)
	}
	s.Output = out
}
