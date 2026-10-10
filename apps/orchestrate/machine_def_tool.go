// `machine` — grouped tool for session-resident phase machines
// (core.MachineDef). Same shape as the `pipeline` tool, for the same
// reason: a multi-phase recipe is authored, revised, and attached, and
// the house pattern for authoring a recipe is a chat tool rather than a
// bespoke editor page.
//
//	create / update — author a machine (name, description, start, phases[])
//	list            — see the user's machines
//	get             — read one machine's full definition
//	run             — run an UNATTENDED machine once, here, on an input
//	delete          — remove one
//
// A conversational machine has nowhere to run outside a session: it runs
// when a turn arrives on an agent that points at it, which is why
// attach_to_agents matters more here than on a pipeline. An unattended one
// RUNS, and run is how its author tries it. Without it Builder had no way to
// try a machine it had just built: agents(run) only lists what was
// dispatchable when the turn started, and the detours it reached for (a
// one-off schedule, a wrapping pipeline) either never reported back or had
// no machine runner at all.
//
// See docs/agent-machines.md.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

func (t *chatTurn) machineGroupedToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "machine",
			Description: "Author phase machines: workflows an agent LIVES IN across a conversation. The session remembers which phase it is in between turns, and what earlier phases decided. Actions: create, update, update_phase, validate, run, list, get, delete.\n\n`update` REPLACES the whole phase list; to change one field of one step use `update_phase`. Run `validate` first: a refused `update` stores NOTHING.\n\nUse a machine when a conversation should work out what is asked once and then settle into that frame, or when EVERY message must take a path (route it, hand some kinds to another agent): then set route_each_message and give the routing step its choices. Set unattended: true for a machine that RUNS start to finish with nobody in the conversation, given an input and returning its last step's result: it may have no resident step, and it is what a request for an \"unattended machine\" means. Try one you built with action=\"run\", name and input (the person's example). Use a PIPELINE for a fixed recipe of model stages that returns a result, and neither for a one-off question.\n\n**Pass `attach_to_agents` in the same call**: an unattached machine does nothing. Call action=\"help\" for the full spec.",
			Parameters: map[string]ToolParam{
				"action":      {Type: "string", Description: "One of: create | update | update_phase | validate | run | list | get | repair | delete | help."},
				"name":        {Type: "string", Description: "Machine name. Required for create; get/update/run/repair/delete also accept the id."},
				"id":          {Type: "string", Description: "(update/get/run/delete) Machine id, if you have it instead of the name."},
				"input":       {Type: "string", Description: "(run) What the run is started with, read as {input}. Pass the example the person gave, as they would."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the machine is for."},
				"start":       {Type: "string", Description: "(create/update) Name of the phase a fresh session enters. Defaults to the first phase in the list."},
				"full":        {Type: "boolean", Description: "(get) When true, return every phase's full prompt. Default false previews them to save context."},
				"phase":       {Type: "string", Description: "(update_phase) Which step to change. Only the fields you pass are written; every other field of that step, and every other step, is left exactly as it was."},
				"tools": {
					Type:        "array",
					Description: "(update_phase) Exact tool names this step may reach, applied on top of its reach. Leave it empty unless the person named these tools or agreed when you asked: empty keeps everything the agent has, and the agent picks the tool when the step runs. Pass an EMPTY array to clear the list, which makes the step inherit the whole catalog again, that is the only way to say it, since an omitted list and an empty one are the same value once parsed. Note that a non-empty list drops framework-provided tools it does not name (knowledge_search, fetch_knowledge_doc, ask_user), so name those here if the step's prompt calls for them.",
					Items:       &ToolParam{Type: "string"},
				},
				"deny": {
					Type:        "array",
					Description: "(update_phase) Tool names this step may NOT reach, subtracted last. Empty array clears.",
					Items:       &ToolParam{Type: "string"},
				},
				"reach":              {Type: "string", Description: "(update_phase) \"all\" (everything the agent has), \"read\" (only what reads: nothing that writes, runs, or reaches the network), or \"none\" (this step only decides). Unset, a transient step that names no tools reaches nothing; every other step reaches everything.", Enum: []string{"all", "read", "none"}},
				"prompt":             {Type: "string", Description: "(update_phase) The step's directive."},
				"desc":               {Type: "string", Description: "(update_phase) One-line summary of what the step is for."},
				"think":              {Type: "string", Description: "(update_phase) \"on\" or \"off\".", Enum: []string{"on", "off"}},
				"model":              {Type: "string", Description: "(update_phase) \"worker\" or \"lead\".", Enum: []string{"worker", "lead"}},
				"next":               {Type: "string", Description: "(update_phase) The phase a transient step hands to, or a waiting step moves to after its reply. null clears it. A step that branches uses choices instead."},
				"guard":              {Type: "string", Description: "(update_phase) Plain-language condition that moves the conversation out of this step."},
				"guard_to":           {Type: "string", Description: "(update_phase) Where the guard sends it."},
				"route_each_message": {Type: "boolean", Description: "(create / update) true: every new message starts at the first step, wherever the last one left off, with the last message's step results cleared. For a machine whose job is ROUTING each message. Omit to leave it as it is."},
				"unattended":         {Type: "boolean", Description: "(create / update) true: the machine RUNS rather than converses. Started with an input, it walks its steps until one hands off nowhere, and that step's result is the run's result. No step may be resident (nobody is there to answer). This is what lets it run from a schedule, the Run button, another machine or a dispatch. Omit to leave it as it is."},
				"reply_with":         {Type: "string", Description: "(update_phase) On a step the conversation waits in: a template that IS the reply, sent with no model call, e.g. {state:ComedianDelegate}. For relaying what an earlier step produced. null clears it."},
				"choices": {
					Type:        "array",
					Description: "(update_phase) The steps this one may hand the turn to, when it decides at run time: this is how a step BRANCHES. Replaces its next. [] clears it.",
					Items:       &ToolParam{Type: "string"},
				},
				"resident": {Type: "boolean", Description: "(update_phase) true: the conversation waits in this step and its reply goes to the person. false: it passes on to its next or choices."},
				"required": {Type: "boolean", Description: "(update_phase) true: this step is never skipped as not applicable; it does its work or the run fails. For a step whose absence would make the result wrong."},
				"machine_deny": {
					Type:        "array",
					Description: "(create / update) Tool names NO step of this machine may reach, wherever it runs. Omit to leave as is; [] clears.",
					Items:       &ToolParam{Type: "string"},
				},
				"phases": {
					Type:        "array",
					Description: "(create/update/validate) Ordered phases, each an object: {\"name\": unique label, \"desc\": one line, \"prompt\": the directive}. The KEY field is \"resident\": true marks a phase user turns come back to (a turn ENDS there); false/omitted marks a transient phase that runs, produces a result, and hands straight off inside the same turn. Every machine needs at least one resident phase. Transient phases declare \"output\": [{name,type,desc,required}] and hand off with \"next\", or, to decide at run time, list the phases they may hand to in \"choices\" (the framework declares the routing field itself, do not declare one, and do not list the options in a prompt). Resident phases may NOT declare output: their reply goes to the user. A resident phase with \"next\" gets ONE turn then hands off (an intake beat); without one it stays. Add \"guard\": a plain-language condition that, checked each turn, moves the conversation out (\"the user has moved on to a different subject\"), with \"guard_to\" naming where it goes. Per-phase \"reach\" (\"all\" for everything the agent has, \"read\", \"none\"; unset, a transient phase naming no tools reaches NOTHING and every other phase reaches everything; prefer this to naming tools; it survives being run by a different agent), \"tools\" (exact names on top of reach; empty inherits), \"deny\" (names this phase may NOT reach, subtracted last, the list for \"everything it had except this one\"), \"model\" (\"worker\"|\"lead\"), \"think\" (\"on\"|\"off\", OFF by default on a transient phase; turn it ON for one that genuinely judges, such as decomposing an ambiguous request or routing between close options). Prompts template a fixed set of built-ins, {input}/{original_input}/{established}/{prev}/{now}/{user}/{agent}/{step}/{machine} (transient only; the message AND the earlier findings are supplied anyway if you never place them) and {state:PHASE} / {state:PHASE.field} (anywhere). **Call action=\"help\" for the full spec.**",
					Items:       &ToolParam{Type: "object"},
				},
				"attach_to_agents": {
					Type:        "array",
					Description: "(create/update) Agent names or IDs to point at this machine, replacing whatever machine each one had. An agent runs at most ONE machine, so this is a set, not an add. Sessions already open keep the machine they started with; new sessions get this one. Unknown names are reported back and the machine still saves.",
					Items:       &ToolParam{Type: "string"},
				},
			},
			Required: []string{"action"},
			// CapNetwork: run walks the machine's steps, and a step's tools,
			// delegates and pipelines can reach the network. Tagged so private
			// mode filters it, as the pipeline tool is for the same reason.
			Caps: []Capability{CapRead, CapNetwork},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
			case "update_phase":
				return t.machineUpdatePhase(args)
			case "validate", "check":
				return t.machineValidate(args)
			case "repair":
				return t.machineRepair(args)
			case "run", "try":
				return t.machineRun(args)
			case "help", "":
				return machineHelpText, nil
			default:
				return "", fmt.Errorf("unknown action %q: use create | update | update_phase | validate | run | list | get | repair | delete | help", action)
			}
		},
	}
}

