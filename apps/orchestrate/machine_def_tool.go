// `machine` — grouped tool for session-resident phase machines
// (core.MachineDef). Same shape as the `pipeline` tool, for the same
// reason: a multi-phase recipe is authored, revised, and attached, and
// the house pattern for authoring a recipe is a chat tool rather than a
// bespoke editor page.
//
//	create / update — author a machine (name, description, start, phases[])
//	list            — see the user's machines
//	get             — read one machine's full definition
//	delete          — remove one
//
// There is no `run`. A machine has nowhere to run outside a session: it
// runs when a turn arrives on an agent that points at it. That is the
// difference between this and a pipeline, and it is why attach_to_agents
// matters more here than there — an unattached machine does nothing at
// all, where an unattached pipeline is at least runnable on demand.
//
// See docs/agent-machines.md.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) machineGroupedToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "machine",
			Description: "Author phase machines — workflows an agent LIVES IN across a conversation, rather than running once and returning. A machine is a set of phases; the session remembers which phase it is in between turns, and what earlier phases decided. Actions: create, update, list, get, delete.\n\nUse a machine when a conversation should do something ONCE and then settle: work out what is being asked, pick an approach, then answer in that frame for the rest of the thread. Use a PIPELINE instead when the work runs start-to-finish and hands back a result. Use neither for a one-off question.\n\n**Pass `attach_to_agents` in the same call** — an unattached machine does nothing at all, because a machine only runs inside a session on an agent that points at it. Call action=\"help\" for the full spec.",
			Parameters: map[string]ToolParam{
				"action":      {Type: "string", Description: "One of: create | update | list | get | delete | help."},
				"name":        {Type: "string", Description: "Machine name. Required for create; get/update/delete also accept the id."},
				"id":          {Type: "string", Description: "(update/get/delete) Machine id, if you have it instead of the name."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the machine is for."},
				"start":       {Type: "string", Description: "(create/update) Name of the phase a fresh session enters. Defaults to the first phase in the list."},
				"full":        {Type: "boolean", Description: "(get) When true, return every phase's full prompt. Default false previews them to save context."},
				"phases": {
					Type:        "array",
					Description: "(create/update) Ordered phases, each an object: {\"name\": unique label, \"desc\": one line, \"prompt\": the directive}. The KEY field is \"resident\": true marks a phase user turns come back to (a turn ENDS there); false/omitted marks a transient phase that runs, produces a result, and hands straight off inside the same turn. Every machine needs at least one resident phase. Transient phases declare \"output\": [{name,type,desc,required}] and hand off with \"next\", or, to decide at run time, list the phases they may hand to in \"choices\" (the framework declares the routing field itself — do not declare one, and do not list the options in a prompt). Resident phases may NOT declare output — their reply goes to the user. A resident phase with \"next\" gets ONE turn then hands off (an intake beat); without one it stays. Add \"guard\": a plain-language condition that, checked each turn, moves the conversation out (\"the user has moved on to a different subject\"), with \"guard_to\" naming where it goes. Per-phase \"tools\" (subset of the agent's catalog), \"model\" (\"worker\"|\"lead\"), \"think\" (\"on\"|\"off\" — OFF by default on a transient phase; turn it ON for one that genuinely judges, such as decomposing an ambiguous request or routing between close options). Prompts template a fixed set of built-ins — {input}/{original_input}/{established}/{prev}/{now}/{user}/{agent}/{step}/{machine} (transient only; the message AND the earlier findings are supplied anyway if you never place them) and {state:PHASE} / {state:PHASE.field} (anywhere). **Call action=\"help\" for the full spec.**",
					Items:       &ToolParam{Type: "object"},
				},
				"attach_to_agents": {
					Type:        "array",
					Description: "(create/update) Agent names or IDs to point at this machine, replacing whatever machine each one had. An agent runs at most ONE machine, so this is a set, not an add. Sessions already open keep the machine they started with; new sessions get this one. Unknown names are reported back and the machine still saves.",
					Items:       &ToolParam{Type: "string"},
				},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
			switch action {
			case "create", "update":
				return t.machineCreateOrUpdate(args, action == "update")
			case "list":
				return t.machineList()
			case "get":
				return t.machineGet(args)
			case "delete":
				return t.machineDelete(args)
			case "help", "":
				return machineHelpText, nil
			default:
				return "", fmt.Errorf("unknown action %q — use create | update | list | get | delete | help", action)
			}
		},
	}
}

