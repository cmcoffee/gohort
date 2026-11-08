// A run's live trace: what it has done so far, step by step, while it is
// still going. The Monitor's Trace view reads it.
//
// The run ledger keeps a finished run's full trace (RunRecord.Steps), but it
// is written when the run ENDS. Until then the live row said only the latest
// tool's name, which cannot tell a background run that is working from one
// that is stuck, and a scheduled fire showed nothing at all of what it was
// doing. This is that gap and no more: in memory, bounded, gone with the run
// (runCleanupAge after it ends), shown only to the run's owner.
//
// It is fed by the agent loop through core.ToolTrace, which rides on the
// context: withRun puts the run there, at each place a run is started.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/jsonapi"

	. "github.com/cmcoffee/gohort/core"
)

// runTraceMax is how many steps a run keeps. The oldest go first; the count
// of those dropped is kept, so the view can say the list starts part way in.
const runTraceMax = 60

// runTraceText is how much of a call's arguments, its result's first line,
// or the model's words a step keeps. A step is a line to scan, not a record:
// the full text is the ledger's, once the run ends.
const runTraceText = 200

// Kinds of trace step.
const (
	traceSaid = "said" // what the model wrote in a round that called tools
	traceTool = "tool" // one tool call, from its start to its end
	traceNote = "note" // the run's own progress line (SetDetail)
)

// runTraceStep is one line of a run's trace.
type runTraceStep struct {
	Kind   string
	Round  int
	Name   string // the tool, for a tool step
	Text   string // the arguments (tool), the words (said), the line (note)
	Result string // a finished call's first line of output
	Err    string // a failed call's error
	At     time.Time
	Ended  time.Time // zero while a tool call is running
}

// withRun is withParentRun for a run in hand: the run's id on ctx for the
// runs it starts to nest under, and the run itself as the trace the agent
// loop reports its steps to.
func withRun(ctx context.Context, r *Run) context.Context {
	return WithToolTrace(withParentRun(ctx, r.ID), r)
}

// addStepLocked appends s, dropping the oldest past runTraceMax. Caller holds
// r.mu. Steps are kept by pointer, so a call's end finds its own step after
// later ones have been added or older ones dropped.
func (r *Run) addStepLocked(s *runTraceStep) {
	r.trace = append(r.trace, s)
	if over := len(r.trace) - runTraceMax; over > 0 {
		r.trace = append([]*runTraceStep(nil), r.trace[over:]...)
		r.traceDropped += over
	}
}

// RoundStarted records what the model said in a round that calls tools.
// Part of core.ToolTrace.
func (r *Run) RoundStarted(round int, said string) {
	said = traceLine(said)
	r.mu.Lock()
	defer r.mu.Unlock()
	if round > r.round {
		r.round = round
	}
	if said == "" || r.closed {
		return
	}
	r.addStepLocked(&runTraceStep{Kind: traceSaid, Round: round, Text: said, At: time.Now()})
}

// ToolStarted records a call as it starts and returns what records its end.
// Part of core.ToolTrace.
func (r *Run) ToolStarted(name string, args map[string]any) func(string, error) {
	s := &runTraceStep{Kind: traceTool, Name: name, Text: traceArgs(args), At: time.Now()}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	s.Round = r.round
	r.lastTool = name
	r.addStepLocked(s)
	r.mu.Unlock()
	return func(output string, err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		s.Ended = time.Now()
		if err != nil {
			s.Err = traceLine(err.Error())
			return
		}
		s.Result = traceLine(output)
	}
}

// noteLocked records the run's own progress line. A run that reports on a
// tick ("1,200 of 8,400") would fill the trace with one step per tick, so a
// note right after a note replaces it: the trace shows the latest word,
// where the run said it. Caller holds r.mu.
func (r *Run) noteLocked(line string) {
	line = traceLine(line)
	if line == "" || r.closed {
		return
	}
	if n := len(r.trace); n > 0 && r.trace[n-1].Kind == traceNote {
		r.trace[n-1].Text, r.trace[n-1].At = line, time.Now()
		return
	}
	r.addStepLocked(&runTraceStep{Kind: traceNote, Round: r.round, Text: line, At: time.Now()})
}

// Trace returns a copy of the run's steps, oldest first, and how many older
// ones were dropped to keep it bounded.
func (r *Run) Trace() (steps []runTraceStep, dropped int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	steps = make([]runTraceStep, len(r.trace))
	for i, s := range r.trace {
		steps[i] = *s
	}
	return steps, r.traceDropped
}

