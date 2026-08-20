// What a pipeline will DO, worked out without running it.
//
// A machine has a rehearsal because its interesting property is where a turn
// GOES, and that could not be read off the definition. A pipeline's stages run
// in order, so the traversal is not the mystery — the two mysteries are what
// each stage will be handed, and what the whole thing is about to cost.
//
// Both are answerable from the definition alone, instantly and for nothing,
// which is the only reason this is worth having: a preview that costs a model
// call per stage is just a run.
//
// THE FIRST QUESTION IS THE SHARP ONE. Nothing auto-supplies a stage with what
// came before it — resolveStageTemplate substitutes the placeholders a prompt
// CONTAINS, and a prompt containing none sees its own text and nothing else.
// That reads as a stage quietly ignoring the work in front of it, and the
// symptom at run time is a fluent, generic answer rather than an error.
//
// THE SECOND MULTIPLIES. A loop of three stages, twice, with a fanout inside
// it, is not "five stages" — and the arithmetic is exactly the kind nobody does
// in their head while authoring.

package core

import (
	"fmt"
	"strconv"
	"strings"
)

// PipelinePlanStep is one stage as the plan describes it.
type PipelinePlanStep struct {
	Depth int               // nesting — a loop or fanout body is one deeper
	Name  string            //
	Kind  PipelineStageKind //
	RunBy string            // what actually runs it, in words
	Reads []string          // what feeds it; empty means it sees only its prompt
	Min   int               // model calls, at least
	Max   int               // model calls, at most
	Note  string            // anything else worth one line
}

// PipelinePlan is the whole walk plus its arithmetic.
type PipelinePlan struct {
	Steps   []PipelinePlanStep
	Min     int
	Max     int
	AtLeast bool // a stage whose cost cannot be bounded from the definition
}

// Plan walks the definition and describes what a run would do.
func (d PipelineDef) Plan() PipelinePlan {
	var p PipelinePlan
	p.walk(d.Stages, 0)
	for _, s := range p.Steps {
		p.Min += s.Min
		p.Max += s.Max
	}
	return p
}

func (p *PipelinePlan) walk(stages []PipelineStage, depth int) {
	for _, s := range stages {
		step := PipelinePlanStep{Depth: depth, Name: s.Name, Kind: s.Kind, Reads: stageReads(s)}
		bodyMin, bodyMax := bodyCost(s.Body)
		switch s.Kind {
		case StageBranch:
			step.RunBy = "no model — reads " + orPlaceholder(s.When, "a condition") + " and decides"
			step.Note = branchNote(s)
		case StageTool:
			step.RunBy = "no model — calls " + orPlaceholder(s.Tool, "a tool") + " directly"
		case StageAgent:
			step.RunBy = "agent " + orPlaceholder(s.Agent, "(unnamed)")
			step.Min, step.Max = 1, 1
			step.Note = "an agent turn is at least one call, and its own tools may add more"
		case StageMachine:
			step.RunBy = "machine " + orPlaceholder(s.Machine, "(unnamed)")
			step.Min, step.Max = 1, 1
			step.Note = "a machine runs its own steps; the cost is whatever it does"
		case StagePanel:
			rounds := boundedRounds(s.Count)
			voices := len(s.Panel)
			step.RunBy = strconv.Itoa(voices) + " voices × " + strconv.Itoa(rounds) + " round" + plural(rounds)
			step.Min, step.Max = voices*rounds, voices*rounds
		case StageFanout:
			// The item count comes from an earlier stage's output, so it is
			// unknowable here. The CAP is knowable, and it is the number
			// somebody sizing a bill needs.
			per, perMax := 1, 1
			if len(s.Body) > 0 {
				per, perMax = bodyMin, bodyMax
			}
			step.RunBy = "once per item of " + orPlaceholder(s.FanOver, "an earlier list") +
				", in parallel, up to " + strconv.Itoa(fanoutMaxItems)
			step.Min, step.Max = per, perMax*fanoutMaxItems
			step.Note = "the item count is not known until " + orPlaceholder(s.FanOver, "that stage") + " runs"
		case StageLoop:
			passes := s.Count
			if passes < 1 {
				passes = 1
			}
			step.RunBy = "its body, up to " + strconv.Itoa(passes) + " time" + plural(passes)
			step.Min, step.Max = bodyMin, bodyMax*passes
			if strings.TrimSpace(s.Until) != "" {
				step.Note = "stops early when " + s.Until + " says so, so one pass is the floor"
			}
		default:
			step.RunBy = "one model call, " + tierWord(s) + thinkWord(s)
			step.Min, step.Max = 1, 1
		}
		p.Steps = append(p.Steps, step)
		if len(s.Body) > 0 {
			// Recorded for the reader, NOT re-counted: the parent's own
			// figures already multiplied them.
			var body PipelinePlan
			body.walk(s.Body, depth+1)
			for _, b := range body.Steps {
				b.Min, b.Max = 0, 0
				p.Steps = append(p.Steps, b)
			}
		}
		if s.Kind == StageAgent || s.Kind == StageMachine {
			p.AtLeast = true
		}
	}
}

