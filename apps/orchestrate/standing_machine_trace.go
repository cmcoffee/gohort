package orchestrate

import (
	"context"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// machineRunTrace is what a scheduled machine run leaves behind.
//
// The walk narrates itself through two sinks — the phase runner (a step ran,
// here is its result) and the note callback (the framework moved, guarded,
// fell back, capped) — and until this existed the scheduled path collected
// the notes into a slice nobody read and recorded only the final step's text.
// So the one run nobody watches happen, fired at 3am with the tab closed, was
// also the one whose record could not say which steps ran, what each
// produced, or why the walk went where it went. The machine page's Run
// button shows all of that live; a schedule showed a summary.
//
// Everything lands in RunRecord.Steps in the order it happened — a phase as
// "step <name>" with its output, a framework decision as its kind with the
// detail — so the ledger's Details view reads as the run's own transcript.
// The live activity row gets the step count and, for a tool step, the tool.
type machineRunTrace struct {
	mu    sync.Mutex
	steps []RunStep
	seq   int
	live  *Run
}

func newMachineRunTrace(live *Run) *machineRunTrace {
	return &machineRunTrace{live: live}
}

// note records a framework decision. Same signature core's walk and the
// unattended host take, so one func serves both.
func (tr *machineRunTrace) note(kind, detail string) {
	kind, detail = strings.TrimSpace(kind), strings.TrimSpace(detail)
	if kind == "" && detail == "" {
		return
	}
	tr.mu.Lock()
	tr.steps = append(tr.steps, RunStep{Name: kind, Result: detail})
	tr.mu.Unlock()
}

// activity records progress a sub-run (a delegate, a pipeline's stages)
// reports, tagged by its source, as a decision-shaped entry.
func (tr *machineRunTrace) activity(source, text string) {
	tr.note("activity", strings.TrimSpace(source)+": "+strings.TrimSpace(text))
}

// wrap decorates a phase runner so each step lands in the trace with its
// result or failure, and the live run advances one round per step.
func (tr *machineRunTrace) wrap(run PhaseRunner) PhaseRunner {
	return func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		tr.mu.Lock()
		tr.seq++
		seq := tr.seq
		tr.mu.Unlock()
		if tr.live != nil {
			var tools []ToolCall
			if tool := strings.TrimSpace(ph.Tool); tool != "" {
				tools = []ToolCall{{Name: tool}}
			}
			tr.live.SetProgress(seq, tools)
		}
		out, err := run(ctx, ph, prompt)
		st := RunStep{Name: "step " + ph.Name}
		if err != nil {
			st.Err = err.Error()
		} else {
			st.Result = strings.TrimSpace(out)
		}
		tr.mu.Lock()
		tr.steps = append(tr.steps, st)
		tr.mu.Unlock()
		return out, err
	}
}

// Steps returns the trace so far, in order. A copy, so a caller holding it
// across a late activity callback sees a stable slice.
func (tr *machineRunTrace) Steps() []RunStep {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.steps) == 0 {
		return nil
	}
	out := make([]RunStep, len(tr.steps))
	copy(out, tr.steps)
	return out
}

// phases counts the steps that ran, for the log line.
func (tr *machineRunTrace) phases() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.seq
}
