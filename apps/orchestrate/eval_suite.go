// Evals as a primitive: a saved suite naming what it grades, and results that
// survive the run that produced them.
//
// The grading itself is not new and is not touched here — EvalCase already
// asserts on substrings, on the actual tool-call trace, and via an LLM judge,
// and it can script what a tool RETURNS so a multi-step scenario behaves like
// production without the side effect. What was missing is everything around it:
// the cases lived on AgentRecord, so nothing else could be graded, and results
// went to an HTTP response and nowhere else, so "did that edit help" had no
// before to compare against.
//
// See docs/eval-primitive.md.
package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// EvalSuitesTable stores per-user suites, keyed by id.
	EvalSuitesTable = "eval_suites"
	// EvalRunsTable stores their results, keyed "<suiteID>:<runID>" so one
	// suite's history is a prefix scan — the same shape PipelineRunKey uses,
	// for the same reason.
	EvalRunsTable = "eval_runs"
)

// EvalTargetKind is what a suite grades.
//
// Stored rather than inferred from the id: ids are unique per KIND, so a suite
// that guessed would grade whatever it found first the day somebody names a
// pipeline after an agent.
type EvalTargetKind string

const (
	EvalTargetAgent    EvalTargetKind = "agent"
	EvalTargetPipeline EvalTargetKind = "pipeline"
	EvalTargetTool     EvalTargetKind = "tool"
	EvalTargetMachine  EvalTargetKind = "machine"
)

// EvalSuite is a saved set of cases plus the thing they grade.
type EvalSuite struct {
	ID    string `json:"id"`
	Owner string `json:"owner,omitempty"`
	Name  string `json:"name"`
	Desc  string `json:"desc,omitempty"`

	TargetKind EvalTargetKind `json:"target_kind"`
	TargetID   string         `json:"target_id"`

	Cases []EvalCase `json:"cases"`

	// Runs is how many times each case runs. The pass RATE is the signal: a
	// single pass on a non-deterministic model is an anecdote, which is why
	// EvalResult has carried Runs/Passes since before this record existed.
	Runs int `json:"runs,omitempty"`

	// Stub scripts what each tool RETURNS instead of executing it.
	//
	// A POINTER so "unset" is distinguishable from "off", because the default
	// is the whole safety story and it has to survive a record written before
	// anybody thought about it. An eval that runs for real sends the emails,
	// files the tickets and spends the money — every time anybody clicks Run,
	// which for a suite is dozens of times a day. Unset reads as ON.
	Stub *bool `json:"stub,omitempty"`

	Created time.Time `json:"created,omitempty"`
	Updated time.Time `json:"updated,omitempty"`
}

// Stubbed reports whether tool calls are scripted rather than executed.
// Unset means yes; see EvalSuite.Stub.
func (s EvalSuite) Stubbed() bool { return s.Stub == nil || *s.Stub }

// RunCount is how many times each case runs, floored at one.
func (s EvalSuite) RunCount() int {
	if s.Runs < 1 {
		return 1
	}
	return s.Runs
}

// Validate refuses a suite that cannot be run, at SAVE time.
//
// Everything here fails at run time otherwise, one case at a time, in a report
// that reads as the target failing rather than as the suite being wrong.
func (s EvalSuite) Validate() error {
	var probs []string
	if strings.TrimSpace(s.Name) == "" {
		probs = append(probs, "the suite needs a name")
	}
	switch s.TargetKind {
	case EvalTargetAgent, EvalTargetPipeline, EvalTargetTool, EvalTargetMachine:
	case "":
		probs = append(probs, "the suite names no target kind — one of agent | pipeline | tool | machine")
	default:
		probs = append(probs, fmt.Sprintf("unknown target kind %q — use agent | pipeline | tool | machine", s.TargetKind))
	}
	if strings.TrimSpace(s.TargetID) == "" {
		probs = append(probs, "the suite names no target")
	}
	if len(s.Cases) == 0 {
		probs = append(probs, "a suite with no cases grades nothing")
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		name := strings.TrimSpace(c.Name)
		switch {
		case name == "":
			probs = append(probs, fmt.Sprintf("case %d has no name — the name is how a result is read", i+1))
			continue
		case seen[name]:
			probs = append(probs, fmt.Sprintf("duplicate case name %q — two results under one name cannot be told apart", name))
			continue
		}
		seen[name] = true
		if strings.TrimSpace(c.Prompt) == "" {
			probs = append(probs, fmt.Sprintf("case %q has no prompt — there is nothing to send", name))
		}
		if len(c.MustInclude) == 0 && len(c.MustNotInclude) == 0 &&
			len(c.MustCallTools) == 0 && len(c.MustNotCallTools) == 0 &&
			strings.TrimSpace(c.JudgePrompt) == "" {
			// A case that asserts nothing passes unconditionally, which is
			// worse than no case: it raises the score and grades nothing.
			probs = append(probs, fmt.Sprintf("case %q asserts nothing — it would pass whatever the target does", name))
		}
	}
	switch len(probs) {
	case 0:
		return nil
	case 1:
		return Error(probs[0])
	}
	return Error(fmt.Sprintf("this suite has %d problems:\n- %s", len(probs), strings.Join(probs, "\n- ")))
}

