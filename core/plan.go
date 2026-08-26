// A plan an agent commits to and then works: the checklist, its step
// lifecycle, and the gap report that keeps an unfinished step from quietly
// vanishing out of the final answer.
//
// Lifted out of apps/servitor, where it was the most complete of the four
// planners this codebase had grown (the turn-level fan-out, Builder's build
// checklist, machines/pipelines as authored recipes, and this). It is the only
// one that models a step's LIFE — started, found something, blocked and why,
// revised because reality contradicted the plan — which is what "planning and
// execution" actually needs.
//
// State only. It does not know what a step's work is, where the checklist is
// drawn, or how it is stored: a host holds the plan, calls these methods, and
// renders and persists the Snapshot however it renders and persists anything.
// The tool group agents drive it with is WorkPlanTools (plan_tools.go).

package core

import (
	"fmt"
	"sync"
)

// WorkStepStatus is the lifecycle state of a single step.
type WorkStepStatus string

const (
	WorkStepPending    WorkStepStatus = "pending"
	WorkStepInProgress WorkStepStatus = "in_progress"
	WorkStepDone       WorkStepStatus = "done"
	WorkStepBlocked    WorkStepStatus = "blocked"
)

// WorkPlanStep is one item in the plan. The model emits title + what_to_find when
// the plan is set; status, findings and blocked_reason are filled in over the
// course of the work.
//
// The json names are load-bearing: they are the payload the browser's plan
// renderer already reads, so a host that emits a Snapshot gets the live
// checklist card with no rendering code of its own.
type WorkPlanStep struct {
	ID            int            `json:"id"`
	Title         string         `json:"title"`
	WhatToFind    string         `json:"what_to_find"`
	Status        WorkStepStatus `json:"status"`
	Findings      string         `json:"findings,omitempty"`
	BlockedReason string         `json:"blocked_reason,omitempty"`
}

// WorkPlan is the ordered set of steps for one piece of work.
//
// The fields are exported and tagged so a host can persist the whole thing —
// a plan that survives only in process is a plan that dies at the end of the
// turn, which is exactly what stopped the earlier version being usable outside
// one session. Always held as a *WorkPlan: the mutex makes a copy a bug.
type WorkPlan struct {
	mu        sync.Mutex
	Steps     []WorkPlanStep `json:"steps,omitempty"`
	NextID    int            `json:"next_id,omitempty"`   // next ID to assign for added steps
	Revisions int            `json:"revisions,omitempty"` // revise calls so far
	GapsDone  bool           `json:"gaps_done,omitempty"` // report_gaps has been called
}

// WorkPlanRevisionLimit caps how many revisions are allowed. Prevents the loop
// where the model reshuffles the plan instead of executing it.
const WorkPlanRevisionLimit = 3

// SetSteps replaces the plan with a fresh list of steps, all pending. IDs are
// assigned in order from 1 so they are stable references for status updates.
func (p *WorkPlan) SetSteps(titles, whatsToFind []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(titles) == 0 {
		return fmt.Errorf("plan must have at least one step")
	}
	if len(whatsToFind) != len(titles) {
		return fmt.Errorf("titles and what_to_find must be the same length: got %d titles, %d details", len(titles), len(whatsToFind))
	}
	p.Steps = make([]WorkPlanStep, len(titles))
	for i := range titles {
		p.Steps[i] = WorkPlanStep{
			ID:         i + 1,
			Title:      titles[i],
			WhatToFind: whatsToFind[i],
			Status:     WorkStepPending,
		}
	}
	p.NextID = len(titles) + 1
	return nil
}

// AddSteps appends new steps with fresh IDs — the revise path, for work the
// findings revealed and the plan could not have anticipated.
func (p *WorkPlan) AddSteps(titles, whatsToFind []string) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(titles) != len(whatsToFind) {
		return nil, fmt.Errorf("titles and what_to_find must be the same length")
	}
	if p.NextID < 1 {
		p.NextID = len(p.Steps) + 1
	}
	added := make([]int, 0, len(titles))
	for i := range titles {
		id := p.NextID
		p.NextID++
		p.Steps = append(p.Steps, WorkPlanStep{
			ID:         id,
			Title:      titles[i],
			WhatToFind: whatsToFind[i],
			Status:     WorkStepPending,
		})
		added = append(added, id)
	}
	return added, nil
}

