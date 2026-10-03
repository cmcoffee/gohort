package core

import (
	"context"
	"strings"
	"sync"
)

// PhaseWorker returns the default PhaseRunner: a transient phase runs as
// a focused worker call over its slice of catalog, on its own tier, with
// its own think setting.
//
// It exists so a host gets transient phases without writing any LLM
// plumbing of its own — the same reason pipelines ship an interpreter
// rather than an interface. A host with a reason to run phases
// differently (its own persona, its own guardrails, a streaming surface)
// passes its own PhaseRunner instead; nothing here is privileged.
//
// Note what it deliberately does NOT carry: no persona, no session
// history. A transient phase does one bounded job against the prompt the
// driver resolved for it, and its product is state, not conversation.
func (T *AppCore) PhaseWorker(catalog []AgentToolDef) PhaseRunner {
	return T.PhaseWorkerConfirm(catalog, nil)
}

// PhaseWorkerConfirm is PhaseWorker with the host's approval hook, for a
// host running steps where somebody is watching.
//
// The hook is the turn's own: a step must not reach a tool the turn
// itself would have stopped to ask about. nil means allow (the dry run
// and any unattended host), which is safe only because those have no
// person to ask and no live catalog to reach.
func (T *AppCore) PhaseWorkerConfirm(catalog []AgentToolDef, confirm func(name, args string) bool) PhaseRunner {
	return func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		// A transient phase defaults to NOT reasoning: it is a bounded
		// transform (split this up, pick a lane) sitting in front of the
		// user's actual turn, and the latency it adds is paid before
		// anyone sees a word. Authors opt in per phase.
		think := PhaseThink(ph, false)
		tools := PhaseTools(ph, catalog)
		if offersSkip(ph) {
			tools = append(tools, skipStepTool())
		}
		return T.runWorkerStageConfirm(ctx, prompt, tools, think, len(ph.ModelOutput()) > 0, PhaseTier(ph), confirm)
	}
}

// skipStepToolName is the way out a model-run step is handed: "this step
// does not apply to what I was given".
//
// It exists because a step that reaches tools had two moves and both were
// wrong when the step did not fit the run: call the tool it was pointed at
// anyway, with whatever arguments it could invent, or improvise with some
// other tool. Both produce a result that reads like the step's real work.
// A skip is visible instead: the reason lands in the run's notes, the step's
// result is marked, and the machine moves on to the step's Next.
const skipStepToolName = "skip_step"

// offersSkip reports whether a step gets the way out: it runs its own model
// with tools, and its author did not mark it required. A step that reaches no
// tools has nothing to call wrongly, a step another runner does (an agent, a
// pipeline, a child machine, one tool) is not running this model, and a
// resident step converses rather than runs.
func offersSkip(ph MachinePhase) bool {
	return !ph.Required && !ph.Resident && !ph.hasRunner() && PhaseReach(ph) != ReachNone
}

func skipStepTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: skipStepToolName,
			Description: "End this step as NOT APPLICABLE, with the reason. Use it only when the step genuinely does not fit what you were given: " +
				"the thing it acts on is absent, or no tool you have does what it asks. Do not use it to avoid work the step can do. " +
				"Nothing the step would have produced is recorded, the reason is, and the run moves on to the next step.",
			Parameters: map[string]ToolParam{
				"reason": {Type: "string", Description: "Why this step does not apply, in one sentence."},
			},
			Required: []string{"reason"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			reason, _ := args["reason"].(string)
			if !SkipStep(ctx, reason) {
				return "", Error("skip_step only works inside a machine step")
			}
			return "Step skipped. Stop here and write nothing more.", nil
		},
	}
}

type phaseVarsKey struct{}

// StepVars returns the variables of the machine step running on ctx: its
// input, what the step before it handed on, the opening message, who and
// when. A step runner that templates anything beyond the prompt it was handed
// templates with these, so {input} and {prev} mean the same there as in the
// prompt. False outside a machine step.
func StepVars(ctx context.Context) (PhaseVars, bool) {
	if ctx == nil {
		return PhaseVars{}, false
	}
	v, ok := ctx.Value(phaseVarsKey{}).(PhaseVars)
	return v, ok
}

type skipSlotKey struct{}

type skipSlot struct {
	mu     sync.Mutex
	reason string
	set    bool
}

// withSkipSlot gives one step's run somewhere to report that it does not
// apply, and returns how to read it afterwards.
func withSkipSlot(ctx context.Context) (context.Context, func() (string, bool)) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &skipSlot{}
	return context.WithValue(ctx, skipSlotKey{}, s), func() (string, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.reason, s.set
	}
}

