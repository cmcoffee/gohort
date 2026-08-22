// What a machine run needs from its surroundings, and nothing else.
//
// A machine's steps can each be run by something different: an inline worker,
// one tool with no model at all, another AGENT, a stored PIPELINE, or a whole
// child MACHINE. Until now that dispatch lived on chatTurn, so it existed only
// inside a conversation. Every turn-free door — the schedule, the machine
// page's Run button, a dispatch from another agent, a pipeline's machine stage —
// ran those steps through the bare inline worker instead, which knows nothing
// about the fields. A step arranged to delegate ran as an ordinary prompt and
// came back with a plausible answer that had done none of the work its author
// arranged, silently. Dispatch alone noticed, and dealt with it by refusing
// such machines by name.
//
// machineHost is that dispatch, separated from the turn. It holds the handful of
// things the runners actually reach for — a catalog, an approval gate, somewhere
// to narrate, somewhere to leave breadcrumbs, the blackboard — as fields rather
// than as a *chatTurn, so a run with nobody watching supplies its own and gets
// the same behavior. The conversational host fills them from the turn; the
// unattended host fills them from the run.
//
// What is deliberately NOT here: the decision to run at all. Guards, checklists,
// and the conversational/unattended split stay with core's walk. This is only
// "who runs this step".

package orchestrate

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// machineHost is the surroundings of one machine run. Every field is
// nil-tolerant except app / user / catalog: a host with nowhere to narrate
// simply does not narrate, which is the normal state of a 3am run.
type machineHost struct {
	app *OrchestrateApp
	// user is whose store references resolve in AND whose authority the run
	// carries. For a dispatched machine that is the REQUESTER, not the owner:
	// the recipe travels, the authority does not.
	user string
	udb  Database
	// agentID is the agent the machine belongs to, when one does. Empty for a
	// run started from the machine page or a schedule that names no agent —
	// which only costs the self-delegation check, since there is no self.
	agentID   string
	agentName string
	// thread is what a delegate's sub-session hangs off: the conversation for a
	// turn, the run's own id otherwise.
	thread string

	// catalog resolves the tool pool for one step. A function rather than a
	// slice because a turn builds it lazily (the step runs before the turn's
	// own session exists) and narrows per step.
	catalog func(ph MachinePhase) []AgentToolDef
	// confirm is the approval gate. nil means there is nobody to ask, which is
	// the unattended case — see PhaseWorkerConfirm, which reads nil the same way.
	confirm func(name, args string) bool
	// status narrates the step about to run; activity carries progress OUT of a
	// sub-run (a delegate, a pipeline's stages). Both nil-safe.
	status   func(text string)
	activity func(source, text string)
	// note leaves a breadcrumb about what the framework decided. This is the
	// same func core's walk takes, so a host passes one thing to both.
	note func(kind, detail string)
	// priorWork records that a step did real work, for the end-of-turn judge.
	// Only a turn has one.
	priorWork func(what string)
	// state is the blackboard, read when a tool step templates its arguments.
	state func() MachineState
	// turn builds the MachineTurn a child run is given.
	turn func(input string) MachineTurn
}

func (h *machineHost) emitStatus(text string) {
	if h.status != nil && strings.TrimSpace(text) != "" {
		h.status(text)
	}
}

func (h *machineHost) emitActivity(source, text string) {
	if h.activity != nil && strings.TrimSpace(text) != "" {
		h.activity(source, text)
	}
}

func (h *machineHost) diag(kind, detail string) {
	if h.note != nil {
		h.note(kind, detail)
	}
}

func (h *machineHost) notePriorWork(what string) {
	if h.priorWork != nil {
		h.priorWork(what)
	}
}

func (h *machineHost) blackboard() MachineState {
	if h.state == nil {
		return nil
	}
	return h.state()
}