const machineHelpText = `machine actions:
- create  {name, description?, start?, phases:[...], attach_to_agents?:[names]} — author a machine.
- update  {name|id, ...} — revise in place (same id, attachments stay).
- list    — your machines: [{id, name, description, phases, start}].
- get     {name|id, full?:true} — one machine's definition.
- delete  {name|id}.

An unattached machine does nothing — pass attach_to_agents, or the agent never enters it.

=== WHAT A MACHINE IS ===
A pipeline runs start-to-finish and returns a result. A machine is where a conversation SITS. The
session remembers which phase it is in between turns, plus a blackboard of what earlier phases
decided. The canonical shape is: work out what is being asked (once), pick an approach (once), then
answer in that frame for the rest of the thread — re-deciding only when the subject genuinely
changes.

Turn 1 runs the transient phases and then replies from the resident one. Turns 2+ go straight to the
resident phase, with the earlier decisions pinned into the prompt. Those decisions are NOT chat
history: they are state, so turn 8 is not re-reading turn 1's reasoning.

=== PHASE FIELDS ===
name       unique label; also the key others read as {state:NAME}. No dots.
desc       one line: what this phase is for. Shown to the guard and in the phase list.
prompt     the directive, layered on top of the agent's persona (it does not replace it)
resident   true = user turns land here and a turn ENDS here. At least one per machine.
next       where control goes when this phase finishes
choices    (transient) [phase names] this phase may hand to; it DECIDES between them at run time.
           Prefer this over next_from: the framework declares the routing field (next_step) and
           writes the instruction naming each destination, so there is nothing to keep in sync.
next_from  (transient) one of THIS phase's declared string fields, whose value names the next phase.
           Only when the routing value is ALSO a finding worth naming. Overrides choices.
agent      (transient) delegate this phase to another agent by name or id
output     [{name, type, desc, required, from}] — validated JSON. Transient phases only.
           A field NAMED after a built-in (original_input, now, user, agent, prev, step, machine)
           IS that built-in: it is filled from what the framework already holds and never asked
           of the model. Do not spend a prompt or a description on one — a value already known is
           not a judgement. Use "from" only to give such a value your OWN field name:
           {"name": "asked", "from": "{original_input}"}. Filled fields hold TEXT and are left out
           of the contract entirely.
           DECLARING these IS the structured-output mechanism. Never ask for JSON in the prompt,
           never describe a shape, never give an example object: the framework encodes the fields
           and validates what comes back. A prompt that also specifies a format is two sets of
           formatting rules, and the usual result is a JSON string nested inside a JSON field.
           Write the prompt to a person — say what to find, and let the fields say what to return.
guard      (resident) plain-language condition that moves the conversation out
guard_to   (resident) where a tripped guard goes; defaults to the start phase
exits_to   [phase names] this phase may be MOVED to by change_phase (the agent deciding mid-turn
           that the request moved on). Empty = anywhere, which is right for most machines. Use it
           when the machine BRANCHES and the arms must stay separate: without it every resident
           phase offers every other phase, so a conversation can cross from one arm to the other.
           Bounds the agent only — this phase's own next, and its guard's target, are always allowed.
keep       [phase names] whose state survives RE-ENTRY into this phase; empty keeps everything
tools      what this phase may use. A RESIDENT phase runs as the turn itself, so this NARROWS the
           agent's catalog and empty inherits all. A TRANSIENT phase runs before the turn has a
           catalog, so it reaches exactly what it names and empty means NO tools — right for a
           phase that only decides or reshapes, wrong for one told to go and look. A phase needing
           different REACH (its own persona, memory, tools) should be delegated with "agent".
model      "worker" | "lead"    think   "on" | "off"

=== TRANSIENT vs RESIDENT ===
Transient = runs and hands off inside one turn; the user never takes a turn in it. Decompose, route,
classify, plan. It MUST hand off (next or next_from).
Resident = the conversation lives here. Answer, converse, execute. It may NOT declare output (its
reply goes to the person, not a decoder) and may not use {input}, {prev} or {now} in its prompt
(pinned across turns; the message is already in the conversation). The stable variables —
{original_input}, {user}, {agent}, {step}, {machine} — work there.
A resident phase with "next" gets exactly ONE turn and then hands off — that is how you write an
intake beat that asks its questions and moves on.

=== ROUTING ===
Static: "next": "answer".
Deciding: "choices": ["hunch", "answer"] — the phase picks one at run time. Do NOT declare a field
for it and do NOT list the options in a prompt or a description: the framework declares next_step
with those values, writes the instruction naming each destination and what it is for, and rejects a
choice that is not a phase when the machine is SAVED. Keep "next" as the fallback.
By hand: declare a string field and point "next_from" at it, when the value is also a finding worth
naming. If the model returns a name that does not exist at run time, the machine falls back to
"next" and leaves a breadcrumb rather than stranding the turn.

=== LEAVING A PHASE ===
Two ways out, and they do the same thing:
- The agent calls change_phase itself. Always available when the machine has more than one phase.
- A "guard" fires. Checked before the phase gets the turn, in fresh context, against the condition
  you wrote. Costs one small model call PER TURN spent in that phase, so put it only where being
  stuck would actually be wrong. It fails toward staying put.
Write guards as a condition for LEAVING, not for staying: "the user has moved on to a subject the
earlier breakdown does not cover".

=== TEMPLATING ===
A fixed vocabulary of primitives — no declaring, no naming, same meaning in every machine:
{input} the person's message this turn · {original_input} the message that opened the conversation ·
{established} everything earlier phases worked out · {prev} the phase run just before, this turn ·
{now} the date and time where the person is · {user} · {agent} · {step} · {machine}.
Transient phases only (a resident phase's prompt is pinned across turns).
You do not have to place {input} or {established}: a transient phase is handed the message when its
prompt mentions none, and the blackboard when it places no {state:…} reference of its own. Reach for
{state:PHASE.field} only when you need ONE value inside a sentence.
{state:NAME} a phase's reply · {state:NAME.field} one declared field — anywhere, any turn.
Every reference is checked when the machine is saved.

=== A WORKED EXAMPLE ===
phases: [
  {name: "decompose", desc: "Work out what is being asked.",
   prompt: "Break this request into its parts.", next: "route",
   output: [{name: "parts", type: "list", desc: "the distinct questions"}]},
  {name: "route", desc: "Pick an approach.",
   prompt: "Pick the phase that should answer.",
   choices: ["answer", "deep"], next: "answer"},
  {name: "answer", desc: "Reply directly.", resident: true,
   prompt: "Answer plainly, working from what is settled.",
   guard: "the user has moved on to a subject the breakdown does not cover", guard_to: "decompose"},
  {name: "deep", desc: "Long-form work.", resident: true,
   prompt: "Take your time and show your working.",
   guard: "the user has moved on to a subject the breakdown does not cover", guard_to: "decompose"}
]

=== THINKING ===
Transient phases run WITHOUT reasoning by default, because they are paid before the user sees a
single word. That default is wrong for some of them. Turn "think": "on" on a phase that genuinely
JUDGES — decomposing an ambiguous request, routing between approaches that are close together,
weighing evidence. Leave it off for a phase that transforms or classifies something already clear.
Same rule pipeline stages follow, and decomposition is the case that most often earns it.

The guard never reasons; that is not configurable. It is a cheap check in front of the turn, and a
guard that needs deliberation is really a transient phase.

The RESIDENT phase inherits the agent's own think setting, so the reply reasons if the agent does.
Set "think" on a resident phase only to differ from the agent — a fast lookup phase inside an
otherwise deliberate agent, or the reverse.

=== COST ===
Transient phases are extra model calls before the user sees anything, so keep them few, and cheap
unless the phase is doing real judgment (see THINKING). A guard is one small call per turn in that
phase. The resident phase's own turn costs exactly what an ordinary agent turn costs — the machine
adds nothing to the turns it is not doing work on.`

