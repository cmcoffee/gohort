package orchestrate

// Maintainers: the people who keep somebody else's agent working.
//
// An agent shared with its users (AllowedUsers, Everyone) gives them the run,
// not the record. A support team's agent, say, is used by one tier and kept
// up by another: its instructions, its rules, its behaviour need adjusting
// from time to time by people who are not its owner. Maintainers are those
// people (names, or a team as group:<id>). The owner keeps the most control.
//
// What a maintainer may change is a LIST, applyMaintainerEdit, not a list of
// what they may not: every field off it is copied from the owner's record.
// A field added to agents later is therefore the owner's until somebody
// decides otherwise, rather than a maintainer's by default. On the list are the
// agent's words and behaviour. Off it are its reach (who uses it, who
// maintains it, Everyone, publishing, callers, channels, where it may hand
// work and how that list is read), its access (memory,
// privacy and network settings, spending, guardrails and scanning, which have
// their own owner-only endpoint), and anything that would hand it more of the
// owner's things. The lists of the owner's things it may use (tools, skills,
// collections, pipelines, boards, auto-approved tools) a
// maintainer may shorten but not lengthen; the lists that switch something
// OFF (disabled tools, actions, pipelines, credentials, tools that never run
// unattended) they may lengthen but not shorten.
//
// Edits land on the owner's record in the owner's store, the one copy there
// is, and the version they replace is kept with the maintainer's name on it.

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/gohort/core/ui"
)

// MaintainedAgentsTable indexes maintainers: maintainer -> (owner, agent id).
const MaintainedAgentsTable = "maintained_agents"

// userMaintainsAgent reports whether user is one of a's maintainers (by name,
// or through a team). The owner is not: they own it.
func userMaintainsAgent(a AgentRecord, user string) bool {
	if strings.TrimSpace(user) == "" || a.Owner == user {
		return false
	}
	return SharedWithUser(a.Maintainers, a.Owner, user)
}

// MaintainedAgentsFor returns the agents user maintains for other people,
// read from each owner's own store and re-checked against the record.
func MaintainedAgentsFor(db Database, user string) []AgentRecord {
	if RootDB == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	var out []AgentRecord
	for _, ref := range SharedRefsFor(RootDB, MaintainedAgentsTable, user) {
		udb := UserDB(db, ref.Owner)
		if udb == nil {
			continue
		}
		a, ok := loadAgent(udb, ref.ID)
		if !ok || !isShareableAgent(a, ref.Owner) || !userMaintainsAgent(a, user) {
			continue
		}
		out = append(out, a)
	}
	sortAgentsByName(out)
	return out
}

// maintainedAgent finds the agent id that user maintains, and the owner's
// store it lives in. False when user maintains no agent by that id.
func maintainedAgent(db Database, user, id string) (AgentRecord, Database, bool) {
	id = strings.TrimSpace(id)
	if RootDB == nil || id == "" || strings.TrimSpace(user) == "" {
		return AgentRecord{}, nil, false
	}
	for _, ref := range SharedRefsFor(RootDB, MaintainedAgentsTable, user) {
		if ref.ID != id {
			continue
		}
		udb := UserDB(db, ref.Owner)
		if udb == nil {
			continue
		}
		a, ok := loadAgent(udb, id)
		if ok && isShareableAgent(a, ref.Owner) && userMaintainsAgent(a, user) {
			return a, udb, true
		}
	}
	return AgentRecord{}, nil, false
}

// applyMaintainerEdit is cur with what a maintainer may change taken from
// req, and everything else kept as the owner has it.
func applyMaintainerEdit(cur, req AgentRecord) AgentRecord {
	out := cur
	// The agent's words and behaviour.
	out.Name, out.Description, out.Category = req.Name, req.Description, req.Category
	out.Voice, out.Triggers, out.RecallHints = req.Voice, req.Triggers, req.RecallHints
	out.OrchestratorPrompt, out.PlanGuidance, out.Rules = req.OrchestratorPrompt, req.PlanGuidance, req.Rules
	out.MaxPlanSteps, out.MaxWorkerRounds, out.ThinkBudget = req.MaxPlanSteps, req.MaxWorkerRounds, req.ThinkBudget
	out.Effort, out.Think, out.ConsultLead = req.Effort, req.Think, req.ConsultLead
	out.LeadModel, out.LeadPlans, out.LeadReview = req.LeadModel, req.LeadPlans, req.LeadReview
	out.ContextDepth, out.DisableCompaction, out.IngestAttachments = req.ContextDepth, req.DisableCompaction, req.IngestAttachments
	out.DisableSkills, out.BuildNotes = req.DisableSkills, req.BuildNotes
	out.IntakeForm, out.GapCheck, out.PreMortem, out.WorkPlan = req.IntakeForm, req.GapCheck, req.PreMortem, req.WorkPlan
	out.KnowledgeModel = req.KnowledgeModel
	// What it may use of the owner's: shorter, never longer.
	out.AllowedTools = maintainerTools(req.AllowedTools, cur.AllowedTools)
	out.AllowedSkills = onlyFrom(req.AllowedSkills, cur.AllowedSkills)
	out.AttachedCollections = onlyFrom(req.AttachedCollections, cur.AttachedCollections)
	out.AttachedPipelines = onlyFrom(req.AttachedPipelines, cur.AttachedPipelines)
	out.Bulletins = onlyFrom(req.Bulletins, cur.Bulletins)
	out.AutoApproveTools = onlyFrom(req.AutoApproveTools, cur.AutoApproveTools)
	// What it has switched off: longer, never shorter.
	out.NoUnattendedTools = keepingAll(req.NoUnattendedTools, cur.NoUnattendedTools)
	out.DisabledPersistentTools = keepingAll(req.DisabledPersistentTools, cur.DisabledPersistentTools)
	out.DisabledToolActions = keepingAll(req.DisabledToolActions, cur.DisabledToolActions)
	out.DisabledPipelines = keepingAll(req.DisabledPipelines, cur.DisabledPipelines)
	out.DisabledCredentials = keepingAll(req.DisabledCredentials, cur.DisabledCredentials)
	return out
}

