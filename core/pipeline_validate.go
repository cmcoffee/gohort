package core

import (
	"strconv"
	"strings"
)

// Validate checks a pipeline def is runnable: at least one stage,
// unique non-empty stage names, agent stages name an agent, declared
// output fields are well-formed, and every {stage:NAME} /
// {stage:NAME.field} / fan_over reference points at an EARLIER stage
// (no forward refs or cycles — stages run strictly in order). Returns
// the first problem found, or nil.
//
// Reference checking happens before the current stage is registered, so
// a self-reference fails the same way a forward reference does. (The
// previous implementation registered the stage first and then tried to
// special-case self-fanout, which let `fan_over: <self>` through to a
// runtime error.)
func (d PipelineDef) Validate() error {
	if len(d.Stages) == 0 {
		return Error("pipeline has no stages")
	}
	// done maps an already-validated stage name to its declared output
	// fields. Built as the walk proceeds, so a lookup miss IS the
	// forward/unknown-reference error.
	done := make(map[string]map[string]PipelineFieldType, len(d.Stages))
	if err := validateStageList(d.Stages, done, false); err != nil {
		return err
	}
	return d.validateSessionMeta()
}

// reservedSessionMetaKeys are the summary's own columns. A promoted field
// named for one of them would overwrite the row's id, its title or its date —
// which does not read as a bad field name, it reads as a sidebar that lost its
// entries or started sorting wrongly.
var reservedSessionMetaKeys = map[string]bool{"id": true, "title": true, "date": true, "blocks": true}

// validateSessionMeta checks every field a def promotes onto its runs' rows.
//
// Everything here fails INVISIBLY when it is wrong: a bad reference does not
// error at run time, it produces a row with a blank pill, and the author's
// first theory is always that the panel is broken rather than that the name
// does not resolve. So the reference has to be proven against the definition
// at save time, where it is cheap.
func (d PipelineDef) validateSessionMeta() error {
	if len(d.SessionMeta) == 0 {
		return nil
	}
	declared := make(map[string]map[string]bool, len(d.Stages))
	for _, st := range d.Stages {
		fields := make(map[string]bool, len(st.Output))
		for _, f := range st.Output {
			fields[f.Name] = true
		}
		declared[st.Name] = fields
	}
	var probs []string
	seen := map[string]bool{}
	for _, raw := range d.SessionMeta {
		ref := strings.TrimSpace(raw)
		stage, field, ok := strings.Cut(ref, ".")
		if !ok || strings.TrimSpace(stage) == "" || strings.TrimSpace(field) == "" {
			probs = append(probs, "session_meta "+strconv.Quote(raw)+" is not a \"stage.field\" reference")
			continue
		}
		fields, isStage := declared[stage]
		if !isStage {
			probs = append(probs, "session_meta "+ref+": no top-level stage named "+strconv.Quote(stage)+
				" (a loop body stage cannot be promoted — it holds a different value every pass)")
			continue
		}
		if !fields[field] {
			probs = append(probs, "session_meta "+ref+": stage "+stage+" declares no output field "+strconv.Quote(field)+
				" — only a stage with an `output` contract has fields to promote")
			continue
		}
		if reservedSessionMetaKeys[strings.ToLower(field)] {
			probs = append(probs, "session_meta "+ref+": "+strconv.Quote(field)+" is one of the summary's own columns and would overwrite it")
			continue
		}
		if seen[field] {
			probs = append(probs, "session_meta "+ref+": a field named "+strconv.Quote(field)+" is already promoted — a row carries one value per name")
			continue
		}
		seen[field] = true
	}
	switch len(probs) {
	case 0:
		return nil
	case 1:
		return Error(probs[0])
	}
	return Error("this pipeline has " + strconv.Itoa(len(probs)) + " session_meta problems — fix them all in one revision:\n- " + strings.Join(probs, "\n- "))
}