// machineCreateOrUpdate parses the phases array and saves a MachineDef.
// Mirrors pipelineCreateOrUpdate, including the upsert-on-update path:
// after a REFUSED create nothing was stored, and the reflex is to "fix
// it" with update, which would otherwise spend a round discovering there
// is nothing to fix.
func (t *chatTurn) machineCreateOrUpdate(args map[string]any, isUpdate bool) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" && !isUpdate {
		return "", errors.New("name is required to create a machine")
	}
	phases, err := parseMachinePhases(args["phases"])
	if err != nil {
		return "", err
	}

	var def MachineDef
	createdViaUpdate := false
	if isUpdate {
		existing, ok := t.findMachine(args)
		switch {
		case ok:
			def = existing
			if name != "" {
				def.Name = name
			}
		case name != "" && len(phases) > 0:
			createdViaUpdate = true
			isUpdate = false
			def = MachineDef{Name: name, Owner: t.user}
		default:
			return "", errors.New("no matching machine to update — nothing is stored under that name/id, and this call carries no phases to store as a new one. machine(action=\"list\") shows what you actually have")
		}
	} else {
		def = MachineDef{Name: name, Owner: t.user}
	}
	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		def.Description = d
	}
	if s := strings.TrimSpace(stringArg(args, "start")); s != "" {
		def.Start = s
	}
	if len(phases) > 0 {
		def.Phases = phases
	}
	if err := def.Validate(); err != nil {
		return "", fmt.Errorf("machine is not runnable: %w", err)
	}
	def.Owner = t.user
	saved := SaveMachineDef(t.udb, def)

	verb := "Created"
	if isUpdate {
		verb = "Updated"
	}
	if createdViaUpdate {
		verb = "Created (nothing was stored under that name yet, so this was saved as a new machine rather than an edit)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s machine %q (id=%s) with %d phase%s: %s.",
		verb, saved.Name, saved.ID, len(saved.Phases), plural(len(saved.Phases)), strings.Join(saved.PhaseNames(), ", "))
	fmt.Fprintf(&b, " A session starts in %s.", saved.StartPhase())

	attached, unknown := t.attachMachineToAgents(args["attach_to_agents"], saved.ID)
	switch {
	case len(attached) > 0:
		target := "that agent"
		if len(attached) > 1 {
			target = "those agents"
		}
		fmt.Fprintf(&b, " Pointed %s at it — new sessions on %s will run it (sessions already open keep what they started with).",
			strings.Join(attached, ", "), target)
	case len(unknown) == 0:
		b.WriteString(" NOT attached to any agent yet, so nothing runs it — pass attach_to_agents, or point an agent at it.")
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, " No agent found named: %s.", strings.Join(unknown, ", "))
	}
	return b.String(), nil
}