func (h *machineHost) machineTurn(input string) MachineTurn {
	if h.turn != nil {
		return h.turn(input)
	}
	return MachineTurn{
		Input: input,
		User:  h.user,
		Agent: h.agentName,
		Now:   time.Now().In(UserLocation(h.user)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}
}

// phaseRunner returns the runner the machine drives: the ordinary inline
// worker, wrapped so a step naming a tool, an agent, a pipeline, or a child
// machine is handed to that instead.
func (h *machineHost) phaseRunner() PhaseRunner {
	return func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		// Per step, because the catalog is: a step that names no tools pays
		// nothing, and one that names some gets exactly those (PhaseWorker
		// narrows by ph.Tools). A delegate brings its own, so it is handed
		// none of this.
		base := h.app.PhaseWorkerConfirm(h.catalog(ph), h.confirm)
		// Say what is happening BEFORE it happens. In a conversation these
		// calls run at the head of the turn, before the persona is even
		// assembled, so until the first one returns the person is looking at
		// nothing. Silence during work somebody is waiting on reads as hung,
		// whatever the reason for it.
		h.emitStatus(phaseStatusLine(ph))
		// Whatever runs the step, the work belongs to whoever is accounting for
		// it — and none of these paths is visible to the loop's own accounting,
		// which begins later and on a different session. Recorded on success
		// only, and before the result is read, so the end-of-turn judge can tell
		// a reply built on a step's findings from one that invented them.
		note := func(kind, what string) func(string, error) (string, error) {
			return func(out string, err error) (string, error) {
				if err == nil {
					h.notePriorWork("step " + ph.Name + " ran as a " + kind + " (" + what + ")")
				}
				return out, err
			}
		}
		// A tool step first: no model, no tokens, no catalog to assemble.
		// Checked before the others because Validate refuses a step naming two
		// runners, so the order only decides which wins on an imported step
		// that carries both anyway.
		if tool := strings.TrimSpace(ph.Tool); tool != "" {
			return note("tool", tool)(h.runToolPhase(ctx, ph, tool, prompt))
		}
		if pipe := strings.TrimSpace(ph.Pipeline); pipe != "" {
			return note("pipeline", pipe)(h.runPipelinePhase(ctx, ph, pipe, prompt, base))
		}
		if child := strings.TrimSpace(ph.Machine); child != "" {
			return note("machine", child)(h.runChildMachinePhase(ctx, ph, child, prompt, base))
		}
		ref := strings.TrimSpace(ph.Agent)
		if ref == "" {
			return base(ctx, ph, prompt)
		}
		return note("delegate", ref)(h.runDelegatedPhase(ctx, ph, ref, prompt, base))
	}
}

