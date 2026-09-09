// eval_tool.go — the eval suite as a TOOL.
//
// Suites were a page and an API and nothing else, which left an agent able to
// CHANGE another agent and unable to find out whether the change helped. That
// asymmetry is the whole reason an "improver" agent is a bad idea without this
// file: an agent that edits and cannot measure will always find something to
// improve, and nothing will ever tell it that last week's edit made things
// worse. See the investigator archetype for the same argument about looking
// before answering.
//
// Everything here goes through the same RunEvalSuite and SaveEvalSuite the page
// calls, so a score an agent reports and a score a person reads cannot
// disagree, and a case a model writes is validated by the same rules as one a
// person types.
package orchestrate

import (
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// evalToolCaseCap bounds how many passing case names are echoed back.
//
// A failure is worth its full body — reasons, tools called, the output that
// missed. A pass is worth its name. A thirty-case suite whose every case
// rendered in full would spend the caller's context on the twenty-six results
// that need no action.
const evalToolCaseCap = 40

// evalTool is an agent's access to the eval suites its owner has saved:
// list what exists, run one and read the per-case result, and read the score
// history so "did that edit help" has a before to compare against.
//
// t is optional in the same way every other authoring tool's is — three
// dispatch paths pass nil — so everything reads the SESSION first and the app
// pointer is checked rather than assumed.
func evalTool(t *chatTurn) *GroupedTool {
	gt := NewGroupedTool("eval",
		"Run and read the eval suites saved for this owner's agents, pipelines, tools and machines. "+
			"A suite is a set of cases plus the thing they grade; running one returns a pass RATE per case, "+
			"which is the signal on a non-deterministic model — a single pass is an anecdote. "+
			"Use it to MEASURE a change you made instead of asserting it helped: run before, edit, run after, compare. "+
			"When a failure you just diagnosed has no case, write one — a suite only measures what somebody thought to assert. "+
			"Actions: list, run, history, add_case, remove_case, create_suite.")

	gt.AddAction("list", &GroupedToolAction{
		Description: "List the eval suites saved for this owner, with what each one grades, how many cases it holds, and its most recent score.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			udb, _, err := evalToolScope(t, sess)
			if err != nil {
				return "", err
			}
			suites := ListEvalSuites(udb)
			if len(suites) == 0 {
				return "No eval suites saved. One is created on the Evals page, against an agent, pipeline, tool or machine; " +
					"until a suite exists there is nothing here to measure a change against.", nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Eval suites (%d) — run one with eval(action=\"run\", suite=\"<name or id>\"):\n\n", len(suites))
			for _, s := range suites {
				fmt.Fprintf(&b, "- %s — grades the %s %q, %d case%s, %d run%s each, tools %s\n",
					s.Name, s.TargetKind, s.TargetID, len(s.Cases), plural(len(s.Cases)),
					s.RunCount(), plural(s.RunCount()), evalToolStubWord(s))
				if runs := ListEvalRuns(udb, s.ID); len(runs) > 0 {
					last := runs[0]
					fmt.Fprintf(&b, "    last: %s (%s) on version %s%s\n",
						last.Rate(), evalOutcome(last), evalToolShortHash(last.TargetHash), evalToolNote(last.Note))
				}
				fmt.Fprintf(&b, "    id: %s\n", s.ID)
			}
			return b.String(), nil
		},
	})

	gt.AddAction("run", &GroupedToolAction{
		// CapExecute, and not because the common case deserves it: with tools
		// stubbed — the default, and what "tools scripted" below means — a run
		// is an LLM loop against scripted returns. With stubbing OFF a suite
		// sends the email and files the ticket, once per case per run. The
		// capability is declared for the shape the tool CAN take, because a
		// reach that admits this tool has no way to know which mode the suite
		// it is about to run was saved in.
		Description: "Run one eval suite and return the pass rate per case. Cases with tools STUBBED (the default) return scripted tool results, so nothing external happens; a suite saved with stubbing off executes its target's tools for real. Records the run, so the score becomes part of the suite's history. Give a note saying what changed — it is what makes the history readable later.",
		Params: map[string]ToolParam{
			"suite": {Type: "string", Description: "The suite's name or id, as shown by eval(action=\"list\")."},
			"note":  {Type: "string", Description: "What changed since the last run, e.g. \"shortened the refund-policy rule\". Stored with the score."},
			"runs":  {Type: "integer", Description: "Override how many times each case runs, for THIS run only. The suite's own setting is used when omitted. A single pass on a non-deterministic target is an anecdote; 3 or more gives you a rate."},
		},
		Required: []string{"suite"},
		Caps:     []Capability{CapRead, CapWrite, CapExecute},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			udb, user, err := evalToolScope(t, sess)
			if err != nil {
				return "", err
			}
			suite, err := findEvalSuite(udb, stringArg(args, "suite"))
			if err != nil {
				return "", err
			}
			// A per-run override of the repeat count.
			//
			// It exists because it was ASKED FOR and silently ignored. A caller
			// that passes runs=3 to a suite saved at 1 gets one execution and a
			// result that says "1/1", and nothing anywhere says the number it
			// asked for went nowhere — so it reports a rate it never measured.
			// Grouped tools do not reject unknown params, which makes an absent
			// parameter indistinguishable from an accepted one.
			if n := intArgOr(args, "runs", 0); n > 0 {
				suite.Runs = n
			}
			// The turn's context, so a Stop reaches a suite already running.
			// runEvalCases checks it per case and returns what it graded, which
			// is why a cancelled run reports partial rather than nothing.
			run, err := t.app.RunEvalSuite(sess.Context(), udb, user, suite, stringArg(args, "note"))
			if err != nil {
				return "", err
			}
			out := renderEvalRunForTool(suite, run)
			// A machine narrows tools per phase and advances across turns; an
			// eval case is one turn against a fresh session, so the score is
			// measured on the UNNARROWED catalog. Said here rather than
			// swallowed: a pass on a suite whose agent loses half its tools in
			// the phase it actually answers from is the kind of green that
			// costs somebody an afternoon.
			if suite.TargetKind == EvalTargetAgent {
				if agent, ok := findAgentByNameOrID(udb, user, suite.TargetID); ok {
					if m := t.app.evalAgentCarriesMachine(udb, agent); m != "" {
						out += "\nNOTE: this agent carries the machine " + strconv.Quote(m) +
							", whose phases narrow which tools a turn may reach. The score above is measured WITHOUT that narrowing, " +
							"so a tool the agent used here may be absent in the phase it actually answers from. Check that machine's phase tool lists before trusting a pass.\n"
					}
				}
			}
			return out, nil
		},
	})

	gt.AddAction("history", &GroupedToolAction{
		Description: "The recent runs of one suite, newest first: score, the version fingerprint of what was graded, and the note. Two runs sharing a fingerprint graded the SAME target, so their difference is noise; two that differ graded a change, so their difference is the answer.",
		Params: map[string]ToolParam{
			"suite": {Type: "string", Description: "The suite's name or id, as shown by eval(action=\"list\")."},
			"limit": {Type: "integer", Description: "How many runs to show, newest first. Default 10."},
		},
		Required: []string{"suite"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			udb, _, err := evalToolScope(t, sess)
			if err != nil {
				return "", err
			}
			suite, err := findEvalSuite(udb, stringArg(args, "suite"))
			if err != nil {
				return "", err
			}
			runs := ListEvalRuns(udb, suite.ID)
			if len(runs) == 0 {
				return fmt.Sprintf("%q has never been run. eval(action=\"run\", suite=%q) establishes the before.",
					suite.Name, suite.Name), nil
			}
			limit := 10
			if n, ok := args["limit"]; ok {
				if v, cerr := strconv.Atoi(strings.TrimSpace(fmt.Sprint(n))); cerr == nil && v > 0 {
					limit = v
				}
			}
			if limit > len(runs) {
				limit = len(runs)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s — %d run%s, newest first:\n\n", suite.Name, len(runs), plural(len(runs)))
			for _, r := range runs[:limit] {
				when := r.Started.Local().Format("2006-01-02 15:04")
				switch {
				case r.Err != "":
					fmt.Fprintf(&b, "- %s  FAILED TO RUN: %s\n", when, r.Err)
				case r.Finished.IsZero():
					// No Finished and no error is a run the process did not
					// live to complete, not a run that scored zero.
					fmt.Fprintf(&b, "- %s  did not finish (interrupted)\n", when)
				default:
					fmt.Fprintf(&b, "- %s  %s (%s) on version %s%s\n",
						when, r.Rate(), evalOutcome(r), evalToolShortHash(r.TargetHash), evalToolNote(r.Note))
				}
			}
			return b.String(), nil
		},
	})

	gt.AddAction("add_case", &GroupedToolAction{
		Description: "Add a case to a suite, or REPLACE the case of that name if it already has one. A case is a prompt plus at least one assertion; a case that asserts nothing passes whatever the target does, which is worse than no case because it raises the score while grading nothing. Assert on the reply (must_include / must_not_include), on the tool CALLS (must_call_tools / must_not_call_tools, graded from the actual trace so it catches \"narrated it but never called the tool\"), on a pipeline's declared output fields (must_fields), or hand a criterion to an LLM judge (judge_prompt).",
		Params: map[string]ToolParam{
			"suite":               {Type: "string", Description: "The suite's name or id, as shown by eval(action=\"list\")."},
			"name":                {Type: "string", Description: "Short label for the case, e.g. \"asks_clarifying\". This is the key: adding a case with an existing name replaces it."},
			"prompt":              {Type: "string", Description: "The user message to send the target."},
			"must_include":        {Type: "array", Description: "Case-insensitive substrings the reply must contain.", Items: &ToolParam{Type: "string"}},
			"must_not_include":    {Type: "array", Description: "Case-insensitive substrings the reply must NOT contain.", Items: &ToolParam{Type: "string"}},
			"must_call_tools":     {Type: "array", Description: "Tool names the target must call at least once. Graded from the tool-call trace, not the reply text.", Items: &ToolParam{Type: "string"}},
			"must_not_call_tools": {Type: "array", Description: "Tool names the target must NOT call.", Items: &ToolParam{Type: "string"}},
			"judge_prompt":        {Type: "string", Description: "A yes/no criterion for an LLM judge, e.g. \"the reply names the policy that decided it\". Use when the thing you want is a property of the answer rather than a string in it."},
			"must_fields":         {Type: "object", Description: "For a pipeline: expected values of the FINAL stage's declared output fields, e.g. {\"winner\": \"for\"}. Compared case-insensitively."},
			"stub_results":        {Type: "object", Description: "What each tool should RETURN instead of running, keyed by tool name. Lets a multi-step case behave like production with no side effect. Ignored unless the suite is stubbed."},
			"notes":               {Type: "string", Description: "Why this case exists — the failure it was written for. Not graded."},
		},
		Required: []string{"suite", "name", "prompt"},
		Caps:     []Capability{CapRead, CapWrite},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			udb, _, err := evalToolScope(t, sess)
			if err != nil {
				return "", err
			}
			suite, err := findEvalSuite(udb, stringArg(args, "suite"))
			if err != nil {
				return "", err
			}
			c := evalCaseFromArgs(args)
			replaced := false
			for i := range suite.Cases {
				if strings.EqualFold(strings.TrimSpace(suite.Cases[i].Name), c.Name) {
					suite.Cases[i] = c
					replaced = true
					break
				}
			}
			if !replaced {
				suite.Cases = append(suite.Cases, c)
			}
			saved, err := SaveEvalSuite(udb, suite)
			if err != nil {
				// Validate's own words. It refuses at SAVE time precisely so a
				// bad case does not fail later as the TARGET looking wrong.
				return "", err
			}
			verb := "Added"
			if replaced {
				verb = "Replaced"
			}
			return fmt.Sprintf("%s case %q in %q — %d case%s now. It is not measured until the suite runs: eval(action=\"run\", suite=%q).",
				verb, c.Name, saved.Name, len(saved.Cases), plural(len(saved.Cases)), saved.Name), nil
		},
	})

	gt.AddAction("remove_case", &GroupedToolAction{
		Description: "Remove one case from a suite by name. Use it for a case that no longer describes anything you want — not for one that is failing, which is the case doing its job.",
		Params: map[string]ToolParam{
			"suite": {Type: "string", Description: "The suite's name or id."},
			"name":  {Type: "string", Description: "The case name to remove, as shown by a run."},
		},
		Required: []string{"suite", "name"},
		Caps:     []Capability{CapRead, CapWrite},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			udb, _, err := evalToolScope(t, sess)
			if err != nil {
				return "", err
			}
			suite, err := findEvalSuite(udb, stringArg(args, "suite"))
			if err != nil {
				return "", err
			}
			want := strings.TrimSpace(stringArg(args, "name"))
			kept := make([]EvalCase, 0, len(suite.Cases))
			var names []string
			for _, c := range suite.Cases {
				names = append(names, c.Name)
				if strings.EqualFold(strings.TrimSpace(c.Name), want) {
					continue
				}
				kept = append(kept, c)
			}
			if len(kept) == len(suite.Cases) {
				return "", Error("no case named " + strconv.Quote(want) + " in " + strconv.Quote(suite.Name) +
					" — it holds: " + strings.Join(names, ", "))
			}
			if len(kept) == 0 {
				// Validate would refuse this anyway; refusing HERE says why in
				// terms of what was asked rather than as a save failure.
				return "", Error("that is the last case in " + strconv.Quote(suite.Name) +
					", and a suite with no cases grades nothing. Add its replacement first, or delete the suite on the Evals page.")
			}
			suite.Cases = kept
			saved, err := SaveEvalSuite(udb, suite)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Removed case %q from %q — %d case%s left.",
				want, saved.Name, len(saved.Cases), plural(len(saved.Cases))), nil
		},
	})

	gt.AddAction("create_suite", &GroupedToolAction{
		// No live-mode parameter, deliberately. Stubbing off is what makes a
		// suite send the emails and spend the money on every run, and that is a
		// decision for a person on the Evals page — not one a model can reach
		// by filling in a field it half-understood.
		Description: "Create a new eval suite against an agent, pipeline, tool or machine, with its first case. Suites are always created with tools STUBBED; running one for real is a setting only a person can turn on. Use this when the thing you want to measure has no suite yet; add the rest of the cases with add_case.",
		Params: map[string]ToolParam{
			"name":                {Type: "string", Description: "What the suite is called, e.g. \"Support bot regression\"."},
			"target_kind":         {Type: "string", Description: "What kind of thing it grades.", Enum: []string{"agent", "pipeline", "tool", "machine"}},
			"target":              {Type: "string", Description: "The name or id of the agent / pipeline / tool / machine being graded."},
			"runs":                {Type: "integer", Description: "How many times each case runs. The pass RATE is the signal on a non-deterministic target, so prefer 3 or more; default 1."},
			"case_name":           {Type: "string", Description: "Short label for the first case."},
			"prompt":              {Type: "string", Description: "The user message the first case sends."},
			"must_include":        {Type: "array", Description: "Case-insensitive substrings the reply must contain.", Items: &ToolParam{Type: "string"}},
			"must_not_include":    {Type: "array", Description: "Case-insensitive substrings the reply must NOT contain.", Items: &ToolParam{Type: "string"}},
			"must_call_tools":     {Type: "array", Description: "Tool names the target must call at least once.", Items: &ToolParam{Type: "string"}},
			"must_not_call_tools": {Type: "array", Description: "Tool names the target must NOT call.", Items: &ToolParam{Type: "string"}},
			"judge_prompt":        {Type: "string", Description: "A yes/no criterion for an LLM judge."},
			"must_fields":         {Type: "object", Description: "For a pipeline: expected values of the final stage's declared fields."},
			"notes":               {Type: "string", Description: "Why this case exists. Not graded."},
		},
		Required: []string{"name", "target_kind", "target", "case_name", "prompt"},
		Caps:     []Capability{CapRead, CapWrite},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			udb, user, err := evalToolScope(t, sess)
			if err != nil {
				return "", err
			}
			kind := EvalTargetKind(strings.ToLower(strings.TrimSpace(stringArg(args, "target_kind"))))
			c := evalCaseFromArgs(args)
			c.Name = strings.TrimSpace(stringArg(args, "case_name"))
			suite := EvalSuite{
				Name:       strings.TrimSpace(stringArg(args, "name")),
				TargetKind: kind,
				TargetID:   strings.TrimSpace(stringArg(args, "target")),
				Cases:      []EvalCase{c},
				Runs:       intArgOr(args, "runs", 1),
			}
			// Resolved BEFORE saving, so a suite pointing at a target nobody has
			// is refused while the caller can still fix the name. Saved, it
			// would fail on every future run as the target being missing, which
			// reads as something breaking rather than as a typo.
			if _, rerr := t.app.resolveEvalTarget(udb, user, suite); rerr != nil {
				return "", rerr
			}
			saved, err := SaveEvalSuite(udb, suite)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Created suite %q grading the %s %q, with case %q, %d run%s each, tools STUBBED. "+
				"Add more cases with eval(action=\"add_case\", suite=%q), then eval(action=\"run\", suite=%q) for the first score.",
				saved.Name, saved.TargetKind, saved.TargetID, c.Name, saved.RunCount(), plural(saved.RunCount()),
				saved.Name, saved.Name), nil
		},
	})

	return gt
}

