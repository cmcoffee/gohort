package orchestrate

// The access page: "how much can this agent reach?", asked ABOUT an agent
// rather than while editing one.
//
// The two halves of it — what it can do, and what it can hand work to — were
// already built (agent_access.go) and lived as two sections near the bottom of
// the editor, behind everything you scroll past to get there. That is the
// wrong place for a question you ask when you are NOT editing: reviewing what
// you granted is its own errand, and the editor is a form.
//
// This page is that errand. It adds the two groups the editor never had, and
// it owns nothing: every row comes from /api/agent-access, which agent_access.go
// serves and whose honesty rule governs all of it — exact where the record
// determines it, named-but-not-enumerated where a session assembles it.
//
// READ-ONLY on purpose. It is useful the moment it exists, it cannot break
// anything, and it shows which rows somebody actually tries to click before
// any of them becomes a control. Every row names where its setting lives.

import (
	"net/http"
	"net/url"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *OrchestrateApp) renderAgentAccess(w http.ResponseWriter, r *http.Request, user string, udb Database, id string) {
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != "" && agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	name := chFirst(agent.Name, agent.ID)
	src := T.WebPrefix() + "/api/agent-access?id=" + url.QueryEscape(agent.ID)
	reach := agentReach(udb, user, agent)

	page := ui.Page{
		Title:      "What " + name + " can reach",
		ShowTitle:  true,
		BackURL:    T.WebPrefix() + "/agent/" + url.PathEscape(agent.ID),
		MaxWidth:   "980px",
		SectionNav: true,
		Nav:        HubNav("/orchestrate"),
		Sections: []ui.Section{
			{
				Title:    "What this agent can do",
				Subtitle: agentAccessSummary(agent, reach) + " " + accessCaveat,
				Detail: "The Unattended column is the GATE's own answer, not a reading of the tool: it is what would happen on a scheduled or standing run. " +
					"None of it applies in chat, where an agent uses its tools freely and always has.",
				Body: ui.Table{
					Source:    src,
					RowKey:    "name",
					EmptyText: agentToolsEmptyText(agent),
					Columns: []ui.Col{
						{Field: "name", Label: "Tool"},
						{Field: "detail", Label: "What it does", Mute: true},
						{Field: "policy", Label: "Unattended", Type: "badge"},
					},
				},
			},
			{
				Title: "What it can hand work to",
				Subtitle: "Delegation reaches past this agent's own tools: whatever it hands work to runs with ITS catalog. " +
					"Only targets that add something are listed; a recipe appears when one of its steps runs an agent.",
				Body: ui.Table{
					Source:    src + "&view=reach",
					RowKey:    "name",
					EmptyText: "Nothing. This agent cannot hand work to anything that would widen it.",
					Columns: []ui.Col{
						{Field: "name", Label: "Target"},
						{Field: "kind", Label: "Kind", Mute: true},
						{Field: "adds", Label: "What it adds", Mute: true},
					},
				},
			},
			{
				Title:    "Workspace",
				Subtitle: "The sandbox its shell and file work happen in.",
				Detail:   "Separate from its tools, and answering for all of them: a custom shell tool and the workspace tool run in the same sandbox, so these rows govern both.",
				Body: ui.Table{
					Source:    src + "&view=workspace",
					RowKey:    "name",
					EmptyText: "Nothing to report.",
					Columns: []ui.Col{
						{Field: "name", Label: ""},
						{Field: "policy", Label: "", Type: "badge"},
						{Field: "detail", Label: "", Mute: true},
						{Field: "where", Label: "Set in", Mute: true},
					},
				},
			},
			{
				Title:    "Knowledge and memory",
				Subtitle: "What it reads before answering.",
				Detail:   "Attached collections travel with the agent to anybody it is shared with. Its memory layers are per-person: what one user tells it is not what another gets back.",
				Body: ui.Table{
					Source:    src + "&view=knowledge",
					RowKey:    "name",
					EmptyText: "It answers from its prompt alone.",
					Columns: []ui.Col{
						{Field: "name", Label: ""},
						{Field: "policy", Label: "", Type: "badge"},
						{Field: "detail", Label: "", Mute: true},
						{Field: "where", Label: "Set in", Mute: true},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