const machineHelpText = `machine actions:
- create  {name, description?, start?, route_each_message?, unattended?, machine_deny?:[tools], phases:[...], attach_to_agents?:[names]}, author a machine.
- update  {name|id, ...}: revise in place (same id, attachments stay). REPLACES the phase list.
- validate {phases:[...]} or {name|id}: check WITHOUT writing. Reports what would refuse the save,
           tool names that resolve to nothing, and steps whose agent cannot reach what they name.
           Pass attach_to_agents to ask "would this work on that agent" before attaching it.
           Worth doing before any update, because a refused update stores nothing at all.
- list, your machines: [{id, name, description, phases, start}].
- get     {name|id, full?:true}, one machine's definition.
- run     {name|id, input}: run an UNATTENDED machine once, here, and get its result (or where it
           stopped and what it had produced). This is how to try one you just built: build it,
           then run it on the person's own example and read the result before calling it done.
           A conversational machine is not run; attach it to an agent and talk to that agent.
- repair  {name|id}: settle the findings with exactly one right answer (references to steps that
           are gone, a field filled from a variable but declared as a number). Anything with two
           defensible answers is left alone and still reported.
- delete  {name|id}.

An unattached machine does nothing: pass attach_to_agents, or the agent never enters it.

=== WHAT A MACHINE IS ===
A pipeline runs start-to-finish and returns a result. A machine is where a conversation SITS. The
session remembers which phase it is in between turns, plus a blackboard of what earlier phases
decided. The canonical shape is: work out what is being asked (once), pick an approach (once), then
answer in that frame for the rest of the thread: re-deciding only when the subject genuinely
changes.

Turn 1 runs the transient phases and then replies from the resident one. Turns 2+ go straight to the
resident phase, with the earlier decisions pinned into the prompt. A ROUTER is the other shape: every
message must be judged and sent somewhere (humor to one agent, the rest answered directly). Set
route_each_message: true and each new message starts at the first step again, with the previous
message's results cleared; no step's next has to point back. Those decisions are NOT chat
history: they are state, so turn 8 is not re-reading turn 1's reasoning.

An UNATTENDED machine is the third shape: set unattended: true and it RUNS instead of conversing.
It is started with an input ({input}), walks its steps until one hands off nowhere, and that last
step's result is the run's result. No step may be resident. Tool steps chain cheaply: each declares
the output fields a later step reads as {state:STEP.field}. It runs from action="run", the Run
button, a schedule, another machine's step or a dispatch, and attach_to_agents is not needed for
that. Build it, then machine(action="run", name, input) to try it with the person's example.

=== PHASE FIELDS ===
name       unique label; also the key others read as {state:NAME}. No dots.
desc       one line: what this phase is for. Shown to the guard and in the phase list.
prompt     the directive, layered on top of the agent's persona (it does not replace it)
resident   true = user turns land here and a turn ENDS here. At least one per machine.
reply_with (resident) a template that IS the reply, sent with no model call: {state:Step} for what
           an earlier step produced, {input} for the message. For a step that only passes along a
           delegate's answer: a model asked to relay rewrites it, re-delegates, or declines.
           Still judged by the agent's output rules; falls back to prompt if empty or stopped.
next       where control goes when this phase finishes
choices    (transient) [phase names] this phase may hand to; it DECIDES between them at run time.
           Prefer this over next_from: the framework declares the routing field (next_step) and
           writes the instruction naming each destination, so there is nothing to keep in sync.
next_from  (transient) one of THIS phase's declared string fields, whose value names the next phase.
           Only when the routing value is ALSO a finding worth naming. Overrides choices.
agent      (transient) delegate this phase to another agent by name or id
pipeline   (transient) run this phase THROUGH a stored pipeline, by name. Not with "agent"
           a step is run by one thing. A TOOL runs no model at all. An agent brings judgement, its own tools and its own
           memory; a pipeline is a fixed recipe (stages, fan out over a list, loop until a
           field goes true). Reach for a pipeline when the step is a procedure you want run
           the same way every time. A pipeline whose LAST stage declares the fields this
           phase declares costs one model call rather than two, because the shape it already
           produced becomes the phase's own.
machine    (transient) run this phase as a CHILD RUN of another machine, by name. Not with
           "agent" or "pipeline": one runner per step. The child must be marked unattended (it
           RUNS rather than converses), gets its own blackboard, and its result becomes this
           step's, so this step's "accumulates" folds it into the parent's working set. Depth is
           capped at one: a child may not run a child. Use it for work that is a smaller version
           of the SAME shape: a run that finds a gap and starts a run to fill it.
accumulates [{name, from, mode?, by?}]: the run-scoped LISTS this phase adds to. "from" is one of
           THIS phase's declared output fields; a list field contributes its elements, a scalar
           contributes itself. mode: append (default) | replace | union ("by" keys a union on one
           field of each element). The list lands on the blackboard under ITS OWN name, so many
           phases build one working set: read it with {state:LIST} (a numbered rendering),
           {state:LIST.items} (the list), {state:LIST.count}. A list may not share a name with a
           step. Use it for what a run collects (answers, sources, unanswered questions), and
           plain "output" for what ONE step decided.
output     [{name, type, desc, required, from}]: validated JSON. Transient phases only.
           A field NAMED after a built-in (original_input, now, user, agent, prev, step, machine)
           IS that built-in: it is filled from what the framework already holds and never asked
           of the model. Do not spend a prompt or a description on one: a value already known is
           not a judgement. Use "from" only to give such a value your OWN field name:
           {"name": "asked", "from": "{original_input}"}. Filled fields hold TEXT and are left out
           of the contract entirely.
           DECLARING these IS the structured-output mechanism. Never ask for JSON in the prompt,
           never describe a shape, never give an example object: the framework encodes the fields
           and validates what comes back. A prompt that also specifies a format is two sets of
           formatting rules, and the usual result is a JSON string nested inside a JSON field.
           Write the prompt to a person: say what to find, and let the fields say what to return.
guard      (resident) plain-language condition that moves the conversation out
guard_to   (resident) where a tripped guard goes; defaults to the start phase
exits_to   [phase names] this phase may be MOVED to by change_phase (the agent deciding mid-turn
           that the request moved on). Empty = anywhere, which is right for most machines. Use it
           when the machine BRANCHES and the arms must stay separate: without it every resident
           phase offers every other phase, so a conversation can cross from one arm to the other.
           Bounds the agent only: this phase's own next, and its guard's target, are always allowed.
keep       [phase names] whose state survives RE-ENTRY into this phase; empty keeps everything
tool       a tool this step CALLS DIRECTLY, with "args", and no model runs at all. The cheap step:
           every other runner thinks first, so "fetch this one thing and carry on" cost a model
           call to decide to do the only thing it could do. Args are templated ({input}, {prev},
           {state:PHASE.field}), a placeholder fills a VALUE and can never become a key. A runner
           like agent/pipeline/machine, so it excludes them, and it cannot be resident.
           {state:STEP.field} reads only fields STEP declares: a tool step whose JSON result a
           later step needs declares "output" fields, and they are decoded from what the tool
           returned. An argument whose placeholder comes out EMPTY skips the step as not
           applicable (the run moves on to next) instead of calling the tool with a blank.
           Unattended runs reach the owner's own tools, except those turned off, marked never
           unattended, or behind a credential that asks before every call; validate says which.
required   true = never skipped. A step that runs its own model with tools is otherwise given
           skip_step, to say it does not apply (nothing to act on, no tool that fits) rather than
           force a call; it moves on to next. Mark required where skipping would make the result
           wrong. machine_deny on the machine removes tools from every step, wherever it runs.
reach      how much of the agent's catalog this phase may touch: "all" = all of it, "read" = only
           tools that read (nothing that writes, runs a command, or reaches the network), "none" =
           nothing, which is right for a phase that only decides or reshapes what it was given.
           UNSET depends on the phase: a transient phase that names no tools, denies none and
           hands its work to no agent/pipeline/machine reaches NOTHING (it is one cheap call, not
           a tool loop); every other phase reaches everything. So a transient phase that has to
           look something up says reach "all" or "read", or names its tools.
           PREFER THIS over naming tools. A catalog is assembled per turn out of things that move
           an MCP server publishes its tools when it connects, a credential mints its own per
           session, an attachment mints more per agent, and a machine is portable across all of
           them, so a name list written here describes one deployment and misdescribes the next.
           A capability travels.
tools      what this phase may use, BY NAME, on top of whatever reach allowed. Empty INHERITS
           whatever the reach allowed, and empty is the default to keep: name tools only when the
           person named them or agreed when asked, and let the agent choose at run time. Naming any tool narrows to those, plus the workflow controls,
           which never go away, plus whatever the agent's attached SOURCES grant, which attaching
           is what granted; name one of a source's own tools and the list governs those too. For a
           phase that only decides or reshapes what it was given, list the single name "__none__":
           it reaches nothing and skips building a catalog it will not use. A phase needing
           different REACH (its own persona, memory, tools) should be delegated with "agent".
           Names must match the agent's catalog EXACTLY: a phase naming a tool nobody has reaches
           nothing under that name, so use list_reference_sources / the agent's own tool list
           rather than a plausible-looking guess.
deny       names this phase may NOT reach, subtracted LAST, after reach, after tools. The list to
           use when a step keeps everything it has EXCEPT one thing: "tools" can only say that by
           enumerating the catalog minus one, which freezes the step at the catalog of the day it
           was written, while a deny keeps the step current and holds back only what it names.
           It only subtracts, so a deny naming a tool this deployment does not have is satisfied
           rather than a mistake. The workflow controls cannot be denied: a step that cannot
           change_phase is stranded, not restricted.
model      "worker" | "lead"    think   "on" | "off"

=== TRANSIENT vs RESIDENT ===
Transient = runs and hands off inside one turn; the user never takes a turn in it. Decompose, route,
classify, plan. It MUST hand off (next or next_from).
Resident = the conversation lives here. Answer, converse, execute. It may NOT declare output (its
reply goes to the person, not a decoder) and may not use {input}, {prev} or {now} in its prompt
(pinned across turns; the message is already in the conversation). The stable variables
{original_input}, {user}, {agent}, {step}, {machine}: work there.
A resident phase with "next" gets exactly ONE turn and then hands off, that is how you write an
intake beat that asks its questions and moves on.

=== ROUTING ===
Static: "next": "answer".
Deciding: "choices": ["hunch", "answer"], the phase picks one at run time. Do NOT declare a field
for it and do NOT list the options in a prompt or a description: the framework declares next_step
with those values, writes the instruction naming each destination and what it is for, and rejects a
choice that is not a phase when the machine is SAVED. Keep "next" as the fallback.
By hand: declare a string field and point "next_from" at it, when the value is also a finding worth
naming. Give that field "enum": [phase names] to declare where it may send the turn, that is what
gets the arrows drawn, makes a name that is not a phase a save-time error, and constrains the reply
where the decoder can still repair it. "choices" does all of that for you. If the model returns a name that does not exist at run time, the machine falls back to
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
A fixed vocabulary of primitives, no declaring, no naming, same meaning in every machine:
{input} the person's message this turn - {original_input} the message that opened the conversation -
{established} everything earlier phases worked out - {prev} the phase run just before, this turn -
{now} the date and time where the person is - {user} - {agent} - {step} - {machine}.
Transient phases only (a resident phase's prompt is pinned across turns).
You do not have to place {input} or {established}: a transient phase is handed the message when its
prompt mentions none, and the blackboard when it places no {state:…} reference of its own. Reach for
{state:PHASE.field} only when you need ONE value inside a sentence.
{state:NAME} a phase's reply - {state:NAME.field} one declared field, anywhere, any turn.
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
JUDGES: decomposing an ambiguous request, routing between approaches that are close together,
weighing evidence. Leave it off for a phase that transforms or classifies something already clear.
Same rule pipeline stages follow, and decomposition is the case that most often earns it.

The guard never reasons; that is not configurable. It is a cheap check in front of the turn, and a
guard that needs deliberation is really a transient phase.

The RESIDENT phase inherits the agent's own think setting, so the reply reasons if the agent does.
Set "think" on a resident phase only to differ from the agent: a fast lookup phase inside an
otherwise deliberate agent, or the reverse.

=== COST ===
Transient phases are extra model calls before the user sees anything, so keep them few, and cheap
unless the phase is doing real judgment (see THINKING). A guard is one small call per turn in that
phase. The resident phase's own turn costs exactly what an ordinary agent turn costs: the machine
adds nothing to the turns it is not doing work on.`