// evalCaseFromArgs builds a case from the shared assertion parameters.
//
// Shared by add_case and create_suite so the two cannot drift into accepting
// different assertions — an author who learned the arguments in one has learned
// the other. What it does NOT do is refuse an empty case: EvalSuite.Validate
// owns that rule, and duplicating it here would be a second definition of
// "asserts nothing" to keep in step.
func evalCaseFromArgs(args map[string]any) EvalCase {
	return EvalCase{
		Name:             strings.TrimSpace(stringArg(args, "name")),
		Prompt:           strings.TrimSpace(stringArg(args, "prompt")),
		MustInclude:      stringSliceArg(args, "must_include"),
		MustNotInclude:   stringSliceArg(args, "must_not_include"),
		MustCallTools:    stringSliceArg(args, "must_call_tools"),
		MustNotCallTools: stringSliceArg(args, "must_not_call_tools"),
		JudgePrompt:      strings.TrimSpace(stringArg(args, "judge_prompt")),
		MustFields:       stringMapArg(args, "must_fields"),
		StubResults:      stringMapArg(args, "stub_results"),
		Notes:            strings.TrimSpace(stringArg(args, "notes")),
	}
}

// intArgOr reads a whole-number argument, tolerating the string and float forms
// a model sends, and falls back rather than failing: a bad run count is not
// worth refusing a suite over.
func intArgOr(args map[string]any, key string, fallback int) int {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(fmt.Sprint(v), ".0")))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

