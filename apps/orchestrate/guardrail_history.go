package orchestrate

// An agent's guardrails keep a history of their own.
//
// The guardrails editor saves on every change: a severity toggled, a rule
// typed, a hook ticked. Each of those saves wrote the whole agent record, and
// each filed the version it replaced in the agent's ring, which is six deep.
// A few minutes of tuning guardrails pushed every earlier version out, the
// persona and rules an owner wanted to go back to among them, and with nothing
// else holding them they were gone.
//
// So a guardrail save files the guardrail settings it replaced in a ring of
// their own (revisions.KindAgentGuardrails) and none in the agent's. Each side
// is undone on its own: going back to an earlier persona keeps today's
// guardrails, and going back to earlier guardrails leaves the persona alone.

import (
	"fmt"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
)

// guardrailSet is everything the guardrail endpoints write on an agent.
type guardrailSet struct {
	Guardrails          string               `json:"guardrails,omitempty"`
	Hooks               []string             `json:"guardrail_hooks,omitempty"`
	FailClosed          bool                 `json:"guardrail_fail_closed,omitempty"`
	Depth               string               `json:"guardrail_depth,omitempty"`
	Declines            []string             `json:"guardrail_declines,omitempty"`
	Disabled            bool                 `json:"guardrails_disabled,omitempty"`
	Authorized          []string             `json:"authorized_identities,omitempty"`
	Exceptions          []GuardrailException `json:"guardrail_exceptions,omitempty"`
	ScanToolResults     bool                 `json:"scan_tool_results,omitempty"`
	ScanToolsAdd        []string             `json:"scan_tools_add,omitempty"`
	ScanToolsSkip       []string             `json:"scan_tools_skip,omitempty"`
	ScanAction          string               `json:"scan_action,omitempty"`
	ScanBlockTools      []string             `json:"scan_block_tools,omitempty"`
	ScanAppealable      bool                 `json:"scan_appealable,omitempty"`
	ScanTightenDisabled bool                 `json:"scan_tighten_disabled,omitempty"`
	ScanTrustedSources  []string             `json:"scan_trusted_sources,omitempty"`
}

// guardrailSetOf is a's guardrail settings.
func guardrailSetOf(a AgentRecord) guardrailSet {
	return guardrailSet{
		Guardrails: a.Guardrails, Hooks: a.GuardrailHooks, FailClosed: a.GuardrailFailClosed,
		Depth: a.GuardrailDepth, Declines: a.GuardrailDeclines, Disabled: a.GuardrailsDisabled,
		Authorized: a.AuthorizedIdentities, Exceptions: a.GuardrailExceptions,
		ScanToolResults: a.ScanToolResults, ScanToolsAdd: a.ScanToolsAdd, ScanToolsSkip: a.ScanToolsSkip,
		ScanAction: a.ScanAction, ScanBlockTools: a.ScanBlockTools, ScanAppealable: a.ScanAppealable,
		ScanTightenDisabled: a.ScanTightenDisabled, ScanTrustedSources: a.ScanTrustedSources,
	}
}

// applyTo puts s on a, leaving everything else on it as it is.
func (s guardrailSet) applyTo(a *AgentRecord) {
	a.Guardrails, a.GuardrailHooks, a.GuardrailFailClosed = s.Guardrails, s.Hooks, s.FailClosed
	a.GuardrailDepth, a.GuardrailDeclines, a.GuardrailsDisabled = s.Depth, s.Declines, s.Disabled
	a.AuthorizedIdentities, a.GuardrailExceptions = s.Authorized, s.Exceptions
	a.ScanToolResults, a.ScanToolsAdd, a.ScanToolsSkip = s.ScanToolResults, s.ScanToolsAdd, s.ScanToolsSkip
	a.ScanAction, a.ScanBlockTools, a.ScanAppealable = s.ScanAction, s.ScanBlockTools, s.ScanAppealable
	a.ScanTightenDisabled, a.ScanTrustedSources = s.ScanTightenDisabled, s.ScanTrustedSources
}

// saveAgentGuardrails is a guardrail endpoint's save: the settings it
// replaced go in the guardrail ring, and nothing in the agent's.
func saveAgentGuardrails(db Database, a AgentRecord, reason string) (AgentRecord, error) {
	var prior AgentRecord
	if db.Get(agentsTable, a.ID, &prior) {
		was, now := guardrailSetOf(prior), guardrailSetOf(a)
		if revisions.Differs(was, now) {
			revisions.Push(db, revisions.KindAgentGuardrails, a.ID, was, prior.Updated, reason)
		}
	}
	return saveAgentAs(db, a, revisions.NoHistory)
}

// rollbackAgentGuardrails puts back a kept version of an agent's guardrail
// settings, filing the ones it replaces, so a rollback is undone the same way.
func rollbackAgentGuardrails(db Database, id, ref string) (AgentRecord, error) {
	rev, ok := revisions.Find(db, revisions.KindAgentGuardrails, id, ref)
	if !ok {
		return AgentRecord{}, fmt.Errorf("no kept version %q of this agent's guardrails", ref)
	}
	var kept guardrailSet
	if !revisions.Load(db, revisions.KindAgentGuardrails, id, fmt.Sprint(rev.Seq), &kept) {
		return AgentRecord{}, fmt.Errorf("kept guardrails #%d could not be read", rev.Seq)
	}
	a, ok := loadAgent(db, id)
	if !ok {
		return AgentRecord{}, fmt.Errorf("no agent %q", id)
	}
	kept.applyTo(&a)
	return saveAgentGuardrails(db, a, fmt.Sprintf("rolled back to #%d", rev.Seq))
}

// handleAgentGuardrailRevisions serves
// /api/agents/{id}/guardrails/revisions[/preview|/restore], owner only, as
// the agent's own history is.
func (T *OrchestrateApp) handleAgentGuardrailRevisions(w http.ResponseWriter, r *http.Request, user, id, action string) {
	udb := UserDB(T.DB, user)
	a, ok := loadAgent(udb, id)
	if !ok || (a.Owner != "" && a.Owner != user) {
		http.NotFound(w, r)
		return
	}
	revisions.Surface{
		Store:   udb,
		Kind:    revisions.KindAgentGuardrails,
		Key:     id,
		Noun:    "agent's guardrails",
		Current: guardrailSetOf(a),
		Restore: func(ref string) error {
			_, err := rollbackAgentGuardrails(udb, id, ref)
			return err
		},
	}.Serve(w, r, action)
}