// machineCreateOrUpdate parses the phases array and saves a MachineDef.
// Mirrors pipelineCreateOrUpdate, including the upsert-on-update path:
// after a REFUSED create nothing was stored, and the reflex is to "fix
// it" with update, which would otherwise spend a round discovering there
// is nothing to fix.
// machineDraft is the definition a call DESCRIBES, before anything is written.
//
// Split out of machineCreateOrUpdate so validate builds its candidate through
// the same code the save does. A validate that assembled the definition even
// slightly differently would be worse than none: its whole promise is "this is
// what a save would say", and the first time it was wrong about that nobody
// would trust it again.
type machineDraft struct {
	def MachineDef
	// isUpdate is what actually happened after the lookup, not what was asked:
	// an update naming nothing stored becomes a create.
	isUpdate         bool
	createdViaUpdate bool
}

// machineDraftFromArgs assembles the draft, resolving an update against what is
// stored. It writes nothing.
func (t *chatTurn) machineDraftFromArgs(args map[string]any, isUpdate bool) (machineDraft, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" && !isUpdate {
		return machineDraft{}, errors.New("name is required to create a machine")
	}
	phases, err := parseMachinePhases(args["phases"])
	if err != nil {
		return machineDraft{}, err
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
		case name == "" && strings.TrimSpace(stringArg(args, "id")) == "":
			return machineDraft{}, errors.New("name the machine to update (name or id). machine(action=\"list\") shows what you have")
		default:
			return machineDraft{}, errors.New("no matching machine to update: nothing is stored under that name/id, and this call carries no phases to store as a new one. machine(action=\"list\") shows what you actually have")
		}
	} else {
		// A second machine under a name already in use is never what an
		// author means, and a name then points at whichever one a lookup
		// finds first. Observed: asked to recreate a machine, an author
		// created a twin, attached "the" machine by name, and the agent
		// stayed on the old one.
		if existing, ok := t.findMachine(map[string]any{"name": name}); ok {
			return machineDraft{}, fmt.Errorf("a machine named %q already exists (id=%s). Change it with action=\"update\" (name=%q), or to start over, delete it first (action=\"delete\") and then create", existing.Name, existing.ID, existing.Name)
		}
		def = MachineDef{Name: name, Owner: t.user}
	}
	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		def.Description = d
	}
	if s := strings.TrimSpace(stringArg(args, "start")); s != "" {
		def.Start = s
	}
	if v, present := args["route_each_message"]; present {
		def.RouteEachMessage = v == true || strings.EqualFold(strings.TrimSpace(fmt.Sprint(v)), "true")
	}
	// The editor page had the only switch for this, so a machine Builder
	// authored could never run on its own: asked for an unattended machine,
	// it built a pipeline or an agent instead.
	if v, present := args["unattended"]; present {
		def.Unattended = v == true || strings.EqualFold(strings.TrimSpace(fmt.Sprint(v)), "true")
	}
	if _, present := args["machine_deny"]; present {
		def.Deny = mapStrList(args, "machine_deny")
	}
	if len(phases) > 0 {
		def.Phases = phases
	}
	def.Owner = t.user
	return machineDraft{def: def, isUpdate: isUpdate, createdViaUpdate: createdViaUpdate}, nil
}