// runToolPhase calls one tool and hands its result on, with no model in the
// loop at all.
//
// The arguments are the AUTHOR'S keys with templated values: a placeholder can
// fill an argument and can never become one, so nothing an earlier step
// produced can add or rename a parameter. The same rule a minted command tool
// follows, for the same reason.
//
// It goes through the host's approval gate. The step's tool NAME is the
// author's, but its ARGUMENTS are templated from what earlier steps produced,
// which is model-written — "the owner wrote the machine" establishes which tool
// runs, not what it is about to be told to do.
func (h *machineHost) runToolPhase(ctx context.Context, ph MachinePhase, tool, prompt string) (string, error) {
	// The step's own catalog: a tool step reaches exactly what the run holds. A
	// name that is not there is the machine naming a tool this agent does not
	// carry, which is what the attach preflight warns about before the first turn.
	pool := h.catalog(MachinePhase{Name: ph.Name, Tools: []string{tool}})
	var handler ToolHandlerFunc
	var needsConfirm bool
	for _, td := range pool {
		if td.Tool.Name == tool {
			handler, needsConfirm = td.Handler, td.NeedsConfirm
			break
		}
	}
	if handler == nil {
		h.diag("machine_step_tool_missing", "step "+ph.Name+" calls "+tool+
			", which this agent does not carry — attach what provides it, or point the step at a tool it has.")
		return "", Error("step " + ph.Name + ": tool " + tool + " is not available to this agent")
	}
	// Templated with the machine's own vocabulary, so a step can pass what an
	// earlier one worked out — {input}, {prev}, {state:PHASE.field}. {prev}
	// carries what the step before this one handed on, which is the argument a
	// tool step most often wants.
	vars := PhaseVars{MachineTurn: h.machineTurn("")}
	vars.Prev = prompt
	args := make(map[string]any, len(ph.Args))
	for k, tmpl := range ph.Args {
		args[k] = ResolvePhaseTemplate(tmpl, vars, h.blackboard())
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if h.confirm != nil && needsConfirm {
		if !h.confirm(tool, formatToolCall(tool, args)) {
			return "", Error("step " + ph.Name + ": " + tool + " was not approved")
		}
	}
	out, err := handler(args)
	if err != nil {
		return "", err
	}
	return out, nil
}

// runDelegatedPhase dispatches one step to another agent.
func (h *machineHost) runDelegatedPhase(ctx context.Context, ph MachinePhase, ref, prompt string, base PhaseRunner) (string, error) {
	target, found := findAgentByNameOrID(h.udb, h.user, ref)
	if !found {
		// Broken-dependency posture: a machine is portable and the agent it
		// names may simply not exist in this deployment. Run the step inline
		// rather than failing the run, and say so — a step that quietly stops
		// delegating is a machine that quietly stopped being what its author
		// built.
		h.diag("phase_delegate_missing", "phase "+ph.Name+" delegates to agent "+ref+
			", which does not exist here; this run did the phase inline instead")
		return base(ctx, ph, prompt)
	}
	if h.agentID != "" && target.ID == h.agentID {
		// Delegating to yourself is a second turn of the same agent with none
		// of the benefit and all of the cost.
		h.diag("phase_delegate_self", "phase "+ph.Name+" delegates to the agent already running it; ran inline")
		return base(ctx, ph, prompt)
	}

	// One continuing thread per (run, step). Continuing, so a re-entered step
	// builds on what the delegate already established in THIS run;
	// per-run, so two investigations never share a delegate's context — the
	// contamination the whole investigation workflow is arranged to prevent.
	sub := "machine:" + h.thread + ":" + ph.Name
	h.diag("phase_delegate", "phase "+ph.Name+" delegated to "+chFirst(target.Name, target.ID))

	label := chFirst(target.Name, target.ID)
	res, err := h.app.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
		AgentOwner:   h.user,
		RuntimeUser:  h.user,
		AgentKey:     target.ID,
		SubSessionID: sub,
		Message:      prompt,
		// A caller mid-turn has a person waiting, so the delegate's progress
		// rides the same activity surface the rest of the turn uses rather than
		// disappearing into a silence. A run with nobody watching drops it.
		StatusCallback: func(s string) { h.emitActivity(label, s) },
	})
	if err != nil {
		// The delegate failed. Falling back to inline would answer the question
		// with the wrong thing wearing the right name, so this fails the step
		// and lets the machine's own error path report it.
		return "", err
	}
	report := strings.TrimSpace(res.Text)
	if report == "" {
		return "", Error("phase " + ph.Name + " delegated to " + label + " and got nothing back")
	}
	if len(ph.Output) == 0 {
		// Nothing declared: the report IS the step's product, and there is
		// nothing to decode.
		return report, nil
	}
	// Shape the report into the declared fields, through the same worker the
	// step would have used. The delegate is asked for its work, not for a schema.
	return base(ctx, ph, "A delegate was given this task:\n\n"+prompt+
		"\n\nIt reported back:\n\n"+report+
		"\n\nRecord what it found in the fields below. Take the delegate's findings as given — "+
		"do not re-do its work, second-guess it, or fill a field it did not address. "+
		"A field it left unanswered is better left empty than invented.")
}

// runPipelinePhase runs one step THROUGH a stored pipeline.
//
// The delegate above hands a step to another agent; this hands it to a recipe.
// Same seam, same broken-dependency posture, and one difference that is the
// whole reason to prefer it where it fits: a pipeline can hand back a SHAPE. An
// agent answers in prose, so a step that declares fields has to pay a second
// call to read them out of the answer. A pipeline whose final stage declared
// those same fields has already produced them, and they are taken as the step's
// own.
func (h *machineHost) runPipelinePhase(ctx context.Context, ph MachinePhase, ref, prompt string, base PhaseRunner) (string, error) {
	def, found := findPipelineByNameOrID(h.udb, h.user, ref)
	if !found {
		// A machine is portable and the pipeline it names may not exist here.
		// Run inline rather than failing the run, and say so.
		h.diag("phase_pipeline_missing", "phase "+ph.Name+" runs through pipeline "+ref+
			", which does not exist here; this run did the phase inline instead")
		return base(ctx, ph, prompt)
	}
	h.diag("phase_pipeline", "phase "+ph.Name+" ran through pipeline "+chFirst(def.Name, def.ID))

	// An agent stage inside the pipeline dispatches as the caller, the same way
	// a pipeline run from its own page does.
	dispatch := func(ctx context.Context, agentID, stageInput string) (string, error) {
		return h.app.RunAgentSync(ctx, h.user, h.user, agentID, stageInput)
	}
	label := chFirst(def.Name, def.ID)
	status := func(s string) { h.emitActivity(label, s) }
	// The step's own catalog is the pipeline's inherited pool, so a stage that
	// names no tools reaches exactly what the step may reach, and one that names
	// some gets that subset. Nothing widens here.
	out, fields, err := h.app.RunPipelineDefHooks(ctx, def, prompt, PipelineHooks{
		Dispatch: dispatch,
		// A stage of that pipeline may itself run a machine. The depth on the
		// context is what stops that going round forever.
		Machine: h.app.pipelineMachineRunner(h.user),
		Status:  status,
		Tools:   h.catalog(ph),
	})
	if err != nil {
		// Falling back to inline would answer with the wrong thing wearing the
		// right name. Fail the step and let the machine report it, exactly as a
		// failed delegate does.
		return "", err
	}
	report := strings.TrimSpace(out)
	if report == "" {
		return "", Error("phase " + ph.Name + " ran pipeline " + label + " and got nothing back")
	}
	if len(ph.Output) == 0 {
		return report, nil
	}
	// The one-call path: the pipeline already produced every field this step
	// declares, so hand those straight to the decoder rather than paying a model
	// to copy them across.
	if js, ok := declaredFieldsJSON(ph.Output, fields); ok {
		h.diag("phase_pipeline_shape", "phase "+ph.Name+" took its fields from the pipeline's own declared output; no second call")
		return js, nil
	}
	// Shapes did not line up. Same recovery as a delegate: the step's own worker
	// reads the report into the declared fields.
	return base(ctx, ph, "A pipeline was run for this task:\n\n"+prompt+
		"\n\nIt produced:\n\n"+report+
		"\n\nRecord what it produced in the fields below. Take its output as given — "+
		"do not re-do the work, second-guess it, or fill a field it did not address. "+
		"A field it left unanswered is better left empty than invented.")
}