// validateStageList validates one stage list against the scope built so
// far, recursing once into a loop's Body. done accumulates every
// validated stage's declared fields, so a lookup miss IS the
// forward/unknown-reference error. inLoop marks the recursive call, which
// is how the one-level depth rule is enforced.
// Every INDEPENDENT problem is reported, not just the first.
//
// A pipeline is authored as one object, so its mistakes arrive as a set: a
// wrong field type, an output on a stage that can't take one, and a fan_over
// written as a prompt template were all present in the same submission, and
// returning them one at a time turned one fix into three round-trips, each
// costing a full re-send of the definition. Checks that would CASCADE are still
// suppressed — an unnamed stage skips its own remaining checks, and a field
// reference into a stage whose output failed is that stage's problem, not a
// second one.
func validateStageList(stages []PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) error {
	probs := stageListProblems(stages, done, inLoop)
	switch len(probs) {
	case 0:
		return nil
	case 1:
		return Error(probs[0])
	}
	return Error("this pipeline has " + strconv.Itoa(len(probs)) + " problems — fix them all in one revision:\n- " + strings.Join(probs, "\n- "))
}

// stageListProblems is validateStageList's collector. Split out so a LOOP
// BODY's problems flatten into the caller's list instead of arriving as one
// already-joined string: nested, the count was wrong and the header printed
// twice ("this pipeline has 2 problems" followed by a bullet repeating it).
func stageListProblems(stages []PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) []string {
	var probs []string
	badOutput := map[string]bool{} // stages whose declared output didn't parse
	for i, s := range stages {
		// Identity first: every check below reads the name, and reporting
		// "stage : output field..." for a nameless stage helps nobody. These
		// end the stage rather than adding to it.
		switch {
		case s.Name == "":
			probs = append(probs, "stage "+strconv.Itoa(i+1)+" has no name")
			continue
		case done[s.Name] != nil || badOutput[s.Name]:
			probs = append(probs, "duplicate stage name: "+s.Name)
			continue
		case StageThinkMode(s) != "" && StageThinkMode(s) != "on" && StageThinkMode(s) != "off":
			probs = append(probs, "stage "+s.Name+": think must be \"on\", \"off\", or empty to inherit, got "+strconv.Quote(s.ThinkMode))
		case !validReach(s.Reach):
			probs = append(probs, "stage "+s.Name+": reach must be \"read\", \"none\", or empty to inherit everything, got "+strconv.Quote(s.Reach))
			continue
		case strings.Contains(s.Name, "."):
			// A dot would make {stage:a.b} ambiguous between a stage
			// named "a.b" and field "b" of stage "a".
			probs = append(probs, "stage name may not contain a dot: "+s.Name)
			continue
		}
		add := func(err error) {
			if err != nil {
				probs = append(probs, err.Error())
			}
		}
		if s.Kind == StageAgent && s.Agent == "" {
			probs = append(probs, "stage "+s.Name+" is kind=agent but names no agent")
		}
		// A fanout stage runs as a worker by default (over the stage's
		// resolved tools) and dispatches only when it names an agent — so
		// the agent is optional. What it MUST have is something to fan
		// over.
		if s.Kind == StagePanel {
			switch {
			case len(s.Panel) < 2:
				// A panel of one is an agent stage (or a worker stage)
				// wearing a heavier word. Say which, because the fix is to
				// change the kind rather than to add a voice nobody wanted.
				probs = append(probs, "stage "+s.Name+": a panel needs at least two voices — with one, use kind \"agent\" (or \"worker\") instead")
			case len(s.Panel) > panelMaxVoices:
				probs = append(probs, "stage "+s.Name+": "+strconv.Itoa(len(s.Panel))+" voices is past the cap of "+
					strconv.Itoa(panelMaxVoices)+" — every voice is a model call per round")
			}
			if len(s.Output) > 0 {
				probs = append(probs, "stage "+s.Name+": a panel produces several voices, not one declared shape — "+
					"declare the output on the stage that reads it instead")
			}
			if len(s.Body) > 0 {
				probs = append(probs, "stage "+s.Name+": a panel voice is one contribution, so it takes no body — "+
					"for multi-step branches use kind \"fanout\"")
			}
			if s.Count > panelMaxRounds {
				probs = append(probs, "stage "+s.Name+": "+strconv.Itoa(s.Count)+" rounds is past the cap of "+
					strconv.Itoa(panelMaxRounds))
			}
		}
		// count_from is honored by the two kinds that repeat. On anything else
		// it is a control that does nothing, and an author who set it believes
		// their stage takes its count from the form.
		if strings.TrimSpace(s.CountFrom) != "" && s.Kind != StagePanel && s.Kind != StageLoop {
			probs = append(probs, "stage "+s.Name+": count_from is only read by kind=panel (rounds) and kind=loop (passes) — "+
				"nothing else repeats, so there is no count for it to set")
		}
		if s.Kind != StagePanel && len(s.Panel) > 0 {
			probs = append(probs, "stage "+s.Name+": only a kind \"panel\" stage has voices")
		}
		if s.Kind == StageFanout && s.FanOver == "" {
			probs = append(probs, "stage "+s.Name+" is kind=fanout but names no fan_over stage")
		}
		if s.Kind == StageFanout && len(s.Output) > 0 {
			probs = append(probs, "stage "+s.Name+": output is not valid on kind=fanout (a fanout produces a joined per-branch block, not one JSON object)")
		}
		if s.Kind != StageLoop && s.Kind != StageFanout && len(s.Body) > 0 {
			probs = append(probs, "stage "+s.Name+": body is only valid on kind=loop and kind=fanout")
		}
		if s.Kind == StageFanout && len(s.Body) > 0 {
			own, bodyProbs := validateFanoutBody(s, done, inLoop)
			add(own)
			probs = append(probs, bodyProbs...)
		}
		if s.Kind == StageLoop {
			own, bodyProbs := validateLoopStage(s, done, inLoop)
			add(own)
			probs = append(probs, bodyProbs...)
		}
		if s.Kind == StageBranch {
			add(validateBranchStage(s, stages, i, done, inLoop))
		} else if s.When != "" || s.SkipTo != "" {
			probs = append(probs, "stage "+s.Name+": when/skip_to are only valid on kind=branch")
		}
		add(validateStageModel(s))
		if s.Kind == StageTool {
			add(validateToolStage(s, done))
		} else if s.Tool != "" || len(s.Args) > 0 {
			probs = append(probs, "stage "+s.Name+": tool/args are only valid on kind=tool")
		}
		if s.Kind == StageMachine {
			if strings.TrimSpace(s.Machine) == "" {
				probs = append(probs, "stage "+s.Name+" is kind=machine but names no machine")
			}
			if strings.TrimSpace(s.Agent) != "" {
				probs = append(probs, "stage "+s.Name+": a machine stage runs a machine, so it does not also name an agent")
			}
		} else if strings.TrimSpace(s.Machine) != "" {
			probs = append(probs, "stage "+s.Name+": machine is only valid on kind=machine")
		}
		if p := doubleBraceProblem(s.Name, "prompt", s.Prompt); p != nil {
			add(p)
		}
		for k, v := range s.Args {
			if p := doubleBraceProblem(s.Name, "args."+k, v); p != nil {
				add(p)
			}
		}
		for _, ref := range stageRefs(s.Prompt) {
			if name, _ := SplitStageRef(ref); !badOutput[name] {
				add(checkStageRef(s.Name, "prompt", ref, done))
			}
		}
		if s.FanOver != "" {
			src, field := SplitStageRef(s.FanOver)
			if !badOutput[src] {
				add(checkStageRef(s.Name, "fan_over", s.FanOver, done))
				// A field reference has to BE a list; the whole-output form
				// is parsed leniently at run time and can't be checked here.
				if field != "" && done[src] != nil {
					if t := done[src][field]; t != FieldList {
						probs = append(probs, "stage "+s.Name+" fans over "+s.FanOver+", which is declared "+string(t)+", not list")
					}
				}
			}
		}
		own, err := validateOutputFields(s.Name, s.Output, false)
		if err != nil {
			probs = append(probs, err.Error())
			badOutput[s.Name] = true
		}
		// Registered even when it failed, so a LATER stage referencing this one
		// by name doesn't also report "unknown stage" — one mistake, one line.
		if own == nil {
			own = map[string]PipelineFieldType{}
		}
		// A fanout declares no shape of its own, but a fanout whose BODY
		// ends in a declared stage carries one per branch. Registering it
		// here is what lets a later stage read {stage:dig.items} and, in
		// particular, fan over the survivors.
		for k, v := range fanoutCollectedShape(s) {
			own[k] = v
		}
		done[s.Name] = own
	}
	return probs
}