// RemoveSteps drops steps by ID. Only PENDING steps can go: done, in_progress
// and blocked steps are durable history and have to stay in the plan for the
// gap report. Returns what was removed and what was refused.
func (p *WorkPlan) RemoveSteps(ids []int) (removed, refused []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idSet := make(map[int]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	out := p.Steps[:0]
	for _, s := range p.Steps {
		if !idSet[s.ID] {
			out = append(out, s)
			continue
		}
		if s.Status != WorkStepPending {
			out = append(out, s)
			refused = append(refused, s.ID)
			continue
		}
		removed = append(removed, s.ID)
	}
	p.Steps = out
	return removed, refused
}

// ReorderSteps takes a new ordering of step IDs. Must be a permutation of the
// current IDs — no missing, no extra, no repeats.
func (p *WorkPlan) ReorderSteps(order []int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(order) != len(p.Steps) {
		return fmt.Errorf("reorder must list all %d step IDs; got %d", len(p.Steps), len(order))
	}
	byID := make(map[int]WorkPlanStep, len(p.Steps))
	for _, s := range p.Steps {
		byID[s.ID] = s
	}
	seen := make(map[int]bool, len(order))
	out := make([]WorkPlanStep, 0, len(order))
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

// IncrRevision bumps the revision counter and reports whether the caller has
// reached the cap.
func (p *WorkPlan) IncrRevision() (count int, atCap bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Revisions++
	return p.Revisions, p.Revisions >= WorkPlanRevisionLimit
}

// RevisionCount returns the revisions used so far.
func (p *WorkPlan) RevisionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Revisions
}

// SetStatus updates one step's status by ID.
func (p *WorkPlan) SetStatus(id int, status WorkStepStatus) error {
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
func (p *WorkPlan) RecordFindings(id int, findings string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			p.Steps[i].Findings = findings
			p.Steps[i].Status = WorkStepDone
			return nil
		}
	}
	return fmt.Errorf("step %d not found in plan", id)
}

// MarkBlocked records why a step could not be completed and marks it blocked.
// The reason becomes part of the gap report.
func (p *WorkPlan) MarkBlocked(id int, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			p.Steps[i].BlockedReason = reason
			p.Steps[i].Status = WorkStepBlocked
			return nil
		}
	}
	return fmt.Errorf("step %d not found in plan", id)
}

// WorkPlanGapEntry is one step represented in a gap report.
type WorkPlanGapEntry struct {
	ID         int    `json:"id"`
	Title      string `json:"title"`
	WhatToFind string `json:"what_to_find"`
	Reason     string `json:"reason"`
}

// WorkPlanGapReport is what the model is handed when it asks what it did not
// manage — the material for the answer's "what I could not determine" half.
type WorkPlanGapReport struct {
	Blocked []WorkPlanGapEntry `json:"blocked,omitempty"`
	Skipped []WorkPlanGapEntry `json:"skipped,omitempty"`
}

// MarkGapsReported flips the gaps-reported flag and returns the report.
func (p *WorkPlan) MarkGapsReported() WorkPlanGapReport {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.GapsDone = true
	report := WorkPlanGapReport{}
	for _, s := range p.Steps {
		switch s.Status {
		case WorkStepBlocked:
			report.Blocked = append(report.Blocked, WorkPlanGapEntry{ID: s.ID, Title: s.Title, WhatToFind: s.WhatToFind, Reason: s.BlockedReason})
		case WorkStepPending, WorkStepInProgress:
			report.Skipped = append(report.Skipped, WorkPlanGapEntry{ID: s.ID, Title: s.Title, WhatToFind: s.WhatToFind, Reason: "step never completed"})
		}
	}
	return report
}

// GapsReported reports whether the gap report has been taken yet.
func (p *WorkPlan) GapsReported() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.GapsDone
}

// Snapshot returns a copy of the step list, safe to render or persist without
// holding the mutex.
func (p *WorkPlan) Snapshot() []WorkPlanStep {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]WorkPlanStep, len(p.Steps))
	copy(out, p.Steps)
	return out
}

// IsSet reports whether the plan has been committed to yet. The other tools
// refuse to operate before that.
func (p *WorkPlan) IsSet() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Steps) > 0
}

// Pending counts steps still to do — the signal for "there is work left", which
// a host reads to decide whether a capped run was unfinished rather than stuck.
func (p *WorkPlan) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, s := range p.Steps {
		if s.Status == WorkStepPending || s.Status == WorkStepInProgress {
			n++
		}
	}
	return n
}

// StatusOf reports one step's status, or an empty status when the id is not in
// the plan. Read-only, so a caller can tell whether a change would be a no-op
// before making it — the difference between "marked in progress" and "was
// already in progress", which is the difference between reporting progress and
// reporting nothing.
func (p *WorkPlan) StatusOf(id int) WorkStepStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return p.Steps[i].Status
		}
	}
	return ""
}