// runChildMachinePhase runs one step as a whole machine of its own.
//
// The third runner, and the one that recurses. A delegate is another agent, a
// pipeline is a recipe, and a child machine is a RUN: its own blackboard, its
// own phases, its own working set, carried to completion while this step waits.
//
// The case it exists for is a gap-filling pass: a run notices a hole, starts a
// smaller run to fill it, and folds what comes back into its own lists. Which is
// why there is no merge mechanism here — the child's result IS this step's
// result, so the step's own Accumulates carries it into the parent's working set
// like any other contribution.
func (h *machineHost) runChildMachinePhase(ctx context.Context, ph MachinePhase, ref, prompt string, base PhaseRunner) (string, error) {
	// Depth first, because the check is cheap and the alternative is a tree
	// nobody authored. The child runs through this same runner, so without a
	// counter on the context its phases would be indistinguishable from the
	// parent's.
	if d := MachineDepth(ctx); d >= MaxMachineDepth {
		h.diag("phase_child_machine_too_deep", "step "+ph.Name+" runs machine "+ref+
			", but this is already "+strconv.Itoa(d)+" machine(s) deep and the limit is "+strconv.Itoa(MaxMachineDepth)+
			". The step ran inline instead; if the work genuinely nests further, flatten it into one machine.")
		return base(ctx, ph, prompt)
	}
	child, found := findMachineByNameOrID(h.udb, h.user, ref)
	if !found {
		// Broken-dependency posture, the same as a missing delegate or pipeline.
		h.diag("phase_child_machine_missing", "step "+ph.Name+" runs machine "+ref+
			", which does not exist here; this run did the step inline instead")
		return base(ctx, ph, prompt)
	}
	if !child.Unattended {
		// A conversational machine has a step that waits for a person, and there
		// is nobody inside a step to wait for. Refusing here beats entering a
		// step the run can never leave.
		h.diag("phase_child_machine_conversational", "step "+ph.Name+" runs machine "+chFirst(child.Name, child.ID)+
			", which is a conversation rather than a run: it has a step that waits for a person, and nobody is waiting inside a step. "+
			"Turn on \"this RUNS instead of converses\" on that machine, or point this step somewhere else. The step ran inline instead.")
		return base(ctx, ph, prompt)
	}
	h.diag("phase_child_machine", "step "+ph.Name+" ran machine "+chFirst(child.Name, child.ID)+" as a child run")

	// The child's own cursor. Fresh every time: a child run is a piece of THIS
	// run's work, not a conversation with a position to resume, and carrying a
	// cursor between them would leak one gap's findings into the next one's
	// blackboard.
	cur := &MachineCursor{}
	// The child reads ITS blackboard, not the parent's — the same separation the
	// fresh cursor exists for. A host is cheap; sharing one would quietly give
	// the child's tool steps the parent's state.
	childHost := *h
	childHost.state = func() MachineState { return cur.State }
	// The same runner, one level deeper. Reusing it is what lets a child's
	// phases delegate and run pipelines exactly as a parent's do; the depth on
	// the context is the only thing that differs, and it is what stops the
	// third level.
	childCtx := WithMachineDepth(ctx, MachineDepth(ctx)+1)
	final, text, err := h.app.RunUnattended(childCtx, child, cur, h.machineTurn(prompt), childHost.phaseRunner(), h.note)
	if err != nil {
		// Falling back to inline would answer the question with something else
		// wearing the right name. Fail the step, as a failed delegate or
		// pipeline does.
		return "", err
	}
	report := strings.TrimSpace(text)
	if report == "" {
		return "", Error("step " + ph.Name + " ran machine " + chFirst(child.Name, child.ID) + " and got nothing back")
	}
	if len(ph.Output) == 0 {
		return report, nil
	}
	// One call when the shapes line up: the child's last step declared what this
	// step declares, so those values are taken as they are.
	if js, ok := declaredFieldsJSON(ph.Output, cur.State[final.Name].Fields); ok {
		h.diag("phase_child_machine_shape", "step "+ph.Name+" took its fields from the child run's own last step; no second call")
		return js, nil
	}
	return base(ctx, ph, "A separate run was carried out for this task:\n\n"+prompt+
		"\n\nIt finished with:\n\n"+report+
		"\n\nRecord what it found in the fields below. Take its findings as given — "+
		"do not re-do the work, second-guess it, or fill a field it did not address. "+
		"A field it left unanswered is better left empty than invented.")
}