// validateLoopStage checks a kind="loop" stage and its Body. The body is
// validated against a COPY of the outer scope: body stages can read
// what ran before the loop and each other, but their names never reach
// the caller's scope, so a later stage referencing one is an unknown-
// stage error rather than a silent read of whichever value the last
// iteration left behind.
func validateLoopStage(s PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) (error, []string) {
	if inLoop {
		return Error("stage " + s.Name + ": loops do not nest — one level only (put the inner work in its own pipeline and call it from a stage)"), nil
	}
	if len(s.Body) == 0 {
		return Error("stage " + s.Name + " is kind=loop but has no body stages to repeat"), nil
	}
	if len(s.Output) > 0 {
		// Naming the loop's result is a reasonable thing to want, so say where
		// the name already is rather than only that this slot is wrong.
		return Error("stage " + s.Name + ": output is not valid on kind=loop — a loop has no shape of its own to declare. Drop it: a later stage reads this loop as {stage:" + s.Name + "}, which is the last pass (or every pass joined, with collect=\"all\"). Declare output on the BODY stage that produces the value if a body stage needs to read it mid-pass, or if until has to test a bool"), nil
	}
	if s.Count < 1 {
		return Error("stage " + s.Name + ": kind=loop needs count (how many times to repeat, 1-" + strconv.Itoa(loopMaxIterations) + ")"), nil
	}
	if s.Count > loopMaxIterations {
		return Error("stage " + s.Name + ": count " + strconv.Itoa(s.Count) + " exceeds the maximum of " + strconv.Itoa(loopMaxIterations) + " — a pipeline runs unattended, so the ceiling is fixed"), nil
	}
	switch strings.TrimSpace(s.Collect) {
	case "", "last", "all":
	default:
		return Error("stage " + s.Name + ": collect must be \"last\" or \"all\", got " + strconv.Quote(s.Collect)), nil
	}
	// Body scope starts as a copy of the outer one so the body can read
	// earlier stages without leaking its own names back out.
	inner := make(map[string]map[string]PipelineFieldType, len(done)+len(s.Body))
	for k, v := range done {
		inner[k] = v
	}
	bodyProbs := stageListProblems(s.Body, inner, true)
	if ref := strings.TrimSpace(s.Until); ref != "" {
		if err := checkLoopUntil(s, ref, done, inner); err != nil {
			return err, bodyProbs
		}
	}
	return nil, bodyProbs
}