// evalToolScope resolves whose suites these are.
//
// The session first and the turn second, for the reason builderAuthoringTools
// states: t is nil on delegated runs, and dereferencing it there is a panic on
// every one of them. The app pointer has no second source, so a caller without
// one is told plainly rather than crashing — the read actions could technically
// work without it, but a tool that lists suites it cannot run is a worse answer
// than a clear refusal.
func evalToolScope(t *chatTurn, sess *ToolSession) (Database, string, error) {
	user := ""
	if sess != nil {
		user = sess.Username
	}
	if user == "" && t != nil {
		user = t.user
	}
	if t == nil || t.app == nil {
		return nil, "", Error("evals are not reachable from this run — it has no app context to resolve a suite's target against")
	}
	if user == "" {
		return nil, "", Error("evals are per-owner and this run has no user")
	}
	udb := UserDB(t.app.DB, user)
	if udb == nil {
		return nil, "", Error("no store for " + strconv.Quote(user))
	}
	return udb, user, nil
}

// findEvalSuite resolves a suite by id or by name.
//
// By NAME as well as id because a model has the name — it is what list printed
// and what a person says — and a UUID it would have to copy exactly. Name match
// is case-insensitive and exact: a prefix match would silently grade the wrong
// suite the first time somebody has "Support bot" and "Support bot v2".
func findEvalSuite(udb Database, want string) (EvalSuite, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return EvalSuite{}, Error("name the suite to run — eval(action=\"list\") shows what exists")
	}
	if s, ok := LoadEvalSuite(udb, want); ok {
		return s, nil
	}
	var names []string
	for _, s := range ListEvalSuites(udb) {
		if strings.EqualFold(strings.TrimSpace(s.Name), want) {
			return s, nil
		}
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return EvalSuite{}, Error("no eval suites are saved for this owner, so there is nothing named " + strconv.Quote(want))
	}
	return EvalSuite{}, Error("no eval suite named " + strconv.Quote(want) + " — there is: " + strings.Join(names, ", "))
}