// machineValidate answers every question a save answers, and writes nothing.
//
// Until this existed there was no way to CHECK a machine: the actions were
// create, update, update_phase, list, get, repair, delete, and the only route
// to the findings was to save and read what came back. Authoring was therefore
// write-blind by construction, and three separate costs followed from that one
// gap:
//
//   - `update` REPLACES the whole phase list, so a restructure re-sends every
//     phase, and a single unmeant field refuses the entire save. Observed twice
//     in one session on a step the author had copied forward verbatim.
//   - `get` omits defaults (reach is `json:",omitempty"`), so a read-modify-
//     write round trip needs the author to know which absences were defaults.
//   - the findings that do NOT refuse a save — a tool name nothing resolves,
//     a step whose reach removes what it names — arrive as prose attached to a
//     success, which is the worst place to put something that has a run-time
//     consequence.
//
// It never returns an error. An action whose entire job is to say what is wrong
// must not fail instead of saying it, so even an unparseable phase list comes
// back as a verdict.
func (t *chatTurn) machineValidate(args map[string]any) (string, error) {
	// isUpdate=true so a bare {name} checks what is STORED. That is the form
	// worth having after something ELSE changed: an agent's allowlist is
	// rewritten somewhere far from here, and the phases that named those tools
	// are only wrong afterwards.
	// A phase list with no machine named is a candidate on its own, which the
	// help promises: validate {phases:[...]}. It used to fall into the update
	// branch, find nothing by name, and report that the call carried no phases.
	var draft machineDraft
	var err error
	if _, sent := args["phases"]; sent && strings.TrimSpace(stringArg(args, "name")) == "" && strings.TrimSpace(stringArg(args, "id")) == "" {
		candidate := make(map[string]any, len(args)+1)
		for k, v := range args {
			candidate[k] = v
		}
		candidate["name"] = "(this phase list)"
		draft, err = t.machineDraftFromArgs(candidate, false)
	} else {
		draft, err = t.machineDraftFromArgs(args, true)
	}
	if err != nil {
		return "NOT VALID, and NOTHING WAS WRITTEN: " + err.Error(), nil
	}
	def := draft.def
	var b strings.Builder
	// phases is a list of OBJECTS, so presence is the test, not length: a
	// bare {name} is "check what is stored", which is a different question
	// from "check this candidate against what is stored".
	_, sent := args["phases"]
	what := "Checked " + strconv.Quote(def.Name)
	if def.ID != "" && !sent {
		what = "Checked the stored " + strconv.Quote(def.Name)
	} else if def.ID != "" {
		what = "Checked " + strconv.Quote(def.Name) + " with the phases in this call"
	}
	fmt.Fprintf(&b, "%s: %d phase%s (%s). NOTHING WAS WRITTEN.",
		what, len(def.Phases), plural(len(def.Phases)), strings.Join(def.PhaseNames(), ", "))
	runnable := def.Validate()
	if runnable != nil {
		fmt.Fprintf(&b, "\n\n⚠ A save WOULD BE REFUSED: %v\nFix that first; a refused update stores nothing at all, not even the phases that were fine.", runnable)
	} else {
		fmt.Fprintf(&b, " A create/update with these phases WOULD SAVE, and a session would start in %s.", def.StartPhase())
	}
	// The per-agent preflight, which the create/update path never ran: its
	// findings come from the generous "does anybody hold this name" catalog,
	// and the question that decides whether a step works is what ONE agent
	// carries. Agents named in this call are checked without being touched.
	gaps := t.machineAttachGapsForNamed(args, def)
	if def.ID != "" {
		gaps = append(gaps, machineAttachGapsForAll(t.udb, t.user, def)...)
	}
	if len(gaps) > 0 {
		b.WriteString("\n\nSteps that name tools the agent running them cannot reach (the step runs WITHOUT them, or with the FULL catalog when it loses every name it lists):\n- " +
			strings.Join(dedupeStrings(gaps), "\n- "))
	}
	findings := t.machineFindingsNote(def)
	b.WriteString(findings)
	// Said out loud, because otherwise "checked and clean" and "the check did
	// not run" are the same output, and the whole point of the action is to be
	// able to trust a quiet answer.
	if len(gaps) == 0 && findings == "" && runnable == nil {
		b.WriteString("\n\nNothing else to report.")
	}
	return b.String(), nil
}

// machineAttachGapsForNamed runs the attach preflight against the agents an
// attach_to_agents argument names, WITHOUT attaching them.
//
// attachMachineToAgents saves, which a validate must never do. Same preflight,
// same wording, no write — so "will this machine work on that agent" can be
// asked before the answer costs anything.
func (t *chatTurn) machineAttachGapsForNamed(args map[string]any, def MachineDef) []string {
	names, _ := args["attach_to_agents"].([]any)
	var out []string
	for _, n := range names {
		key := strings.TrimSpace(fmt.Sprint(n))
		if key == "" {
			continue
		}
		ag, why := t.ownAgentByNameOrID(key)
		if why != "" {
			out = append(out, why)
			continue
		}
		out = append(out, machineAttachGaps(t.udb, t.user, def, ag)...)
	}
	return out
}

// dedupeStrings keeps first-seen order. A machine validated against an agent
// named in the call AND already attached to it would otherwise report the same
// gap twice, which reads as two problems.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (t *chatTurn) machineCreateOrUpdate(args map[string]any, isUpdate bool) (string, error) {
	draft, err := t.machineDraftFromArgs(args, isUpdate)
	if err != nil {
		return "", err
	}
	def, isUpdate, createdViaUpdate := draft.def, draft.isUpdate, draft.createdViaUpdate
	if err := def.Validate(); err != nil {
		return "", fmt.Errorf("machine is not runnable: %w. machine(action=\"validate\") checks a phase list without writing it, which is the cheaper way to find this: an update REPLACES the whole list, so one bad field costs the entire save", err)
	}
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
		fmt.Fprintf(&b, " Pointed %s at it: new sessions on %s will run it (sessions already open keep what they started with).",
			strings.Join(attached, ", "), target)
	case len(unknown) == 0:
		// "Not attached" used to mean only that this call attached nothing,
		// so every edit to a machine in use said nothing ran it, and an
		// author told the owner to attach a machine their agent was running.
		if users := t.machineUsers(saved.ID); len(users) > 0 {
			fmt.Fprintf(&b, " Already run by %s: new sessions there run this version (sessions already open keep what they started with).", strings.Join(users, ", "))
		} else {
			b.WriteString(" NOT attached to any agent yet, so nothing runs it: pass attach_to_agents, or point an agent at it.")
		}
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, " No agent found named: %s.", strings.Join(unknown, ", "))
	}
	b.WriteString(t.machineFindingsNote(saved))
	return b.String(), nil
}