// fanoutCollectedShape is what a fanout exposes beyond its joined text:
// the per-branch results, when the body ends in a stage that declared a
// shape to collect. Nil for a single-prompt fan, which has none.
//
// The LAST body stage is the terminal one for this purpose. A branch that
// ended early on a skip did not run it, and that branch simply contributes
// no entry; the declared TYPE of the collection does not change with it.
func fanoutCollectedShape(s PipelineStage) map[string]PipelineFieldType {
	if s.Kind != StageFanout || len(s.Body) == 0 {
		return nil
	}
	if last := s.Body[len(s.Body)-1]; len(last.Output) == 0 {
		return nil
	}
	return map[string]PipelineFieldType{
		"items": FieldList,
		"count": FieldNumber,
	}
}

// validateFanoutBody checks a fanout that runs several stages per item.
//
// A body turns the fan from "one prompt per item" into "a small pipeline
// per item", which is what makes "investigate each of these properly, then
// compare what came back" expressible. The rules are the loop's, for the
// same reasons, plus one of its own about who runs the branch.
func validateFanoutBody(s PipelineStage, done map[string]map[string]PipelineFieldType, inLoop bool) (error, []string) {
	if inLoop {
		return Error("stage " + s.Name + ": bodies do not nest — one level only. Put the inner work in its own pipeline and call it from a stage."), nil
	}
	if strings.TrimSpace(s.Agent) != "" {
		// Both would have to mean something, and neither reading is
		// obviously right: run the agent per item and ignore the body, or
		// run the body and ignore the agent. Refuse instead of choosing.
		return Error("stage " + s.Name + ": a fanout runs its body OR an agent per item, not both. A body branch is several stages of your own; an agent branch is one dispatch. Keep whichever the fan is really for."), nil
	}
	// Body scope starts as a copy of the outer one so a branch can read
	// what was established before the fan, without leaking its own stage
	// names back out to anything after it.
	inner := make(map[string]map[string]PipelineFieldType, len(done)+len(s.Body))
	for k, v := range done {
		inner[k] = v
	}
	return nil, stageListProblems(s.Body, inner, true)
}

