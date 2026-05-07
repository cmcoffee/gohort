package servitor

import (
	"fmt"
	"sync"
)

// PlanStepStatus is the lifecycle state of a single investigation step.
type PlanStepStatus string

const (
	PlanStepPending    PlanStepStatus = "pending"
	PlanStepInProgress PlanStepStatus = "in_progress"
	PlanStepDone       PlanStepStatus = "done"
	PlanStepBlocked    PlanStepStatus = "blocked"
)

// PlanStep is one item in the investigator's plan. The model emits the
// title + what_to_find when the plan is set; status, findings, and
// blocked_reason are filled in over the course of execution.
type PlanStep struct {
	ID            int            `json:"id"`
	Title         string         `json:"title"`
	WhatToFind    string         `json:"what_to_find"`
	Status        PlanStepStatus `json:"status"`
	Findings      string         `json:"findings,omitempty"`
	BlockedReason string         `json:"blocked_reason,omitempty"`
}

// Plan is the ordered set of investigation steps for one session. Stored
// in process memory (sync.Map keyed by session id), not persisted —
// plans are session-scoped and rebuilt at the start of each
// investigation. If durable plan storage becomes useful later, swap the
// session map for a kvlite table.
type Plan struct {
	mu             sync.Mutex
	Steps          []PlanStep
	nextID         int  // next ID to assign for newly-added steps (revise_plan additions)
	revisionCount  int  // number of revise_plan calls so far this session
	gapsReported   bool // set when report_gaps is called; gates synthesis
}

// PlanRevisionLimit caps how many revise_plan calls are allowed per
// session. Prevents endless re-planning loops where the model
// reshuffles instead of executing.
const PlanRevisionLimit = 3

// SetSteps replaces the plan with a fresh list of steps, all marked
// pending. IDs are assigned in order starting at 1 so they're stable
// references for status updates from the LLM.
func (p *Plan) SetSteps(titles, whatsToFind []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(titles) == 0 {
		return fmt.Errorf("plan must have at least one step")
	}
	if len(whatsToFind) != len(titles) {
		return fmt.Errorf("titles and what_to_find must be the same length: got %d titles, %d details", len(titles), len(whatsToFind))
	}
	p.Steps = make([]PlanStep, len(titles))
	for i := range titles {
		p.Steps[i] = PlanStep{
			ID:         i + 1,
			Title:      titles[i],
			WhatToFind: whatsToFind[i],
			Status:     PlanStepPending,
		}
	}
	p.nextID = len(titles) + 1
	return nil
}

// AddSteps appends new steps with fresh IDs. Used by revise_plan when
// findings reveal investigation areas that weren't anticipated.
func (p *Plan) AddSteps(titles, whatsToFind []string) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(titles) != len(whatsToFind) {
		return nil, fmt.Errorf("titles and what_to_find must be the same length")
	}
	if p.nextID < 1 {
		p.nextID = len(p.Steps) + 1
	}
	added := make([]int, 0, len(titles))
	for i := range titles {
		id := p.nextID
		p.nextID++
		p.Steps = append(p.Steps, PlanStep{
			ID:         id,
			Title:      titles[i],
			WhatToFind: whatsToFind[i],
			Status:     PlanStepPending,
		})
		added = append(added, id)
	}
	return added, nil
}