// machineFindingsNote reports what the editor's checklist and its
// "worth a look" panel show, in the tool's reply.
//
// Two halves, and the second one is why this is a method rather than a
// function on the definition. Advice() is what the machine looks like it
// might not have meant, and it is pure — a MachineDef can compute it
// alone. The tool-name findings cannot: whether "jira" is a tool depends
// on the catalog this user's agents actually assemble, so answering needs
// the database and the user, which only the turn has.
//
// Leaving the second half out was a real hole, not a tidy split. Validate
// refuses anything with Problems, so the RULES half is genuinely always
// empty here — but a phase tool name that resolves to nothing is not a
// problem by that definition, and it is the most expensive mistake on
// this path. The step keeps running; it just silently runs UNNARROWED,
// with the whole catalog, which is the opposite of what the list was
// written for. Observed: a phase naming [servitor jira confluence gitlab]
// — four systems, not four tools — ran with 115 tools and the model spent
// the turn insisting it could not reach files it was holding the tools
// for. The web editor said so in two lines; the tool, which is where a
// MODEL authors, said nothing.
//
// The page has shown the advice half since the editor existed; the tool
// showed nothing, which is backwards. The author on this path is a MODEL,
// and the most common finding — "the prompt asks for JSON, but this step
// already declares fields" — is a mistake only a model makes. The help
// text warns about it in advance, at the top of a long spec, and then
// nothing checked afterwards.
//
// Same functions behind both surfaces, so the tool cannot report a
// different machine than the page does.
func (t *chatTurn) machineFindingsNote(def MachineDef) string {
	findings := unknownPhaseToolFindings(t.udb, t.user, def)
	// A step whose reach removes what it names belongs with the names that do
	// not resolve, not with the advice: both are the machine unable to do what
	// it says, and neither is a matter of taste. It reaches Builder here or
	// nowhere — a model authoring through this tool never sees the page.
	findings = append(findings, machineReachConflicts(t.user, def)...)
	return machineFindingsText(findings, machineAdvice(t.udb, t.user, def))
}

// machineFindingsText is the wording, split from the gathering so the
// reply can be tested against findings written out by hand rather than
// against whatever the process has registered.
//
// Catalog findings lead. They are the half with a consequence at RUN
// time, and a reader stops at the first list.
func machineFindingsText(catalog, advice []string) string {
	var out string
	if len(catalog) > 0 {
		out += "\n\nThese tool names do not resolve, and nothing will tell you again at run time:\n- " +
			strings.Join(catalog, "\n- ")
	}
	if len(advice) > 0 {
		out += "\n\nWorth a look, none of this stopped the save, and none of it is certain:\n- " +
			strings.Join(advice, "\n- ")
	}
	return out
}

// attachMachineToAgents points each named agent at a machine. Returns
// the agents changed and the names that matched nothing.
//
// An agent runs at most one machine, so this SETS rather than appends —
// the asymmetry with attached_pipelines is real and the tool description
// says so, because "attach" reading as "add to a list" is how someone
// ends up expecting two machines to run at once.

// normalizeReach maps the sayable spelling of a reach onto the stored one.
//
// "all" is what the schema enum offers and what every success message prints
// back, because the enum cannot offer "" — an empty enum value makes Gemini
// reject the whole request, disabling every tool for that turn (see
// TestNoEmptyEnumValuesInSource) — and "omit the param for the default" does
// not work where an omitted field means "leave it alone" and widening a
// narrowed step back to everything has to be sayable.
//
// The stored value for "inherit everything" was once the empty string, so the
// two spellings had to be translated somewhere. It is "all" now, because empty
// became "unset" and resolves by the step's kind (see PhaseReach). update_phase did it and the
// whole-machine create/update path did not, which made "all" a word the
// framework teaches and then refuses: the author reads `reach = all (inherits
// everything the agent has)` out of one call, passes it to the next, and core
// answers `reach must be "read", "none", or empty to inherit everything, got
// "all"`. Observed on a 3-phase-to-5 restructure, twice in one session, where
// the rejected field belonged to a step the author had copied forward verbatim
// and never meant to touch — and a whole-machine update is all-or-nothing, so
// one unmeant field cost the entire save.
//
// Lives here, at the parse boundary, because every write path goes through one
// of these two parsers and a normalizer further in would have to be found by
// whoever adds the third.
func normalizeReach(v string) string {
	if r := strings.ToLower(strings.TrimSpace(v)); r != "all" {
		return r
	}
	return ReachAll
}

// machineUsers names the owner's agents that already point at a machine,
// the same reading the Machines page shows as "Used by".
func (t *chatTurn) machineUsers(machineID string) []string {
	var out []string
	for _, ag := range listAgents(t.udb, t.user) {
		if ag.Machine == machineID {
			out = append(out, chFirst(ag.Name, ag.ID))
		}
	}
	return out
}