// SkipStep ends the machine step running on ctx as not applicable, for the
// reason given, and reports whether there was a step to end. The step's
// runner calls it and returns; the machine records the skip, passes the
// step's input through as its result, and moves on to the step's Next. A
// runner that is not running a machine step gets false and should fail
// rather than pretend.
func SkipStep(ctx context.Context, reason string) bool {
	if ctx == nil {
		return false
	}
	s, _ := ctx.Value(skipSlotKey{}).(*skipSlot)
	if s == nil {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "no reason given"
	}
	s.mu.Lock()
	if !s.set {
		s.reason, s.set = reason, true
	}
	s.mu.Unlock()
	return true
}

// skipRequested reports whether the step on ctx has already been skipped.
func skipRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	s, _ := ctx.Value(skipSlotKey{}).(*skipSlot)
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

// CompleteTurn closes a turn the host has finished running, advancing
// the cursor when the resident phase that replied names a Next.
//
// This is what makes a one-beat resident phase possible: an intake that
// greets, asks its questions, and then hands off. AdvanceMachine cannot
// do it, because the handoff is only correct AFTER the phase has had its
// turn, and the driver returns before that. Empty Next (the ordinary
// case) stays put, which is the shape most machines want: a resident
// phase the conversation lives in.
//
// It deliberately writes NOTHING to the state blackboard. A resident
// phase's product is the conversation, and the conversation is already
// in history; pinning its reply into MachineState would paste it into
// the system prompt of every later phase, forever, growing the one part
// of the prompt this design keeps small and stable.
func (d MachineDef) CompleteTurn(cur *MachineCursor, ph MachinePhase, note func(kind, detail string)) {
	if cur == nil || !ph.Resident {
		return
	}
	next := strings.TrimSpace(ph.Next)
	if next == "" {
		return
	}
	if note == nil {
		note = func(string, string) {}
	}
	nph, ok := d.Phase(next)
	if !ok {
		note("machine_dead_end", "step "+ph.Name+" hands off to unknown step "+next+"; staying put")
		return
	}
	cur.moveTo(ph.Name, nph, "handed off after one turn", note, d.accumulatorNames())
	note("machine_phase_advance", "step "+ph.Name+" has had its turn; moving to "+nph.Name)
}

// PhaseInstructions returns EXACTLY what one step is sent, for a caller
// that needs to show an author the real thing rather than a description
// of it.
//
// The two kinds are composed differently and that difference is easy to
// get wrong — the editor's first preview called PhaseBlock for both,
// showing transient steps an "Established earlier" block they never
// receive and hiding the output contract they do:
//
//   - A TRANSIENT step gets its own prompt with {input} / {prev} /
//     {state:…} resolved, plus the declared-output contract appended by
//     runDeclaredOutput.
//   - A RESIDENT step gets PhaseBlock, layered into the agent's system
//     prompt: its directive, what earlier steps established, where else
//     it can go, and the routing block.
//
// Built from the same functions the run path uses, so it cannot drift
// into describing something the model never sees.
func (d MachineDef) PhaseInstructions(ph MachinePhase, st MachineState, v PhaseVars) string {
	if ph.Resident {
		return d.PhaseBlock(ph, st, v)
	}
	out := d.phasePrompt(ph, st, v)
	if fields := ph.ModelOutput(); len(fields) > 0 {
		out += renderOutputContract(fields)
	}
	return out
}

// phasePrompt composes a transient step's directive: its own prompt with
// the vocabulary resolved, preceded by the person's message when the
// prompt never placed it itself.
//
// Split from PhaseInstructions because the run path adds the output
// contract itself (runDeclaredOutput renders it, and again on the repair
// retry), so this is the part both share and neither duplicates.
func (d MachineDef) phasePrompt(ph MachinePhase, st MachineState, v PhaseVars) string {
	// The two that depend on WHICH step is asking, filled in here so no
	// caller has to remember to.
	v.Step = ph.Name
	v.Machine = chooseStr(v.Machine, d.Name)
	v.Established = d.establishedBlock(ph, st)

	out := ResolvePhaseTemplate(ph.Prompt, v, st)
	// A step that runs against no message at all is the most expensive
	// thing an author can forget, and it fails silently: the model
	// answers confidently about nothing.
	if !mentionsInput(ph.Prompt) {
		out = v.inputBlock() + out
	}
	// And what earlier steps worked out, unless the prompt reaches for it
	// itself. A resident step has always been handed this; a transient
	// one was left to hand-copy {state:…} references for values the
	// definition already knows.
	if !mentionsEstablished(ph.Prompt) {
		out = v.establishedBlock() + out
	}
	// A transient step that decides where to go gets the same account of
	// its choices a resident one does. It used to see only the bare names
	// in its output contract.
	if r := d.routingBlock(ph, v); r != "" {
		out += r
	}
	return out
}