// RemoveSteps drops steps by ID. Only steps in pending state can be
// removed — done, in_progress, and blocked steps are durable history
// and must remain in the plan for the gap report. Returns the IDs that
// were actually removed.
func (p *Plan) RemoveSteps(ids []int) ([]int, []int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idSet := make(map[int]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	var removed, refused []int
	out := p.Steps[:0]
	for _, s := range p.Steps {
		if !idSet[s.ID] {
			out = append(out, s)
			continue
		}
		if s.Status != PlanStepPending {
			out = append(out, s)
			refused = append(refused, s.ID)
			continue
		}
		removed = append(removed, s.ID)
	}
	p.Steps = out
	return removed, refused, nil
}

// ReorderSteps takes a new ordering of step IDs. Must be a permutation
// of all current step IDs (no missing, no extra). Returns an error if
// the requested ordering is invalid.
func (p *Plan) ReorderSteps(order []int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(order) != len(p.Steps) {
		return fmt.Errorf("reorder must list all %d step IDs; got %d", len(p.Steps), len(order))
	}
	byID := make(map[int]PlanStep, len(p.Steps))
	for _, s := range p.Steps {
		byID[s.ID] = s
	}
	seen := make(map[int]bool, len(order))
	out := make([]PlanStep, 0, len(order))
	for _, id := range order {
		s, ok := byID[id]
		if !ok {
			return fmt.Errorf("reorder includes unknown step id %d", id)
		}
		if seen[id] {
			return fmt.Errorf("reorder lists step id %d more than once", id)
		}
		seen[id] = true
		out = append(out, s)
	}
	p.Steps = out
	return nil
}

// IncrRevision bumps the revision counter and reports whether the
// caller is still under the revision cap.
func (p *Plan) IncrRevision() (count int, atCap bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revisionCount++
	return p.revisionCount, p.revisionCount >= PlanRevisionLimit
}

// RevisionCount returns the current revision count for status checks.
func (p *Plan) RevisionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revisionCount
}

// MarkGapsReported flips the gaps-reported flag, signaling synthesis
// can proceed. Returns the structured gap summary the LLM is expected
// to incorporate into the final report.
func (p *Plan) MarkGapsReported() PlanGapReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gapsReported = true
	report := PlanGapReport{}
	for _, s := range p.Steps {
		switch s.Status {
		case PlanStepBlocked:
			report.Blocked = append(report.Blocked, PlanGapEntry{ID: s.ID, Title: s.Title, WhatToFind: s.WhatToFind, Reason: s.BlockedReason})
		case PlanStepPending, PlanStepInProgress:
			report.Skipped = append(report.Skipped, PlanGapEntry{ID: s.ID, Title: s.Title, WhatToFind: s.WhatToFind, Reason: "step never completed"})
		}
	}
	return report
}

// GapsReported reports whether report_gaps has been called yet.
func (p *Plan) GapsReported() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gapsReported
}

// PlanGapEntry is one step represented in a gap report.
type PlanGapEntry struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	WhatToFind string `json:"what_to_find"`
	Reason     string `json:"reason"`
}

// PlanGapReport is the structured result returned to the LLM when
// report_gaps is called. The LLM is expected to incorporate this into
// the final answer's "What I Couldn't Determine" section.
type PlanGapReport struct {
	Blocked []PlanGapEntry `json:"blocked,omitempty"`
	Skipped []PlanGapEntry `json:"skipped,omitempty"`
}

// SetStatus updates the status of a step by ID. Returns an error if the
// step doesn't exist or the requested transition is invalid (e.g.
// re-setting a done step to pending).
func (p *Plan) SetStatus(id int, status PlanStepStatus) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			p.Steps[i].Status = status
			return nil
		}
	}
	return fmt.Errorf("step %d not found in plan", id)
}

// RecordFindings attaches a findings summary to a step and marks it done.
func (p *Plan) RecordFindings(id int, findings string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			p.Steps[i].Findings = findings
			p.Steps[i].Status = PlanStepDone
			return nil
		}
	}
	return fmt.Errorf("step %d not found in plan", id)
}

// MarkBlocked records why a step couldn't be completed (no access, tool
// missing, permission denied, etc.) and marks it blocked. The reason
// becomes part of the gap report at synthesis time.
func (p *Plan) MarkBlocked(id int, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			p.Steps[i].BlockedReason = reason
			p.Steps[i].Status = PlanStepBlocked
			return nil
		}
	}
	return fmt.Errorf("step %d not found in plan", id)
}

// Snapshot returns a copy of the current step list, safe to render
// without holding the mutex.
func (p *Plan) Snapshot() []PlanStep {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PlanStep, len(p.Steps))
	copy(out, p.Steps)
	return out
}

// IsSet reports whether the plan has been initialized. The investigator
// system prompt requires set_plan as the first tool call; this lets the
// other plan tools refuse to operate before that's happened.
func (p *Plan) IsSet() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Steps) > 0
}