// untilShape is the one sentence that turns "until is wrong" into "here is what
// until is". Appended to every until refusal, because the field is the least
// guessable thing in the vocabulary: it is not a condition, it is the NAME of a
// bool a body stage promised to return.
const untilShape = "until reads ONE bool field, by bare name: until:\"check.done\", where a body stage named check declares output:[{\"name\":\"done\",\"type\":\"bool\"}]. It is not an expression — there is no ==, no quotes, no braces. To stop when a critic is satisfied, have the critic stage declare a bool (\"satisfied\") alongside its prose and point until at it."

// looksLikeCondition reports a bare-reference slot written as a comparison.
//
// until and when both read the NAME of a bool, and an author reaching for a
// condition is the failure both of them actually see —
// "critic.satisfied == true", "{stage:critic}.lower() == \"ok\"". Only until
// used to detect it; when fell through to "declares no output field
// satisfied == true", which is true, unhelpful, and cost two rounds of one
// build on its own.
//
// A space alone does not qualify: a stage name may legitimately contain one,
// and every real instance carries an operator or a quote anyway.
func looksLikeCondition(ref string) bool {
	return strings.ContainsAny(ref, "={}<>!'\"")
}

// boolRefShape is what both slots need said when one is written as a condition.
const boolRefShape = "It reads ONE bool field, by bare name — when:\"check.done\" — where the stage named check declares output:[{\"name\":\"done\",\"type\":\"bool\"}]. It is NOT an expression: no ==, no quotes, no braces, no method calls. To branch on a critic being satisfied, have that stage declare a bool alongside its prose and point when at it."

// checkLoopUntil validates a loop's early exit.
//
// Every refusal carries the shape, because the observed failures were not typos
// — they were an author reaching for a CONDITION. Written as
// {stage:critic.feedback} == 'SATISFIED', then as stage:critic.feedback ==
// 'SATISFIED', then abandoned: three rounds, after which the loop was replaced
// by five hand-copied stage pairs that cannot stop early at all.
func checkLoopUntil(s PipelineStage, ref string, done, inner map[string]map[string]PipelineFieldType) error {
	bad := func(why string) error {
		return Error("stage " + s.Name + ": until " + why + ". " + untilShape)
	}
	if looksLikeCondition(ref) {
		return bad("is written as a condition (" + strconv.Quote(ref) + ")")
	}
	if p := barePrefixProblem(s.Name, "until", ref); p != nil {
		return p
	}
	name, field := SplitStageRef(ref)
	if field == "" {
		return bad("names a stage (" + strconv.Quote(ref) + ") and not a field of one")
	}
	if _, ok := inner[name]; !ok {
		return bad("references " + strconv.Quote(name) + ", which is not a stage in this loop's body")
	}
	t, declared := inner[name][field]
	if !declared {
		return bad("references " + strconv.Quote(ref) + ", but " + name + " declares no output field " + strconv.Quote(field))
	}
	if t != FieldBool {
		return bad("references " + ref + ", which is declared " + string(t) + ", not bool")
	}
	if _, outer := done[name]; outer {
		return Error("stage " + s.Name + ": until references " + ref + ", which is OUTSIDE the loop — its value never changes between passes, so the loop would either run once or all " + strconv.Itoa(s.Count) + " times. Point it at a body stage.")
	}
	return nil
}