func (t *chatTurn) attachMachineToAgents(raw any, machineID string) (attached, unknown []string) {
	names, _ := raw.([]any)
	for _, n := range names {
		key := strings.TrimSpace(fmt.Sprint(n))
		if key == "" {
			continue
		}
		ag, why := t.ownAgentByNameOrID(key)
		if ag.ID == "" {
			if strings.HasPrefix(why, "no agent found") {
				unknown = append(unknown, key)
			} else {
				unknown = append(unknown, key+" ("+why+")")
			}
			continue
		}
		if ag.Machine == machineID {
			attached = append(attached, chFirst(ag.Name, ag.ID))
			continue
		}
		// After the no-op check: an agent already on this machine has nothing
		// to approve.
		if msg := agentChangeGate(t.chatAsker(), t.udb, &ag, t.user, fmt.Sprintf("run it as machine %q", machineID)); msg != "" {
			unknown = append(unknown, key+" ("+msg+")")
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

// ownAgentByNameOrID resolves an agent the caller owns, by id first then
// case-insensitive name. why is empty on success and otherwise says what went
// wrong: no such agent, or a name several of the caller's agents answer to.
// That used to be the first match, so attaching a machine to a duplicated
// name put it on whichever agent listed first.
func (t *chatTurn) ownAgentByNameOrID(key string) (ag AgentRecord, why string) {
	if a, ok := loadAgent(t.udb, key); ok && (a.Owner == "" || a.Owner == t.user) {
		return a, ""
	}
	var hits []AgentRecord
	for _, a := range listAgents(t.udb, t.user) {
		if strings.EqualFold(a.Name, key) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 0:
		return AgentRecord{}, "no agent found named " + strconv.Quote(key)
	case 1:
		return hits[0], ""
	}
	ids := make([]string, 0, len(hits))
	for _, a := range hits {
		ids = append(ids, a.ID)
	}
	return AgentRecord{}, fmt.Sprintf("%d of your agents are named %q, so nothing was done: use the id of the one you mean (%s)", len(hits), key, strings.Join(ids, ", "))
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
		fmt.Fprintf(&b, "- %s (id=%s), %d phase%s: %s. Starts in %s.",
			d.Name, d.ID, len(d.Phases), plural(len(d.Phases)), strings.Join(d.PhaseNames(), ", "), d.StartPhase())
		if desc := strings.TrimSpace(d.Description); desc != "" {
			fmt.Fprintf(&b, " %s", desc)
		}
		if who := users[d.ID]; len(who) > 0 {
			fmt.Fprintf(&b, " Used by: %s.", strings.Join(who, ", "))
		} else {
			b.WriteString(" Not attached to any agent (inert).")
		}
		// A STORED machine can have problems even though create refuses
		// them: an import, a partial save from the editor, an older
		// version. Counting them here is what makes "fix my machine" a
		// question this tool can start answering.
		if n := len(d.Problems()); n > 0 {
			fmt.Fprintf(&b, " %d thing%s to fix (get it for the list).", n, plural(n))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (t *chatTurn) machineGet(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", machineNotFound(args)
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
		out += "\n\n(phase prompts previewed: call again with full=true to read them in full)"
	}
	// Both halves here, because a stored machine can carry problems that
	// create would have refused — this is the surface somebody reaches
	// for when asked to fix one.
	if probs := def.Problems(); len(probs) > 0 {
		out += fmt.Sprintf("\n\nStill missing (%d), it is stored and it runs degraded until these are settled:\n- %s",
			len(probs), strings.Join(probs, "\n- "))
		if fixable := def.Repairs(RepairAll); len(fixable) > 0 {
			out += fmt.Sprintf("\n\n%d of those have exactly one right answer (references to steps that are gone, and the like). "+
				"machine(action=\"repair\", name=%q) settles them and leaves everything that needs a judgement alone.",
				len(fixable), def.Name)
		}
	}
	out += t.machineFindingsNote(def)
	return out, nil
}

// machineUpdatePhase changes named fields on ONE phase, leaving the rest of the
// machine exactly as it was.
//
// update replaces the whole phases array (def.Phases = phases), which is right
// when you are authoring a machine and wrong for every small edit afterwards.
// To clear one phase's tool list you had to re-send every phase in full, and
// any field parseMachinePhases does not read is dropped on the way through — so
// the cost of fixing one list was risking the rest of the machine. Same shape
// as the temp-tool round-trip that quietly dropped fields.
//
// PRESENCE is the intent. An omitted field is left alone; a field that arrives
// is written, including an empty one. That is what makes "clear this list"
// sayable at all: tools=[] means the step inherits the catalog again, and there
// is no other way to say it — an omitted tools and an empty tools are the same
// value once they reach a Go slice, so only the args map can tell them apart.
//
// Structural changes stay on update: a step's output fields, what it delegates
// to, its branching. Those reshape what the step IS, the whole-phase form is
// the honest way to say so, and a partial edit spread across three calls would
// leave a machine that does not run in between.
func (t *chatTurn) machineUpdatePhase(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", machineNotFound(args)
	}
	want := strings.TrimSpace(stringArg(args, "phase"))
	if want == "" {
		return "", errors.New("name the phase to change (phase=\"<name>\"), this machine has: " + strings.Join(def.PhaseNames(), ", "))
	}
	idx := -1
	for i, ph := range def.Phases {
		if strings.EqualFold(strings.TrimSpace(ph.Name), want) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", errors.New("no phase named " + strconv.Quote(want) + " in " + strconv.Quote(def.Name) +
			", it has: " + strings.Join(def.PhaseNames(), ", "))
	}

	// Every key this call carries must be one it writes. A field it does not
	// handle used to be dropped while the reply said "Updated", so an author
	// who set resident or a branch condition was told it had worked.
	if unknown := unknownUpdatePhaseKeys(args); len(unknown) > 0 {
		// Fields that belong to the MACHINE get their own answer: they are
		// valid, just not here, and the generic refusal read as though they
		// were misspelled. Refused rather than half-applied, so a reply never
		// says "Updated" about a call that set only some of what it carried.
		if machineLevel := machineLevelKeys(unknown); len(machineLevel) > 0 {
			return "", errors.New("nothing was saved: " + strings.Join(machineLevel, ", ") + " belong(s) to the whole machine, not one step, and is set with " +
				"action=\"update\" (name, plus just those fields: the phases are kept when you send none). " +
				"Drop " + strings.Join(unknown, ", ") + " from this update_phase call and send it again for the step's own fields.")
		}
		return "", errors.New("update_phase does not change " + strings.Join(unknown, ", ") + ", so nothing was saved. " +
			"It changes: " + strings.Join(updatePhaseFieldNames, ", ") + ". " +
			"A step branches with choices (the steps it may pick between), not a condition field. " +
			"For output, agent, pipeline, machine, tool, keep or exits_to, use action=\"update\" with the whole phase list.")
	}

	ph := &def.Phases[idx]
	var changed []string
	setStr := func(key string, dst *string, lower bool) {
		v, present := args[key]
		if !present {
			return
		}
		// null means clear. fmt.Sprint(nil) is "<nil>", which was being
		// stored as the value: next=null named a step called "<nil>".
		s := ""
		if v != nil {
			s = strings.TrimSpace(fmt.Sprint(v))
		}
		if lower {
			s = strings.ToLower(s)
		}
		*dst = s
		if s == "" {
			changed = append(changed, key+" (cleared)")
			return
		}
		changed = append(changed, key)
	}
	setList := func(key string, dst *[]string) {
		if _, present := args[key]; !present {
			return
		}
		*dst = stringSliceArg(args, key)
		if len(*dst) == 0 {
			changed = append(changed, key+" (cleared: the step inherits the catalog again)")
			return
		}
		changed = append(changed, key+" = "+strings.Join(*dst, ", "))
	}

	if v, present := args["reach"]; present {
		r := normalizeReach(fmt.Sprint(v))
		switch r {
		case ReachAll, ReachRead, ReachNone:
		default:
			return "", errors.New("reach " + strconv.Quote(r) + " is not one of: \"all\" (everything the agent has), \"read\", \"none\"")
		}
		ph.Reach = r
		if r == ReachAll {
			changed = append(changed, "reach = all (inherits everything the agent has)")
		} else {
			changed = append(changed, "reach = "+r)
		}
	}
	// The prompt is the one field an empty value must NOT clear.
	//
	// Presence-is-intent is right for a list, where clearing is the whole point
	// and there is no other way to say it. It is wrong here: a step's prompt is
	// authored text with no undo, an empty one is a step that says nothing, and
	// a model that passes prompt:"" when it meant to omit the field would erase
	// work in a call that looked like it was about tools. Validate does not
	// object — it deliberately declines to judge prompt wording — so the guard
	// belongs here, where the intent is legible. Rewriting a prompt to empty on
	// purpose is still sayable through the whole-phase form.
	if v, present := args["prompt"]; present {
		if strings.TrimSpace(fmt.Sprint(v)) == "" {
			return "", errors.New("refusing to empty the prompt of step " + strconv.Quote(ph.Name) +
				", an empty prompt is a step that says nothing, and this call looks more like an omitted field than a deliberate erasure. " +
				"Pass the wording you want, or use action=\"update\" if you really mean to rewrite the step")
		}
		ph.Prompt = fmt.Sprint(v)
		changed = append(changed, "prompt")
	}
	setStr("desc", &ph.Desc, false)
	setStr("think", &ph.Think, true)
	setStr("model", &ph.Model, true)
	setStr("next", &ph.Next, false)
	setStr("reply_with", &ph.ReplyWith, false)
	setStr("guard", &ph.Guard, false)
	setStr("guard_to", &ph.GuardTo, false)
	setList("tools", &ph.Tools)
	setList("deny", &ph.Deny)
	if _, present := args["choices"]; present {
		ph.Choices = stringSliceArg(args, "choices")
		if len(ph.Choices) == 0 {
			changed = append(changed, "choices (cleared)")
		} else {
			changed = append(changed, "choices = "+strings.Join(ph.Choices, ", "))
		}
	}
	if v, present := args["resident"]; present {
		on := false
		switch b := v.(type) {
		case bool:
			on = b
		case string:
			on = strings.EqualFold(strings.TrimSpace(b), "true")
		}
		ph.Resident = on
		changed = append(changed, fmt.Sprintf("resident = %v", on))
	}
	if v, present := args["required"]; present {
		on := v == true || strings.EqualFold(strings.TrimSpace(fmt.Sprint(v)), "true")
		ph.Required = on
		changed = append(changed, fmt.Sprintf("required = %v", on))
	}

	if len(changed) == 0 {
		return "", errors.New("nothing to change: name at least one field (" + strings.Join(updatePhaseFieldNames, ", ") + "). " +
			"An omitted field is left alone; pass tools=[] to CLEAR a list")
	}
	if err := def.Validate(); err != nil {
		return "", fmt.Errorf("that change leaves the machine unrunnable, so nothing was saved: %w", err)
	}
	saved := SaveMachineDef(t.udb, def)
	Log("[orchestrate.machines] user=%q updated phase %q of machine %q: %s",
		t.user, saved.Phases[idx].Name, saved.Name, strings.Join(changed, "; "))
	out := fmt.Sprintf("Updated step %q of %q: changed %s. Every other step is untouched.",
		saved.Phases[idx].Name, saved.Name, strings.Join(changed, ", "))
	out += " Sessions already open keep the phase they are parked in; the change applies from their next turn."
	return out + t.machineFindingsNote(saved), nil
}

// machineRepair settles the findings with exactly one right answer.
//
// The page has had this button since v0.6.203 and the tool had no way
// to do it at all: a dangling reference could only be cleared by
// rewriting the whole machine through update, which is a large edit to
// fix something nobody chose. Same core function behind both, so the
// two surfaces cannot settle different things.
func (t *chatTurn) machineRepair(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", machineNotFound(args)
	}
	fixed := def.Repair(RepairAll)
	if len(fixed) == 0 {
		return "Nothing to repair in " + strconv.Quote(def.Name) + ", every finding it has needs a judgement, so none of them can be settled mechanically. " +
			"machine(action=\"get\") lists them.", nil
	}
	saved := SaveMachineDef(t.udb, def)
	Log("[orchestrate.machines] user=%q repaired machine %q via tool: %s", t.user, saved.Name,
		strings.Join(RepairLines(fixed), "; "))
	out := fmt.Sprintf("Repaired %d thing%s in %q:\n- %s",
		len(fixed), plural(len(fixed)), saved.Name, strings.Join(RepairLines(fixed), "\n- "))
	if probs := saved.Problems(); len(probs) > 0 {
		out += fmt.Sprintf("\n\nStill outstanding (%d), each because it has more than one defensible answer:\n- %s",
			len(probs), strings.Join(probs, "\n- "))
	}
	return out + t.machineFindingsNote(saved), nil
}

// machineRun runs one unattended machine to the end, inside this turn, and
// returns its result.
//
// The same run a kind=machine stage gets (runOwnedMachine): the owner's
// catalog, the full host, the same refusals. What this adds is what a caller
// in a turn owes: the warden on the way in and out, the approval question a
// narrowed agent is asked before a recipe reaches other agents, and a live
// run the Monitor can Stop. It runs on the turn's context because its answer
// is this tool call's result: once the turn is gone nobody is waiting for it.
// runTimeout is the ceiling the Run button has, for the same reason.
func (t *chatTurn) machineRun(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", machineNotFound(args)
	}
	if !def.Unattended {
		return "", errors.New(machineNotRunnableMessage(def))
	}
	input := strings.TrimSpace(stringArg(args, "input"))
	if input == "" {
		return "", errors.New("input is required to run a machine: pass what the run starts with, such as the example the person gave")
	}
	// Asked before anything is spent, and in full: a machine is routinely
	// saved half-built, and one finding at a time is a round per finding.
	if probs := machineRunProblems(t.udb, t.user, def); len(probs) > 0 {
		return "", fmt.Errorf("machine %q will not run yet, %d thing(s) outstanding:\n- %s\nFix them with action=\"update_phase\" or \"update\" (action=\"repair\" settles the mechanical ones), then run it again",
			def.Name, len(probs), strings.Join(probs, "\n- "))
	}
	if origin := t.dispatchOrigin; origin != nil && !origin.allowsMachine(def) {
		return "", fmt.Errorf("machine %q was not run: you are running on behalf of %q, whose dispatch policy does not permit that machine. Report back that it is built and could not be tried from here", def.Name, origin.AgentName)
	}
	for _, prior := range dispatchedMachines(t.ctx) {
		if prior == def.ID {
			return "", fmt.Errorf("machine %q is already running above this call; a step of it cannot run it again", def.Name)
		}
	}
	if err := t.guardMachineInput(t.ctx, def, input); err != nil {
		return "", err
	}
	if err := t.confirmRecipeEdge("machine", def.ID, def.Name, machineReach(def)); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(t.ctx, runTimeout)
	defer cancel()
	ctx, liveRun := t.app.runsRegistry().CreateCancellable(ctx, t.user, "", "")
	liveRun.Describe("machine", machineRunLabel(t, def), truncateObs(input, 100)).
		Parent(parentRunFromCtx(t.ctx))
	defer liveRun.Complete(RunStatusFailed) // safety net; the explicit calls below win
	ctx = withDispatchedMachine(ctx, def.ID)
	ctx = withParentRun(ctx, liveRun.ID)
	ctx = t.guardedRunContext(ctx)

	note := func(kind, detail string) {
		t.emitStatus("[" + def.Name + "] " + detail)
		t.turnDiag(kind, detail)
		Log("[orchestrate.machine %q] %s", def.Name, detail)
	}
	Log("[orchestrate.machines] %s running machine %q from the authoring tool (%d steps)", t.agent.ID, def.Name, len(def.Phases))
	out, _, err := t.app.runOwnedMachine(ctx, t.user, def.ID, input, note)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		msg := fmt.Sprintf("machine %q stopped: %v", def.Name, err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg += fmt.Sprintf(" (it ran into the %s ceiling on a run)", runTimeout)
		}
		// What it had produced travels with the failure, judged like a
		// result: nine steps of work are worth seeing, and seeing them is
		// how the author tells which step to fix.
		if partial, gerr := t.guardMachineOutput(t.ctx, def, strings.TrimSpace(out)); gerr == nil && partial != "" {
			msg += "\n\nWhat it had produced before it stopped:\n" + partial
		}
		return "", errors.New(msg)
	}
	liveRun.Complete(RunStatusCompleted)
	if out, err = t.guardMachineOutput(t.ctx, def, out); err != nil {
		return "", err
	}
	return machineDispatchResult(def, out)
}

