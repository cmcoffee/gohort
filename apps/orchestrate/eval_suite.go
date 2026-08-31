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