// traceArgs is a call's arguments as one line: compact JSON, cut short.
func traceArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return traceCut(string(b))
}

// traceLine is the first non-blank line of s, cut short.
func traceLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return traceCut(line)
		}
	}
	return ""
}

// traceCut cuts s to runTraceText runes, marking the cut.
func traceCut(s string) string {
	rs := []rune(s)
	if len(rs) <= runTraceText {
		return s
	}
	return string(rs[:runTraceText]) + "…"
}

// consoleTraceRow is one line of the Monitor's Trace view.
type consoleTraceRow struct {
	Step   string `json:"step"`             // the tool, or what kind of line this is
	State  string `json:"state"`            // running | done | failed | said | note, or the run's own status on its header
	Time   string `json:"time,omitempty"`   // how long a call took, or has been going
	Detail string `json:"detail,omitempty"` // the call's arguments, the words, the note
	Result string `json:"result,omitempty"` // a finished call's first line, or its error
	ID     string `json:"_id"`
}

// handleConsoleTrace serves one run's live trace, and its sub-agents', for
// the Monitor's Trace view: a header line for the run, then its steps newest
// first, a sub-agent's marked with its name. Owner-scoped as the activity
// feed is: another user's run, or one already cleared, reads as nothing.
func (T *OrchestrateApp) handleConsoleTrace(c *jsonapi.Call) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	rows := []consoleTraceRow{}
	if id == "" {
		return rows, nil
	}
	reg := T.runsRegistry()
	snaps := descendantsOf(reg.Activity(c.User), id)
	if len(snaps) == 0 {
		return rows, nil
	}
	return traceRows(reg, snaps, time.Now()), nil
}

// traceRows builds the Trace view from a run (snaps[0]) and its sub-agents.
func traceRows(reg *RunRegistry, snaps []RunSnapshot, now time.Time) []consoleTraceRow {
	root := snaps[0]
	head := consoleTraceRow{Step: firstNonEmpty(root.AgentName, root.AgentID), State: root.Status, Detail: root.Label, ID: root.ID}
	if root.Status == RunStatusRunning {
		head.Time = shortElapsed(now.Sub(root.StartedAt))
	} else {
		head.Time = "took " + shortElapsed(root.EndedAt.Sub(root.StartedAt))
	}
	type step struct {
		runTraceStep
		who string // the sub-agent's name; "" for the run itself
		key string
	}
	var all []step
	dropped := 0
	for _, s := range snaps {
		r := reg.Get(s.ID)
		if r == nil {
			continue
		}
		who := ""
		if s.ID != root.ID {
			who = firstNonEmpty(s.AgentName, s.AgentID)
		}
		steps, d := r.Trace()
		if s.ID == root.ID {
			dropped = d
		}
		for i, st := range steps {
			all = append(all, step{st, who, s.ID + ":" + strconv.Itoa(d+i)})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	rows := []consoleTraceRow{head}
	for _, s := range all {
		row := consoleTraceRow{Detail: s.Text, ID: s.key}
		switch s.Kind {
		case traceTool:
			row.Step = s.Name
			switch {
			case s.Ended.IsZero():
				row.State, row.Time = "running", shortElapsed(now.Sub(s.At))
			case s.Err != "":
				row.State, row.Time, row.Result = "failed", "took "+shortElapsed(s.Ended.Sub(s.At)), s.Err
			default:
				row.State, row.Time, row.Result = "done", "took "+shortElapsed(s.Ended.Sub(s.At)), s.Result
			}
		case traceSaid:
			row.Step, row.State = "said", traceSaid
		default:
			row.Step, row.State = "progress", traceNote
		}
		if s.Round > 0 {
			row.Step = fmt.Sprintf("round %d · %s", s.Round, row.Step)
		}
		if s.who != "" {
			row.Step = "↳ " + s.who + " · " + row.Step
		}
		rows = append(rows, row)
	}
	if dropped > 0 {
		rows = append(rows, consoleTraceRow{Step: fmt.Sprintf("%d earlier steps", dropped), State: traceNote,
			Detail: "Not kept: a run keeps its latest " + strconv.Itoa(runTraceMax) + " steps while it goes. The full list is in its run record once it ends.", ID: root.ID + ":dropped"})
	}
	return rows
}