// attachMachineToAgents points each named agent at a machine. Returns
// the agents changed and the names that matched nothing.
//
// An agent runs at most one machine, so this SETS rather than appends —
// the asymmetry with attached_pipelines is real and the tool description
// says so, because "attach" reading as "add to a list" is how someone
// ends up expecting two machines to run at once.
func (t *chatTurn) attachMachineToAgents(raw any, machineID string) (attached, unknown []string) {
	names, _ := raw.([]any)
	for _, n := range names {
		key := strings.TrimSpace(fmt.Sprint(n))
		if key == "" {
			continue
		}
		ag, ok := t.findAgentByNameOrID(key)
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		if ag.Machine == machineID {
			attached = append(attached, chFirst(ag.Name, ag.ID))
			continue
		}
		ag.Machine = machineID
		if _, err := saveAgent(t.udb, ag); err != nil {
			unknown = append(unknown, key+" (save failed: "+err.Error()+")")
			continue
		}
		attached = append(attached, chFirst(ag.Name, ag.ID))
	}
	return attached, unknown
}

// findAgentByNameOrID resolves an agent the caller owns, by id first
// then case-insensitive name — the same lookup order the pipeline tool's
// attach pass uses.
func (t *chatTurn) findAgentByNameOrID(key string) (AgentRecord, bool) {
	if ag, ok := loadAgent(t.udb, key); ok && (ag.Owner == "" || ag.Owner == t.user) {
		return ag, true
	}
	for _, ag := range listAgents(t.udb, t.user) {
		if strings.EqualFold(ag.Name, key) {
			return ag, true
		}
	}
	return AgentRecord{}, false
}

