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
	// {name} is the row's own name FIELD, substituted per row. Not {row_key}:
	// the substituter resolves a placeholder by looking that key up ON THE
	// RECORD, so {row_key} finds no such field, resolves to empty, and leaves
	// a control that posts to a URL with no tool in it.
	toolWrite := T.WebPrefix() + "/api/agent-access/tool?agent=" + url.QueryEscape(agent.ID) + "&name={name}"
	reach := agentReach(udb, user, agent)

	// Absolute, not relative: this page sits one level deeper than the editor
	// (/agent/<id>/access), so a relative API path resolves against THIS page
	// and lands somewhere else. PATCH rather than POST because a form here
	// owns a few fields and must not carry the whole record: a FormPanel posts
	// everything it holds, and everything it does not hold would be blanked.
	patchURL := T.WebPrefix() + "/api/agents/" + url.PathEscape(agent.ID)

	page := ui.Page{
		Title:     "Security: " + name,
		ShowTitle: true,
		BackURL:   T.WebPrefix() + "/agent/" + url.PathEscape(agent.ID),
		MaxWidth:  "980px",
		// Tabs across the top, the same control the admin pages use: sections
		// sharing a Group land under one tab. Four, because four different
		// questions arrive here and one long scroll made the reader sort them.
		Tabbed: true,
		Nav:    HubNav("/orchestrate"),
		Sections: []ui.Section{
			{
				Title:    "What this agent can do",
				Group:    "Tools",
				Subtitle: agentAccessSummary(agent, reach) + " " + accessCaveat,
				Detail: "Two different questions, and they are set per row. The switch is ask-before-every-call IN CHAT: the turn stops and waits for you. " +
					"Runs / Queues / Never is the GATE's answer for a scheduled or standing run, where nobody is watching, and none of it applies in chat. " +
					"Both are offered only on tools that carry a record of their own: a framework tool has nothing to hold the setting, so it shows neither.",
				Body: ui.Table{
					Source:            src,
					RowKey:            "name",
					EmptyText:         agentToolsEmptyText(agent),
					Search:            true,
					SearchPlaceholder: "Find a tool",
					Columns: []ui.Col{
						{Field: "name", Label: "Tool"},
						{Field: "chat", Label: "In chat", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: "Asks", Label: "Asks first", Color: "warning"},
							{Value: "Runs", Label: "Runs free", Color: "mute"},
							{Value: "off", Label: "Not loaded", Color: "mute"},
						}},
						{Field: "origin", Label: "From", Mute: true},
						{Field: "detail", Label: "What it does", Mute: true},
					},
					RowActions: []ui.RowAction{
						// Both are offered ONLY on a row that can hold the
						// setting. A framework tool has no record of its own,
						// so a switch on its row would take the click, show
						// the new state, and revert on the next reload.
						{
							Type: "toggle", Field: "asks", OnlyIf: "governable",
							PostTo: toolWrite, Method: "PATCH",
						},
						{
							Type: "segmented", Field: "unattended", OnlyIf: "governable",
							PostTo: toolWrite, Method: "PATCH",
							Options: []ui.SelectOption{
								{Value: "allow", Label: "Runs"},
								{Value: "ask", Label: "Queues"},
								// "Never" rather than "Blocked": this ladder is
								// about unattended runs only, and a segment
								// reading Blocked on a page listing the agent's
								// tools would read as switching the tool off
								// everywhere, which is not what it does.
								{Value: "block", Label: "Never"},
							},
						},
					},
				},
			},
			{
				Group:    "Delegation",
				Title:    "Who may call it, and who it may call",
				Subtitle: "Both directions of agent-to-agent calling. The policy is the kill switch; the target list below is read only by the two \"selected\" modes.",
				Body: ui.FormPanel{
					Source:      patchURL,
					PostURL:     patchURL,
					Method:      "PATCH",
					SubmitLabel: "Save",
					Fields: []ui.FormField{
						{Field: "hidden", Type: "toggle", Label: "Hide from agent fleet",
							Help:   "Off (default) = globally callable. On drops the agent from the fleet and refuses dispatch.",
							Detail: "Globally callable means it appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). Hidden, it is dropped from that block and dispatch is refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list.\n\nThis affects FLEET visibility only. The agent still appears in your own Agents picker and stays reachable at its dashboard URL when published."},
						{Field: "dispatch_mode", Type: "select", Label: "Dispatch policy",
							Options: dispatchModeOptions(effectiveDispatchMode(agent)),
							Help:    "Which other agents this one may call via agents(action=\"run\").",
							Detail:  "This is the blast radius, and it bounds damage in a way the tool list cannot: whatever this agent calls runs with ITS catalog, not this one's. Allow all means any non-hidden agent, and is the default. Only allow, and Allow all except, draw from the target list below. Allow none blocks all dispatch and is the actual delegation kill switch."},
						{Field: "allow_builder_dispatch", Type: "toggle", Label: "Can dispatch Builder",
							Help:   "Lets this agent hand work to Builder, to author an agent, tool or app on its behalf.",
							Detail: "The call is agents(action=\"run\", agent=\"builder\"). Off by default and normally reserved to conductor agents, because authoring expects a human in the loop: the intake conversation, its clarifying pauses, and your review of the draft.\n\nSeparate from the authoring tools, which have the agent build things ITSELF; this one has it ask Builder to. Overridden by Dispatch policy = Allow none."},
					},
				},
			},
			{
				Group:    "Delegation",
				Title:    "Dispatch target list",
				Subtitle: dispatchTargetSubtitle(effectiveDispatchMode(agent)),
				Body: ui.ChipPicker{
					OptionsSource: T.WebPrefix() + "/api/agents?role=dispatch-target&self=" + url.QueryEscape(agent.ID),
					RecordSource:  patchURL,
					Field:         "allowed_dispatch_targets",
					PostTo:        patchURL,
					Method:        "PATCH",
					NameField:     "id",
					LabelField:    "name",
					DescField:     "description",
				},
			},
			{
				Title: "What it can call",
				Group: "Delegation",
				Subtitle: "The blast radius. Whatever this agent hands work to runs with ITS catalog, not this one's, " +
					"so the tools above are the floor and this list is how far past them the agent reaches.",
				Detail: "Only targets that WIDEN it are listed: something that adds nothing it already has cannot extend the damage. " +
					"A recipe appears when one of its steps runs an agent. Narrow this on the agent's own editor, under delegation.",
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
				Title: "Sub-agents",
				Group: "Delegation",
				Subtitle: "Who else holds what this agent holds. A sub-agent runs with its parent's authority, " +
					"so tightening this agent is worth nothing if something it owns is looser.",
				Detail: "Lockstep means every restriction above binds the sub-agent too. Tighter means it also carries limits of its own. " +
					"A sub-agent is private to its parent: nothing else can reach it, which is why it does not appear in the list above.",
				Body: ui.Table{
					Source:    src + "&view=subagents",
					RowKey:    "name",
					EmptyText: "None. Nothing else runs with this agent's authority.",
					Columns: []ui.Col{
						{Field: "name", Label: "Sub-agent"},
						{Field: "inherits", Label: "Restrictions", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: "lockstep", Label: "Lockstep", Color: "success"},
							{Value: "tighter", Label: "Tighter", Color: "success"},
						}},
						{Field: "detail", Label: "", Mute: true},
					},
				},
			},
			{
				Group: "Guardrails",
				Title: "Checks that hold whether or not it agrees",
				Subtitle: "A guardrail is enforcement, not guidance. An independent check reads the turn and stops it, " +
					"so it holds even when the agent decided otherwise, and the agent cannot edit it away.",
				Detail: "Its counterpart is Rules, under Configure: guidance prepended to the prompt that the agent follows, " +
					"and that the agent and Builder can both rewrite. Rules shape behaviour; these enforce it.\n\n" +
					"They inherit downward, so whatever this agent hands work to carries them too. A deployment's own rules sit beneath these as a floor you cannot lift here.",
				Body: ui.ClientRegion{
					Action: "orchestrate_rules_modal",
					Args:   map[string]any{"only": "guardrails", "agent": agent.ID},
				},
			},
			{
				Group:    "Workspace",
				Title:    "What its sandbox may reach",
				Subtitle: "Shell and file work happen in one sandbox, and these govern all of it.",
				Body: ui.FormPanel{
					Source:      patchURL,
					PostURL:     patchURL,
					Method:      "PATCH",
					SubmitLabel: "Save",
					Fields: []ui.FormField{
						{Field: "workspace_no_network", Type: "toggle", Label: "Workspace may not reach the network",
							Help: "The agent keeps its tools and its model; only code running in its workspace is stopped from dialling out.",
							Detail: "For an agent that should process text or files locally and never phone anywhere from in there. It can still read, write and run commands in the workspace.\n\n" +
								"Enforced at both ways out: the sandbox gets no network namespace, and the gohort.fetch helper refuses. Closing one alone would just move a script from one to the other.\n\n" +
								"It inherits downward, so a sub-agent cannot dial on this one's behalf, and it only ever narrows: Private mode still blocks a turn outright.\n\n" +
								"A tool already in your pool is not stopped by this: it reaches out through the brokered fetch helper, which is a path you approved and which gohort dials on its behalf. What this stops is code the agent writes and runs on the spot."},
					},
				},
			},
			{
				Title:    "How it stands now",
				Group:    "Workspace",
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
				Group:    "Access",
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