// EvalRun is one execution of a suite.
type EvalRun struct {
	ID      string    `json:"id"`
	SuiteID string    `json:"suite_id"`
	Owner   string    `json:"owner,omitempty"`
	Started time.Time `json:"started"`
	// Finished is zero while the run is in flight, which is also how a run
	// interrupted by a restart is told apart from one that completed with
	// nothing — the same reason PipelineRun carries Running.
	Finished time.Time    `json:"finished,omitempty"`
	Results  []EvalResult `json:"results,omitempty"`
	Passed   int          `json:"passed"`
	Total    int          `json:"total"`

	// TargetHash identifies the version graded.
	//
	// The field that makes a history worth keeping. Without it a score history
	// is numbers drifting for unreadable reasons; with it, two runs sharing a
	// hash are the same thing measured twice and their difference is noise,
	// while two that differ are a change and their difference is the answer
	// somebody wanted.
	TargetHash string `json:"target_hash,omitempty"`
	Note       string `json:"note,omitempty"`
	Err        string `json:"error,omitempty"`
}

// Rate renders the headline "24/30". Kept here so the panel, the API and any
// future chart all read the same string.
func (r EvalRun) Rate() string { return fmt.Sprintf("%d/%d", r.Passed, r.Total) }

// EvalRunKey is the storage key. Exported because the FORMAT is the contract
// that makes a suite's history a prefix scan.
func EvalRunKey(suiteID, runID string) string { return suiteID + ":" + runID }

// EvalTargetFingerprint hashes the fields of a target that change behaviour.
//
// Behaviour only, and deliberately not the whole record: a rename, a
// description edit or a new timestamp must not read as a change, or every
// score comparison is against a different hash and the history says nothing.
func EvalTargetFingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// --- storage -----------------------------------------------------------------