func (t *chatTurn) machineList() (string, error) {
	defs := ListMachineDefs(t.udb, t.user)
	if len(defs) == 0 {
		return "You have no machines. machine(action=\"create\", name=…, phases=[…]) authors one; call action=\"help\" first for the spec.", nil
	}
	// Which agents point at what, so the answer to "what do I have" also
	// answers "and is any of it live" — an unattached machine is inert,
	// and that is the single most useful thing to know about one.
	users := map[string][]string{}
	for _, ag := range listAgents(t.udb, t.user) {
		if ag.Machine != "" {
			users[ag.Machine] = append(users[ag.Machine], chFirst(ag.Name, ag.ID))
		}
	}
	var b strings.Builder
	for _, d := range defs {
		fmt.Fprintf(&b, "- %s (id=%s) — %d phase%s: %s. Starts in %s.",
			d.Name, d.ID, len(d.Phases), plural(len(d.Phases)), strings.Join(d.PhaseNames(), ", "), d.StartPhase())
		if desc := strings.TrimSpace(d.Description); desc != "" {
			fmt.Fprintf(&b, " %s", desc)
		}
		if who := users[d.ID]; len(who) > 0 {
			fmt.Fprintf(&b, " Used by: %s.", strings.Join(who, ", "))
		} else {
			b.WriteString(" Not attached to any agent (inert).")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (t *chatTurn) machineGet(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", errors.New("no machine found by that name or id — machine(action=\"list\") shows what you have")
	}
	full := boolArg(args, "full")
	view := struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Start       string         `json:"start"`
		Phases      []MachinePhase `json:"phases"`
	}{def.ID, def.Name, def.Description, def.StartPhase(), def.Phases}
	if !full {
		// Preview prompts. Reading back a machine you wrote this session
		// to change one field shouldn't re-pay every prompt in context.
		view.Phases = append([]MachinePhase(nil), def.Phases...)
		for i := range view.Phases {
			view.Phases[i].Prompt = previewText(view.Phases[i].Prompt, 160)
		}
	}
	b, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", err
	}
	out := string(b)
	if !full {
		out += "\n\n(phase prompts previewed — call again with full=true to read them in full)"
	}
	return out, nil
}

func (t *chatTurn) machineDelete(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", errors.New("no machine found by that name or id")
	}
	detached := detachMachineFromAgents(t.udb, t.user, def.ID)
	DeleteMachineDef(t.udb, def.ID)
	out := fmt.Sprintf("Deleted machine %q (id=%s).", def.Name, def.ID)
	if len(detached) > 0 {
		out += " Detached from " + strings.Join(detached, ", ") + "."
	}
	out += " Sessions already parked in it keep their history and run as ordinary agent turns from here."
	return out, nil
}