// runPhase resolves one transient phase's prompt and calls it, decoding
// a declared Output through the same contract → decode → one repair path
// pipeline stages use.
func (T *AppCore) runPhase(ctx context.Context, def MachineDef, ph MachinePhase, v PhaseVars, st MachineState, run PhaseRunner, note func(kind, detail string)) (string, map[string]any, error) {
	// One composition, shared with the editor's preview, so what an
	// author is shown is what the model is sent.
	prompt := def.phasePrompt(ph, st, v)
	// The step's own variables ride the context, for a runner that templates
	// more than the prompt: a tool step's arguments. It used to rebuild them
	// with no input and with the composed prompt as {prev}, so {input} in a
	// tool step's arguments was empty on every run.
	sv := v
	sv.Step, sv.Machine = ph.Name, chooseStr(v.Machine, def.Name)
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, phaseVarsKey{}, sv)
	out := ph.ModelOutput()
	// A step that skipped is finished: no decode of what it said, no repair
	// call to make it say something decodable. The walk reads the skip.
	call := func(p string) (string, error) {
		if skipRequested(ctx) {
			return "", Error("step skipped")
		}
		text, err := run(ctx, ph, p)
		if skipRequested(ctx) {
			return "", Error("step skipped")
		}
		return text, err
	}

	// A step that asks the model for nothing and says nothing is a step
	// that pins values. Calling anyway would buy a paragraph nobody
	// reads and a bill nobody expected.
	if len(out) == 0 && strings.TrimSpace(ph.Prompt) == "" && len(ph.StaticFields()) > 0 {
		return "", def.fillStatic(ph, nil, v, st, note), nil
	}
	if len(out) == 0 {
		text, err := call(prompt)
		if err != nil {
			return "", nil, Error("machine " + def.Name + ", phase " + ph.Name + ": " + err.Error())
		}
		return text, def.fillStatic(ph, nil, v, st, note), nil
	}
	status := func(s string) { note("machine_output_repair", s) }
	text, fields, err := T.runDeclaredOutput(ctx, "phase "+ph.Name, out, prompt, call, restateCall(ctx, ph, run), status)
	if err != nil {
		return "", nil, Error("machine " + def.Name + ", phase " + ph.Name + ": " + err.Error())
	}
	return text, def.fillStatic(ph, fields, v, st, note), nil
}

// restateCall is the repair for a step whose run was expensive: one that
// reached tools, or handed its work to an agent, a pipeline, or a child
// run. Its reply is restated by a call standing in for the step with none
// of that, so a formatting miss costs one small request rather than the
// whole step again (see runDeclaredOutput). nil for a step that reaches
// nothing, where running it again IS one small request, and a fresh try
// can fix a reply that was wrong in substance as well as in shape.
func restateCall(ctx context.Context, ph MachinePhase, run PhaseRunner) func(string) (string, error) {
	if PhaseReach(ph) == ReachNone && !ph.hasRunner() {
		return nil
	}
	plain := MachinePhase{Name: ph.Name, Model: ph.Model, Think: "off", Reach: ReachNone, Output: ph.Output}
	return func(p string) (string, error) { return run(ctx, plain, p) }
}

// fillStatic merges the fields taken from variables into a step's
// result, so the blackboard carries them exactly like the answered ones.
//
// Filled AFTER the model, and it overwrites: a model that answered a
// field it was never shown has guessed, and the known value wins.
func (d MachineDef) fillStatic(ph MachinePhase, fields map[string]any, v PhaseVars, st MachineState, note func(kind, detail string)) map[string]any {
	static := ph.StaticFields()
	if len(static) == 0 {
		return fields
	}
	if fields == nil {
		fields = map[string]any{}
	}
	v.Step = ph.Name
	v.Machine = chooseStr(v.Machine, d.Name)
	v.Established = d.establishedBlock(ph, st)
	for _, f := range static {
		val := strings.TrimSpace(ResolvePhaseTemplate(f.From, v, st))
		if val == "" && note != nil {
			// The field the author expected to be free is empty, and
			// nothing downstream will say why. {original_input} on a turn
			// that arrived as an image and no words is the real case.
			note("machine_static_empty", "step "+ph.Name+": "+f.Name+" is filled from "+f.From+
				", which resolved to nothing this turn; the field is empty")
		}
		fields[f.Name] = val
	}
	return fields
}