// maintainerTools is the tool list a maintainer may leave the agent with.
// An EMPTY list is not "no tools" but every default tool, so taking the last
// one off has to become the explicit no-tools marker or it would widen the
// agent to everything; and a list that is empty or already that marker is
// not one a maintainer can narrow from, so it stays as the owner set it.
func maintainerTools(want, have []string) []string {
	if len(have) == 0 || isNoToolsSentinel(have) {
		return have
	}
	out := onlyFrom(want, have)
	if len(out) == 0 {
		return []string{noToolsSentinel}
	}
	return out
}

// onlyFrom is want with anything not already in have dropped.
func onlyFrom(want, have []string) []string {
	allowed := make(map[string]bool, len(have))
	for _, h := range have {
		allowed[h] = true
	}
	out := want[:0:0]
	for _, w := range want {
		if allowed[w] {
			out = append(out, w)
		}
	}
	return out
}

// keepingAll is want with everything in have added back that it left out.
func keepingAll(want, have []string) []string {
	present := make(map[string]bool, len(want))
	out := append(want[:0:0], want...)
	for _, w := range want {
		present[w] = true
	}
	for _, h := range have {
		if !present[h] {
			out = append(out, h)
		}
	}
	return out
}

// saveAsMaintainer writes a maintainer's whole-record save onto the owner's
// record: what a maintainer may change comes from req, the rest stays as the
// owner has it, and the version it replaces is kept with their name on it.
func (T *OrchestrateApp) saveAsMaintainer(maintainer string, cur AgentRecord, ownerDB Database, req AgentRecord) (any, error) {
	next := applyMaintainerEdit(cur, req)
	if next.Rules != cur.Rules {
		if err := rulesProblem(next.Rules); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	saved, err := saveAgentAs(ownerDB, next, "edited by "+maintainer)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[orchestrate.agents] %s adjusted %s's agent %q as a maintainer", maintainer, cur.Owner, cur.Name)
	return saved, nil
}

// renderMaintainerEditor is the agent editor a maintainer gets: the fields
// they may change, and a line saying what stays with the owner. Everything
// else the owner's editor shows is left off, rather than shown and quietly
// not saved.
func renderMaintainerEditor(w http.ResponseWriter, r *http.Request, a AgentRecord) {
	src := orchPath("api/agents/") + url.PathEscape(a.ID)
	page := ui.Page{
		Title:     "Maintain " + chFirst(a.Name, a.ID),
		ShowTitle: true,
		BackURL:   "..?agent=" + url.QueryEscape(a.ID),
		MaxWidth:  "900px",
		Sections: []ui.Section{{
			Title:    "Agent",
			Subtitle: "You maintain this agent for " + a.Owner + ": its words and behaviour are yours to adjust.",
			Detail: "Who uses it and who maintains it, publishing, its guardrails and security settings, what it remembers, and adding tools, skills or collections stay with " + a.Owner + ". " +
				"You can take a tool, skill or collection away from it, but not give it one. Your changes are kept in its history with your name on them, and " + a.Owner + " can roll any of them back.",
			Body: ui.FormPanel{
				Source:  src,
				PostURL: orchPath("api/agents?id=") + url.QueryEscape(a.ID),
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "name", Type: "text", Label: "Name"},
					{Field: "description", Type: "text", Label: "Description"},
					{Field: "orchestrator_prompt", Type: "sections", Label: "Orchestrator prompt", Rows: 12,
						Help: "Voice, decomposition style, and synthesis approach."},
					{Field: "rules", Type: "textarea", Label: "Rules", Rows: 6,
						Help: "What it must always or never do, one rule per line."},
					{Field: "plan_guidance", Type: "textarea", Label: "Plan guidance", Rows: 3,
						Help: "Optional. Appended to the orchestrator prompt: nudges decomposition style."},
				},
			},
		}},
	}
	page.ServeHTTP(w, r)
}

// tellNewMaintainers tells each person who has just become a maintainer of a
// (by name or through a team) that they are one, and where to find it: they
// have nothing else to say so.
func tellNewMaintainers(a AgentRecord, before []string) {
	if shareledger.NotifyRecipient == nil {
		return
	}
	had := map[string]bool{}
	for _, u := range before {
		had[u] = true
	}
	for _, u := range ShareMembers(a.Maintainers, a.Owner) {
		if had[u] {
			continue
		}
		shareledger.NotifyRecipient(u, a.Owner+" made you a maintainer of "+chFirst(a.Name, a.ID),
			"You can adjust its prompt, rules and behaviour at /orchestrate/agent/"+a.ID+". "+
				"Who uses it, its guardrails, its memory and what it is given stay with "+a.Owner+".", nil)
	}
}