// detachMachineFromAgents clears a machine off every agent pointing at
// it, returning the ones changed.
//
// Every delete path must do this, or the next session on one of those
// agents opens pointing at a machine that no longer exists and quietly
// runs as a plain agent. The turn breadcrumbs it (machine_missing), but
// a breadcrumb is a poor substitute for the agent simply still being
// what its author configured. Shared by the tool and the HTTP handler so
// the two can't drift on it.
func detachMachineFromAgents(udb Database, user, machineID string) []string {
	var detached []string
	for _, ag := range listAgents(udb, user) {
		if ag.Machine != machineID || ag.Owner != user {
			continue
		}
		ag.Machine = ""
		if _, err := saveAgent(udb, ag); err == nil {
			detached = append(detached, chFirst(ag.Name, ag.ID))
		}
	}
	return detached
}

// findMachine resolves a machine by id then case-insensitive name.
func (t *chatTurn) findMachine(args map[string]any) (MachineDef, bool) {
	if id := strings.TrimSpace(stringArg(args, "id")); id != "" {
		if d, ok := LoadMachineDef(t.udb, t.user, id); ok {
			return d, true
		}
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return MachineDef{}, false
	}
	for _, d := range ListMachineDefs(t.udb, t.user) {
		if strings.EqualFold(d.Name, name) {
			return d, true
		}
	}
	return MachineDef{}, false
}

// parseMachinePhases converts the LLM-supplied phases array into typed
// phases. Reuses parsePipelineFields for "output", so a machine phase
// and a pipeline stage declare structure identically — one vocabulary to
// learn, and the validator behind it is the same one.
func parseMachinePhases(raw any) ([]MachinePhase, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("phases must be an array of phase objects")
	}
	out := make([]MachinePhase, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("phase %d must be an object {name, prompt, resident?, next?}", i+1)
		}
		fields, err := parsePipelineFields(i+1, m["output"])
		if err != nil {
			return nil, err
		}
		out = append(out, MachinePhase{
			Name:     strings.TrimSpace(mapStr(m, "name")),
			Desc:     strings.TrimSpace(mapStr(m, "desc")),
			Prompt:   mapStr(m, "prompt"),
			Tools:    mapStrList(m, "tools"),
			Model:    strings.ToLower(strings.TrimSpace(mapStr(m, "model"))),
			Think:    normalizePhaseThink(m["think"]),
			Output:   fields,
			Resident: mapBool(m, "resident"),
			Next:     strings.TrimSpace(mapStr(m, "next")),
			NextFrom: strings.TrimSpace(mapStr(m, "next_from")),
			Choices:  mapStrList(m, "choices"),
			// Agent was missing here, so a machine written through this
			// tool could never delegate a step and an existing one lost
			// its delegate on the next update.
			Agent: strings.TrimSpace(mapStr(m, "agent")),
			Guard:    strings.TrimSpace(mapStr(m, "guard")),
			GuardTo:  strings.TrimSpace(mapStr(m, "guard_to")),
			Keep:     mapStrList(m, "keep"),
			ExitsTo:  mapStrList(m, "exits_to"),
		})
	}
	return out, nil
}

// normalizePhaseThink accepts the tri-state string the field actually is
// AND the boolean the model will reach for anyway, because "think": true
// is what every neighbouring surface (pipeline stages, agent config in
// prose) has trained it to write. Rejecting that costs a round to learn
// a distinction the author does not care about.
func normalizePhaseThink(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case bool:
		if v {
			return "on"
		}
		return "off"
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		switch s {
		case "true", "yes", "on":
			return "on"
		case "false", "no", "off":
			return "off"
		}
		return s // let Validate reject anything else, by name
	}
	return ""
}

// previewText caps a prompt for the compact `get` view.
func previewText(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "… (" + strconv.Itoa(len(s)) + " chars)"
}