// declaredFieldsJSON re-encodes a sub-run's values for the fields this step
// declares, and reports whether it could.
//
// Every declared name must be present. A partial match is not a shortcut worth
// taking: the missing field would decode as empty and read as "the pipeline had
// nothing to say about it", when the truth is that nobody asked. That case goes
// to the shaping call, which can.
//
// Re-encoding the SUBSET rather than passing the sub-run's own JSON through is
// deliberate. runDeclaredOutput retries once on a decode mismatch by calling the
// runner again, and for this runner that means running the whole pipeline a
// second time. Handing the decoder exactly the names it asked for keeps that
// retry from ever being reached.
func declaredFieldsJSON(declared []PipelineField, fields map[string]any) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	out := make(map[string]any, len(declared))
	for _, f := range declared {
		v, ok := fields[f.Name]
		if !ok {
			return "", false
		}
		out[f.Name] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// phaseStatusLine narrates one step in the author's own words.
//
// The step's description is written for a person (it is what the rail and the
// routing instruction show), so it is the right sentence here too — no second
// copy to keep in sync. A guard arrives as a synthetic phase named
// "guard:<step>", and saying so matters: it is a call the person pays for on
// EVERY turn spent in that step, and "checking whether this is still the same
// job" is the only honest description of where that second went.
func phaseStatusLine(ph MachinePhase) string {
	if guarded := strings.TrimPrefix(ph.Name, "guard:"); guarded != ph.Name {
		return "Checking whether this is still the same job…"
	}
	if d := strings.TrimSpace(ph.Desc); d != "" {
		return ph.Name + ": " + strings.TrimRight(d, ".") + "…"
	}
	return "Working through " + ph.Name + "…"
}

// findPipelineByNameOrID resolves a step's pipeline reference.
//
// By NAME as well as id, and name first among the user's own, because a machine
// is portable: an exported recipe carries the name somebody wrote, while the id
// belongs to the deployment it was authored in.
func findPipelineByNameOrID(udb Database, user, ref string) (PipelineDef, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return PipelineDef{}, false
	}
	for _, d := range ListPipelineDefs(udb, user) {
		if strings.EqualFold(strings.TrimSpace(d.Name), ref) {
			return d, true
		}
	}
	if def, _, _, found := resolvePipelineFor(user, udb, ref); found {
		return def, true
	}
	return PipelineDef{}, false
}

// findMachineByNameOrID resolves a step's child-machine reference, name first
// for the same reason a pipeline reference resolves that way.
func findMachineByNameOrID(udb Database, user, ref string) (MachineDef, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return MachineDef{}, false
	}
	for _, d := range ListMachineDefs(udb, user) {
		if strings.EqualFold(strings.TrimSpace(d.Name), ref) {
			return d, true
		}
	}
	return LoadMachineDef(udb, user, ref)
}