// renderEvalRunForTool is the result an agent reads.
//
// Failures in full and passes by name, because the two are read for different
// reasons: a failure is the thing to act on and needs its reason, its tool
// calls and what the target actually said; a pass needs only to be counted.
// The headline goes first so a caller that reads one line reads the number.
func renderEvalRunForTool(suite EvalSuite, run EvalRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s (%s), grading the %s %q on version %s.\n",
		suite.Name, run.Rate(), evalOutcome(run), suite.TargetKind, suite.TargetID,
		evalToolShortHash(run.TargetHash))
	fmt.Fprintf(&b, "Each case ran %d time%s, tools %s.\n",
		suite.RunCount(), plural(suite.RunCount()), evalToolStubWord(suite))

	if len(run.Results) < run.Total {
		// The cancelled shape. Said plainly, because a partial score read as a
		// whole one is a wrong answer rather than a missing one.
		fmt.Fprintf(&b, "\nSTOPPED EARLY: %d of %d cases were graded. The rate above covers only those.\n",
			len(run.Results), run.Total)
	}

	var failed, passed []EvalResult
	for _, r := range run.Results {
		if r.Passed {
			passed = append(passed, r)
			continue
		}
		failed = append(failed, r)
	}

	if len(failed) > 0 {
		b.WriteString("\nFAILED:\n")
		for i, r := range failed {
			if i >= evalToolCaseCap {
				fmt.Fprintf(&b, "\n(%d more failures not shown)\n", len(failed)-evalToolCaseCap)
				break
			}
			fmt.Fprintf(&b, "\n%s\n", evalCaseTitle(r))
			for _, line := range strings.Split(strings.TrimRight(evalCaseBody(r), "\n"), "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}

	if len(passed) > 0 {
		b.WriteString("\nPASSED: ")
		var names []string
		for i, r := range passed {
			if i >= evalToolCaseCap {
				names = append(names, fmt.Sprintf("and %d more", len(passed)-evalToolCaseCap))
				break
			}
			if r.Runs > 1 {
				names = append(names, fmt.Sprintf("%s (%d/%d)", r.Name, r.Passes, r.Runs))
				continue
			}
			names = append(names, r.Name)
		}
		b.WriteString(strings.Join(names, ", ") + "\n")
	}

	// The comparison is the point of recording, so say where to get it rather
	// than leaving the caller to guess that a history exists.
	fmt.Fprintf(&b, "\nCompare with earlier runs: eval(action=\"history\", suite=%q).\n", suite.Name)
	return b.String()
}

// evalToolStubWord says which mode a suite runs in, in words a reader acts on
// rather than the stored value.
func evalToolStubWord(s EvalSuite) string {
	if s.Stubbed() {
		return "STUBBED (scripted results, nothing external happens)"
	}
	return "LIVE (the target's tools execute for real, with their side effects)"
}

// evalToolShortHash keeps a fingerprint readable. It is an identity to compare,
// never something anybody types, so the first eight characters carry it.
func evalToolShortHash(h string) string {
	h = strings.TrimSpace(h)
	switch {
	case h == "":
		return "unrecorded"
	case len(h) > 8:
		return h[:8]
	}
	return h
}

func evalToolNote(note string) string {
	if note = strings.TrimSpace(note); note != "" {
		return " — " + note
	}
	return ""
}