// SaveEvalSuite writes a suite, allocating an id and stamping times.
func SaveEvalSuite(udb Database, s EvalSuite) (EvalSuite, error) {
	if udb == nil {
		return s, Error("no store")
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	if s.ID == "" {
		s.ID = UUIDv4()
		s.Created = time.Now()
	}
	s.Updated = time.Now()
	udb.Set(EvalSuitesTable, s.ID, s)
	return s, nil
}

// LoadEvalSuite reads one back.
func LoadEvalSuite(udb Database, id string) (EvalSuite, bool) {
	var s EvalSuite
	if udb == nil || id == "" {
		return s, false
	}
	ok := udb.Get(EvalSuitesTable, id, &s)
	return s, ok
}

// ListEvalSuites returns the owner's suites, newest first.
func ListEvalSuites(udb Database) []EvalSuite {
	if udb == nil {
		return nil
	}
	var out []EvalSuite
	for _, k := range udb.Keys(EvalSuitesTable) {
		var s EvalSuite
		if udb.Get(EvalSuitesTable, k, &s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// DeleteEvalSuite drops a suite AND its history.
//
// Together, because a run keyed to a suite that no longer exists is a row
// nothing can render or explain — it has a score, and no way back to what was
// scored.
func DeleteEvalSuite(udb Database, id string) {
	if udb == nil || id == "" {
		return
	}
	udb.Unset(EvalSuitesTable, id)
	for _, k := range udb.Keys(EvalRunsTable) {
		if strings.HasPrefix(k, id+":") {
			udb.Unset(EvalRunsTable, k)
		}
	}
}

// SaveEvalRun writes one execution.
func SaveEvalRun(udb Database, run EvalRun) EvalRun {
	if udb == nil {
		return run
	}
	if run.ID == "" {
		run.ID = UUIDv4()[:12]
	}
	if run.Started.IsZero() {
		run.Started = time.Now()
	}
	udb.Set(EvalRunsTable, EvalRunKey(run.SuiteID, run.ID), run)
	return run
}

// LoadEvalRun reads one back.
func LoadEvalRun(udb Database, suiteID, runID string) (EvalRun, bool) {
	var run EvalRun
	if udb == nil || suiteID == "" || runID == "" {
		return run, false
	}
	ok := udb.Get(EvalRunsTable, EvalRunKey(suiteID, runID), &run)
	return run, ok
}

// ListEvalRuns returns a suite's history, newest first — which is the score
// history the whole primitive exists to produce.
func ListEvalRuns(udb Database, suiteID string) []EvalRun {
	if udb == nil || suiteID == "" {
		return nil
	}
	var out []EvalRun
	for _, k := range udb.Keys(EvalRunsTable) {
		if !strings.HasPrefix(k, suiteID+":") {
			continue
		}
		var run EvalRun
		if udb.Get(EvalRunsTable, k, &run) {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// DeleteEvalRun drops one result.
func DeleteEvalRun(udb Database, suiteID, runID string) {
	if udb != nil && suiteID != "" && runID != "" {
		udb.Unset(EvalRunsTable, EvalRunKey(suiteID, runID))
	}
}

// --- running a suite ---------------------------------------------------------

// EvalTargetMissing is what a suite reports when the thing it grades is gone.
//
// Its own error because it is a different situation from a failing target and
// must not read as one: a suite whose agent was deleted has not scored 0/30, it
// has scored nothing, and recording a zero would put a fabricated cliff in the
// score history at the moment somebody renamed something.
type EvalTargetMissing struct {
	Kind EvalTargetKind
	ID   string
}

func (e EvalTargetMissing) Error() string {
	return fmt.Sprintf("this suite grades the %s %q, which no longer exists — point it at another one or delete the suite", e.Kind, e.ID)
}

// evalTarget is a resolved thing to grade: how to run one case against it, and
// what fingerprints the version being graded.
type evalTarget struct {
	Fingerprint string
	Exec        evalExecutor
}

// RunEvalSuite grades a suite and RECORDS the result.
//
// The recording is the point. Running cases was already possible; what was not
// is asking afterwards whether the last edit helped, which needs a before.
func (T *OrchestrateApp) RunEvalSuite(ctx context.Context, udb Database, user string, suite EvalSuite, note string) (EvalRun, error) {
	run := EvalRun{
		SuiteID: suite.ID,
		Owner:   suite.Owner,
		Started: time.Now(),
		Total:   len(suite.Cases),
		Note:    note,
	}
	target, err := T.resolveEvalTarget(udb, user, suite)
	if err != nil {
		run.Err = err.Error()
		run.Finished = time.Now()
		// Recorded even so: a suite pointing at something deleted is a fact
		// worth keeping, and a run that vanishes leaves somebody wondering
		// whether they clicked the button.
		return SaveEvalRun(udb, run), err
	}
	run.TargetHash = target.Fingerprint
	// Persisted before the work starts so an in-flight run is visible and a
	// process that dies mid-suite leaves a record with no Finished rather than
	// no trace — the same reason PipelineRun is written up front.
	run = SaveEvalRun(udb, run)

	run.Results = runEvalCases(ctx, suite.Cases, suite.RunCount(), target.Exec)
	for _, r := range run.Results {
		if r.Passed {
			run.Passed++
		}
	}
	run.Finished = time.Now()
	return SaveEvalRun(udb, run), nil
}

// resolveEvalTarget turns a suite's {kind, id} into something runnable.
func (T *OrchestrateApp) resolveEvalTarget(udb Database, user string, suite EvalSuite) (evalTarget, error) {
	switch suite.TargetKind {
	case EvalTargetAgent:
		agent, ok := findAgentByNameOrID(udb, user, suite.TargetID)
		if !ok {
			return evalTarget{}, EvalTargetMissing{Kind: suite.TargetKind, ID: suite.TargetID}
		}
		return evalTarget{
			Fingerprint: agentFingerprint(agent),
			// allowConsequential is the inverse of stubbing: with tools
			// scripted there is nothing consequential to allow, and with them
			// live the suite has already said so deliberately.
			Exec: T.agentEvalExecutor(udb, agent, suite.Stubbed(), !suite.Stubbed()),
		}, nil
	case EvalTargetPipeline:
		def, ok := T.LookupAppPipeline(user, suite.TargetID)
		if !ok {
			return evalTarget{}, EvalTargetMissing{Kind: suite.TargetKind, ID: suite.TargetID}
		}
		return evalTarget{
			Fingerprint: pipelineFingerprint(def),
			Exec:        T.pipelineEvalExecutor(context.Background(), udb, user, def),
		}, nil
	default:
		// Refused rather than silently scoring nothing. The kinds arrive in
		// their own steps; until then a suite for one is an authoring mistake
		// with a clear message, not a run that reports 0/30.
		return evalTarget{}, Error(fmt.Sprintf("grading a %s is not wired up yet — this suite cannot run", suite.TargetKind))
	}
}

// pipelineEvalExecutor grades a pipeline: the case prompt is the run's input,
// the final stage's text is the output, and MustFields asserts on the declared
// fields behind it.
//
// Structured assertions are the reason grading a pipeline is worth doing
// separately at all. A debate's verdict is three paragraphs; "wins" appearing
// somewhere in them is not the same claim as winner == "for", and only one of
// those two is a test.
func (T *OrchestrateApp) pipelineEvalExecutor(ctx context.Context, udb Database, user string, def PipelineDef) evalExecutor {
	hooks := PipelineHooks{
		Dispatch: func(ctx context.Context, agentID, stageInput string) (string, error) {
			if _, found := findAgentByNameOrID(udb, user, agentID); !found {
				return "", ErrNoSuchAgent
			}
			return T.RunAgentSync(ctx, user, user, agentID, stageInput)
		},
		Tools: T.pipelineStandaloneTools(ctx, user, def),
	}
	return func(ctx context.Context, c EvalCase) EvalResult {
		out, fields, err := T.RunPipelineDefHooks(ctx, def, c.Prompt, hooks)
		row := EvalResult{Name: c.Name, Output: truncateForEval(out, 2000)}
		if err != nil {
			row.ErrText = err.Error()
			row.Reasons = append(row.Reasons, "the pipeline errored: "+err.Error())
			return row
		}
		textReasons, textPass := gradeEvalText(c, out)
		fieldReasons, fieldPass := gradeEvalFields(c, fields)
		row.Reasons = append(textReasons, fieldReasons...)
		row.Passed = textPass && fieldPass
		return row
	}
}

// gradeEvalFields checks MustFields against a stage's declared output.
//
// A field the pipeline does not declare is a FAILURE rather than a skip: the
// case asserts something about a shape, and a shape that changed out from under
// it is exactly the regression evals exist to catch. Silently passing would
// make a renamed field read as continued success.
func gradeEvalFields(c EvalCase, fields map[string]any) (reasons []string, pass bool) {
	pass = true
	// Sorted, so a failing case reads the same way twice — map order would
	// shuffle the reasons between two runs of an identical failure.
	names := make([]string, 0, len(c.MustFields))
	for name := range c.MustFields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := c.MustFields[name]
		got, present := fields[name]
		if !present {
			reasons = append(reasons, fmt.Sprintf("no field %q in the final stage's output (it declares: %s)", name, declaredFieldNames(fields)))
			pass = false
			continue
		}
		gotStr := strings.TrimSpace(fmt.Sprint(got))
		if !strings.EqualFold(gotStr, strings.TrimSpace(want)) {
			reasons = append(reasons, fmt.Sprintf("%s = %q, want %q", name, gotStr, want))
			pass = false
			continue
		}
		reasons = append(reasons, fmt.Sprintf("ok: %s = %q", name, gotStr))
	}
	return reasons, pass
}

func declaredFieldNames(fields map[string]any) string {
	if len(fields) == 0 {
		return "none — the final stage has no output contract"
	}
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// pipelineFingerprint hashes what makes a pipeline BEHAVE as it does: each
// stage's name, kind, prompt, tier and declared output shape.
//
// Not the pipeline's name or description, for the same reason an agent's are
// left out: a rename that read as a change would put every run under a fresh
// hash and the history would compare nothing to nothing.
func pipelineFingerprint(def PipelineDef) string {
	parts := make([]string, 0, len(def.Stages)*2)
	for _, st := range def.Stages {
		fields := make([]string, 0, len(st.Output))
		for _, f := range st.Output {
			fields = append(fields, f.Name+":"+string(f.Type))
		}
		parts = append(parts, strings.Join([]string{
			st.Name, string(st.Kind), st.Prompt, st.Model, strings.Join(fields, ","),
		}, "|"))
	}
	return EvalTargetFingerprint(parts...)
}