// bodyCost is what one pass through a body costs.
func bodyCost(body []PipelineStage) (int, int) {
	if len(body) == 0 {
		return 0, 0
	}
	var p PipelinePlan
	p.walk(body, 0)
	for _, s := range p.Steps {
		p.Min += s.Min
		p.Max += s.Max
	}
	return p.Min, p.Max
}

// stageReads names what feeds a stage, read off the placeholders its prompt
// actually contains — which is all the runtime substitutes.
func stageReads(s PipelineStage) []string {
	if s.Kind == StageTool || s.Kind == StageBranch {
		return nil // no prompt to read from; they take args or a field
	}
	var out []string
	p := s.Prompt
	if strings.Contains(p, "{input}") {
		out = append(out, "the run's input")
	}
	if strings.Contains(p, "{prev}") {
		out = append(out, "the previous stage")
	}
	if strings.Contains(p, "{item}") {
		out = append(out, "the current item")
	}
	if strings.Contains(p, "{panel}") {
		out = append(out, "what the other voices said")
	}
	for _, ref := range stageRefsIn(p) {
		out = append(out, "stage "+ref)
	}
	return out
}

// stageRefsIn pulls every {stage:NAME} / {stage:NAME.field} out of a prompt.
func stageRefsIn(p string) []string {
	var out []string
	rest := p
	for {
		i := strings.Index(rest, "{stage:")
		if i < 0 {
			return out
		}
		rest = rest[i+len("{stage:"):]
		j := strings.Index(rest, "}")
		if j < 0 {
			return out
		}
		if ref := strings.TrimSpace(rest[:j]); ref != "" {
			out = append(out, ref)
		}
		rest = rest[j+1:]
	}
}

func branchNote(s PipelineStage) string {
	if to := strings.TrimSpace(s.SkipTo); to != "" {
		return "skips ahead to " + to + " when it holds"
	}
	return "ENDS the run when it holds — nothing after this stage happens"
}

func boundedRounds(n int) int {
	switch {
	case n < 1:
		return 1
	case n > panelMaxRounds:
		return panelMaxRounds
	}
	return n
}

func tierWord(s PipelineStage) string {
	if strings.EqualFold(strings.TrimSpace(s.Model), "lead") {
		return "on the lead tier"
	}
	return "on the worker"
}

func thinkWord(s PipelineStage) string {
	switch StageThinkMode(s) {
	case "on":
		return ", thinking"
	case "off":
		return ", not thinking"
	}
	return ""
}

func orPlaceholder(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Summary is the arithmetic in one sentence, for a surface that wants the
// headline without the walk.
func (p PipelinePlan) Summary() string {
	if len(p.Steps) == 0 {
		return "No stages yet."
	}
	calls := strconv.Itoa(p.Min)
	if p.Max != p.Min {
		calls = fmt.Sprintf("%d–%d", p.Min, p.Max)
	}
	at := ""
	if p.AtLeast {
		at = " at least"
	}
	return fmt.Sprintf("%d stage%s,%s %s model call%s per run.",
		len(p.Steps), plural(len(p.Steps)), at, calls, plural(p.Max))
}