// machineNotRunnableMessage is the answer to run on a machine that converses.
// It says both ways forward, because which one is right depends on what the
// machine is for, and the author knows that where the framework does not.
func machineNotRunnableMessage(def MachineDef) string {
	msg := "machine " + strconv.Quote(def.Name) + " converses rather than runs, so there is no single run to start"
	var waits []string
	for _, p := range def.Phases {
		if p.Resident {
			waits = append(waits, strconv.Quote(p.Name))
		}
	}
	if len(waits) > 0 {
		msg += ": " + strings.Join(waits, ", ") + " wait(s) for the person"
	}
	return msg + ". If nobody converses with it (it takes an input and returns a result), make it unattended: " +
		"action=\"update\" with unattended: true, no resident step, and next left empty on the step that produces the answer; then run it. " +
		"If it does converse, try it by attaching it to an agent (attach_to_agents) and talking to that agent."
}

func (t *chatTurn) machineDelete(args map[string]any) (string, error) {
	def, ok := t.findMachine(args)
	if !ok {
		return "", machineNotFound(args)
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
//
// An id that is not one is tried as a name: asked to rename a machine, an
// author put the old name in id and the new one in name, and was told
// nothing was stored under either.
func (t *chatTurn) findMachine(args map[string]any) (MachineDef, bool) {
	byName := func(name string) (MachineDef, bool) {
		for _, d := range ListMachineDefs(t.udb, t.user) {
			if strings.EqualFold(d.Name, name) {
				return d, true
			}
		}
		return MachineDef{}, false
	}
	if id := strings.TrimSpace(stringArg(args, "id")); id != "" {
		if d, ok := LoadMachineDef(t.udb, t.user, id); ok {
			return d, true
		}
		if d, ok := byName(id); ok {
			return d, true
		}
	}
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return MachineDef{}, false
	}
	return byName(name)
}

// parseAccumulators decodes a phase's contributions to the working set.
//
// Lenient in the same way the rest of this decoder is: a malformed entry is
// skipped rather than failing the whole authoring call, because Validate
// reports what is missing in words the author can act on, and refusing the
// save would lose the nine phases that were right.
func parseAccumulators(v any) []MachineAccumulator {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []MachineAccumulator
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		acc := MachineAccumulator{
			Name: strings.TrimSpace(mapStr(m, "name")),
			From: strings.TrimSpace(mapStr(m, "from")),
			Mode: strings.ToLower(strings.TrimSpace(mapStr(m, "mode"))),
			By:   strings.TrimSpace(mapStr(m, "by")),
		}
		if acc.Name == "" && acc.From == "" {
			continue
		}
		out = append(out, acc)
	}
	return out
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
		if unknown := unknownPhaseKeys(m); len(unknown) > 0 {
			return nil, fmt.Errorf("phase %d (%s): %s", i+1, chFirst(strings.TrimSpace(mapStr(m, "name")), "unnamed"), unknownPhaseKeysMessage(unknown))
		}
		if why := nonStringNames(m); why != "" {
			return nil, fmt.Errorf("phase %d (%s): %s", i+1, chFirst(strings.TrimSpace(mapStr(m, "name")), "unnamed"), why)
		}
		fields, err := parsePipelineFields(i+1, m["output"])
		if err != nil {
			return nil, err
		}
		out = append(out, MachinePhase{
			Name:      strings.TrimSpace(mapStr(m, "name")),
			Desc:      strings.TrimSpace(mapStr(m, "desc")),
			Prompt:    mapStr(m, "prompt"),
			Reach:     normalizeReach(mapStr(m, "reach")),
			Tools:     mapStrList(m, "tools"),
			Deny:      mapStrList(m, "deny"),
			Model:     strings.ToLower(strings.TrimSpace(mapStr(m, "model"))),
			Think:     normalizePhaseThink(m["think"]),
			Output:    fields,
			Resident:  mapBool(m, "resident"),
			Required:  mapBool(m, "required"),
			Next:      strings.TrimSpace(mapStr(m, "next")),
			ReplyWith: strings.TrimSpace(mapStr(m, "reply_with")),
			NextFrom:  strings.TrimSpace(mapStr(m, "next_from")),
			Choices:   mapStrList(m, "choices"),
			// Agent was missing here, so a machine written through this
			// tool could never delegate a step and an existing one lost
			// its delegate on the next update.
			Agent: strings.TrimSpace(mapStr(m, "agent")),
			// Same failure the comment above records, so the same fix: a
			// field the tool documents and never reads is a field an
			// author sets once and loses on the next update.
			Tool:        strings.TrimSpace(mapStr(m, "tool")),
			Args:        mapStrMap(m, "args"),
			Pipeline:    strings.TrimSpace(mapStr(m, "pipeline")),
			Machine:     strings.TrimSpace(mapStr(m, "machine")),
			Accumulates: parseAccumulators(m["accumulates"]),
			Guard:       strings.TrimSpace(mapStr(m, "guard")),
			GuardTo:     strings.TrimSpace(mapStr(m, "guard_to")),
			Keep:        mapStrList(m, "keep"),
			ExitsTo:     mapStrList(m, "exits_to"),
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

// updatePhaseFieldNames is what update_phase writes, in the order it says so.
var updatePhaseFieldNames = []string{
	"prompt", "desc", "think", "model", "next", "choices", "resident", "required", "reply_with",
	"guard", "guard_to", "tools", "deny", "reach",
}

// machineLevelFieldNames are the create/update fields that describe the whole
// machine, which update_phase is sometimes handed alongside a step's own.
var machineLevelFieldNames = map[string]bool{
	"machine_deny": true, "unattended": true, "route_each_message": true,
	"start": true, "description": true, "attach_to_agents": true,
}

// machineLevelKeys picks the machine-level fields out of a list of keys.
func machineLevelKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if machineLevelFieldNames[k] {
			out = append(out, k)
		}
	}
	return out
}

// unknownUpdatePhaseKeys names the keys an update_phase call carries that it
// neither writes nor uses to find the step.
func unknownUpdatePhaseKeys(args map[string]any) []string {
	known := map[string]bool{"action": true, "name": true, "id": true, "phase": true}
	for _, k := range updatePhaseFieldNames {
		known[k] = true
	}
	var out []string
	for k := range args {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// machinePhaseKeys is every field a phase object may carry: MachinePhase's own
// JSON names, which is what parseMachinePhases reads.
var machinePhaseKeys = map[string]bool{
	"name": true, "desc": true, "prompt": true, "tool": true, "args": true,
	"reach": true, "tools": true, "deny": true, "model": true, "think": true,
	"output": true, "resident": true, "required": true, "reply_with": true, "next": true, "next_from": true,
	"choices": true, "guard": true, "guard_to": true, "exits_to": true,
	"keep": true, "agent": true, "pipeline": true, "machine": true,
	"accumulates": true,
}

// unknownPhaseKeys names the keys of one phase object that no field reads. They
// used to vanish on save, so a step written with a branch condition saved
// without one and nothing said so.
func unknownPhaseKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		if !machinePhaseKeys[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// unknownPhaseKeysMessage says what to use instead, naming the branching
// fields when the unknown key looks like an attempt at a condition.
func unknownPhaseKeysMessage(unknown []string) string {
	msg := "unknown field(s) " + strings.Join(unknown, ", ") + ", so nothing was saved."
	for _, k := range unknown {
		switch strings.ToLower(k) {
		case "when", "if", "else", "else_next", "then", "condition", "branch", "route", "routes", "on_true", "on_false":
			return msg + " A step branches with choices (the steps it may pick between; it decides at run time) or next_from (one of its own string output fields holding a step name). There is no condition field."
		case "kind", "type":
			// A pipeline stage HAS a kind, so an author who has written one
			// carries the habit over. Naming the fields that do the job is
			// the whole answer; the generic list would leave them guessing
			// which one replaces it.
			return msg + " A machine step has no kind: what it runs is set by the field you fill. " +
				"tool (with args) makes a tool step; agent, pipeline or machine delegates; none of them makes a model step."
		}
	}
	names := make([]string, 0, len(machinePhaseKeys))
	for k := range machinePhaseKeys {
		names = append(names, k)
	}
	sort.Strings(names)
	return msg + " A step's fields are: " + strings.Join(names, ", ") + "."
}

// machineNotFound is the answer when findMachine finds nothing: that no
// machine was named, when neither name nor id was given (the usual case,
// and "not found" sent the builder looking for a machine that was there),
// else that the named one does not exist.
func machineNotFound(args map[string]any) error {
	if strings.TrimSpace(stringArg(args, "name")) == "" && strings.TrimSpace(stringArg(args, "id")) == "" {
		return errors.New("name (or id) is required: which machine? machine(action=\"list\") shows what you have")
	}
	return errors.New("no machine found by that name or id: machine(action=\"list\") shows what you have")
}
