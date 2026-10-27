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
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// settingOptions is the option list for one per-agent tri-state: the inherited
// choice, then the setting's own values, all in the setting's own WORDS.
//
// The inherited option names the value it resolves to. "Use the default" does
// not tell the reader what would happen, and what would happen is the entire
// question: the difference between leaving a setting alone and setting it to
// the same value is whether it FOLLOWS the deployment when that changes.
// "Default (Allowed)" answers both at once.
//
// Words from the setting, never the stored value - "on" means allowed for the
// workspace ceiling and shared for a memory layer, so a list reading "on" six
// times is a list nobody can act on. Ordered loosest first, the same order the
// admin page and the ceiling use.
//
// Values looser than the deployment MAXIMUM are not offered. Picking one would
// be clamped on the way out, so the control would take the click, show the new
// value, and mean the old one - which reads as the control being broken rather
// than as a ceiling doing its job. The same rule this page applies everywhere
// else: never offer a state the value cannot be.
func settingOptions(db Database, key string) []ui.SelectOption {
	out := []ui.SelectOption{{
		Value: "",
		Label: "Default (" + settingWord(key, effectiveDeploymentDefault(db, key)) + ")",
	}}
	for _, v := range deploymentDefaultChoices(db, key) {
		out = append(out, ui.SelectOption{Value: v, Label: settingWord(key, v)})
	}
	return out
}

// shareChoice is one memory layer on a shared agent: the three options and the
// line saying whether this agent decided or is following.
//
// A shared helper because four of them differ only in the field and the noun,
// and four copies of a select is where one quietly keeps the wrong option
// list, or where the words drift apart from the line underneath.
func shareChoice(field, noun, key string, db Database, owner string, agent AgentRecord) ui.FormField {
	return ui.FormField{
		Field: field, Type: "select", Label: noun,
		Options: settingOptions(db, key),
		Help:    settingSource(db, agent, key),
	}
}

// renderAgentAccess serves an agent's Security page. The page's state is
// accessPage: prepare works out the agent and what the forms offer (and ends
// the render for an agent this user may not see), buildPage lays the sections
// out tab by tab, and serve adds what depends on where the agent sits before
// sending it, as JSON when asked.
func (T *OrchestrateApp) renderAgentAccess(w http.ResponseWriter, r *http.Request, user string, udb Database, id string) {
	ap := &accessPage{T: T, w: w, r: r, user: user, udb: udb, id: id}
	if !ap.prepare() {
		return
	}
	ap.buildPage()
	ap.serve()
}

// accessPage is one render of an agent's Security page: what the preamble
// works out once (the agent, its URLs, the choices the forms offer) and the
// sections built from it, a method per run of tabs.
type accessPage struct {
	T                *OrchestrateApp
	w                http.ResponseWriter
	r                *http.Request
	user             string
	udb              Database
	id               string
	agent            AgentRecord
	ok               bool
	name             string
	forcePrivateHelp string
	src              string
	toolWrite        string
	handoffURL       string
	reach            []agentReachRow
	patchURL         string
	depthURL         string
	policyURL        string
	approveURL       string
	alwaysURL        string
	denyURL          string
	removeURL        string
	contactPresets   []ui.FieldPreset
	callable         []ui.SelectOption
	policyChoice     []ui.SelectOption
	promoteURL       string
	narrowURL        string
	page             ui.Page
}

// Standing decisions, one tab's worth at a time. Narrowed on the SERVER:
// a browser-side filter would have every tab fetch every row and hide
// most, and a count in a heading would then be counting what is not shown.
func (ap *accessPage) decisions(kind, scope string) string {
	T := ap.T
	u := T.WebPrefix() + "/api/console/permissions?agent=" + url.QueryEscape(ap.agent.ID) + "&kind=" + kind
	if scope != "" {
		u += "&scope=" + scope
	}
	return u
}

// The segmented control every decision row carries. Its value arrives in
// the POST body keyed by the field, which the policy handler now reads as
// well as ?value=, so this writes through the same setter the card list
// always did rather than a second endpoint kept in step by hand.
func (ap *accessPage) grantURL(kind string) string {
	T := ap.T
	return T.WebPrefix() + "/api/console/permissions/grant?kind=" + kind + "&agent=" + url.QueryEscape(ap.agent.ID)
}

// scope names which way a decision can move. A decision made for THIS
// agent can be widened to every agent; one that binds every agent can be
// brought back to this one. Both MOVE it rather than copying, because two
// records for one decision means the wider one goes on binding everything
// while the page shows the narrow one as the answer.
func (ap *accessPage) scopeMove(wide bool) ui.RowAction {
	if wide {
		return ui.RowAction{
			Type: "button", Label: "Just this agent", PostTo: ap.narrowURL,
			Confirm: "Narrow this to " + ap.name + " alone? Every other agent loses it.",
		}
	}
	return ui.RowAction{
		Type: "button", Label: "All agents", PostTo: ap.promoteURL,
		Confirm: "Widen this to every agent you have? Each of them carries its own persona and its own rules about what it may say.",
	}
}

// No panel here carries a submit button, and that is the whole difference
// between saving and not.
//
// SubmitLabel switches a FormPanel from per-field auto-save into
// submit-button mode, where the POST carries the WHOLE form state. With
// PATCH, auto-save sends only the field that changed; submit mode sends
// everything the panel loaded, protected keys included, which is what made
// every save here fail with a list of fields nobody had touched.
//
// It is also the right model for this page on its own terms. Each control
// is one decision, and a button that batches several is how "install an
// enforced check" ended up sharing a Save with "edit a preference". The
// segmented controls beside these have always written immediately.
//
// The two GRANT forms keep their button: they create something that does
// not exist yet and need every field before it means anything.
func (ap *accessPage) policyLadder() []ui.RowAction {
	return []ui.RowAction{{
		Type: "segmented", Field: "_policy", PostTo: ap.policyURL,
		Options: permissionLadder(),
	}, {
		Type: "button", Label: "Remove", Variant: "danger", OnlyIf: "_managed",
		PostTo:  ap.removeURL,
		Confirm: "Forget this decision entirely? It returns to the default and leaves this page.",
	}}
}

func (ap *accessPage) prepare() bool {
	T := ap.T
	ap.agent, ap.ok = loadAgent(ap.udb, ap.id)
	if !ap.ok || (ap.agent.Owner != "" && ap.agent.Owner != ap.user && ap.agent.Owner != seedOwner) {
		http.NotFound(ap.w, ap.r)
		return false
	}
	ap.name = chFirst(ap.agent.Name, ap.agent.ID)
	// A parent forced Private makes its sub-agents Private (privateParentOf).
	ap.forcePrivateHelp = "Permanently drops network and sub-agent dispatch tools, and holds the agent on the local model."
	if pp := privateParentOf(ap.agent); pp != "" {
		ap.forcePrivateHelp = pp + " is forced Private, so this agent is too, whatever this says. " + ap.forcePrivateHelp
	}
	ap.src = T.WebPrefix() + "/api/agent-access?id=" + url.QueryEscape(ap.agent.ID)
	// {name} is the row's own name FIELD, substituted per row. Not {row_key}:
	// the substituter resolves a placeholder by looking that key up ON THE
	// RECORD, so {row_key} finds no such field, resolves to empty, and leaves
	// a control that posts to a URL with no tool in it.
	ap.toolWrite = T.WebPrefix() + "/api/agent-access/tool?agent=" + url.QueryEscape(ap.agent.ID) + "&name={name}"
	ap.handoffURL = T.WebPrefix() + "/api/agent-access/handoff?agent=" + url.QueryEscape(ap.agent.ID) + "&target={id}"
	ap.reach = agentReach(ap.udb, ap.user, ap.agent)

	// Absolute, not relative: this page sits one level deeper than the editor
	// (/agent/<id>/access), so a relative API path resolves against THIS page
	// and lands somewhere else. PATCH rather than POST because a form here
	// owns a few fields and must not carry the whole record: a FormPanel posts
	// everything it holds, and everything it does not hold would be blanked.
	ap.patchURL = T.WebPrefix() + "/api/agents/" + url.PathEscape(ap.agent.ID)
	ap.depthURL = ap.patchURL + "/guardrail-depth"
	ap.policyURL = T.WebPrefix() + "/api/console/permissions/policy?id={_id}"
	ap.approveURL = T.WebPrefix() + "/api/console/approvals/approve?id={_id}"
	ap.alwaysURL = T.WebPrefix() + "/api/console/approvals/always?id={_id}"
	ap.denyURL = T.WebPrefix() + "/api/console/approvals/deny?id={_id}"
	ap.removeURL = T.WebPrefix() + "/api/console/permissions/remove?id={_id}"
	// The handles this owner already has a decision about, as one-click fills.
	// Presets rather than a closed list: a contact nobody has decided about
	// yet is exactly the one somebody is here to grant, and a picker offering
	// only what already exists could never originate anything.
	ap.contactPresets = []ui.FieldPreset{}
	for _, e := range ListContactPolicies(RootDB, ap.user) {
		if e.Target != "" && e.Scope == "" {
			ap.contactPresets = append(ap.contactPresets, ui.FieldPreset{Label: e.Target, Value: e.Target})
		}
	}
	// Everything this agent could be allowed to call, by name.
	// The same agents every other picker offers (a hidden app agent, a
	// clone-only template or a retired seed is not one to grant anything
	// about), with an app's agents under their app rather than mixed in.
	ap.callable = []ui.SelectOption{}
	for _, a := range listAgents(ap.udb, ap.user) {
		if a.ID != ap.agent.ID && !fleetHidden(a.ID) {
			ap.callable = append(ap.callable, ui.SelectOption{Value: a.ID, Label: a.Name, Group: agentGroupFor(a, "Your agents")})
		}
	}
	sort.SliceStable(ap.callable, func(i, j int) bool { return appGroupsLast(ap.callable[i].Group, ap.callable[j].Group) })
	ap.policyChoice = permissionLadder()
	ap.promoteURL = T.WebPrefix() + "/api/console/permissions/promote?id={_id}"
	ap.narrowURL = T.WebPrefix() + "/api/console/permissions/narrow?id={_id}&agent=" + url.QueryEscape(ap.agent.ID)
	return true
}

func (ap *accessPage) buildPage() {
	T := ap.T
	ap.page = ui.Page{
		Title: "Security: " + ap.name,
		// A default nobody can reach is not a default. The fleet page is one
		// arrow away from every agent that reads it.
		Nav: append(HubNav("/orchestrate"), ui.NavLink{
			Label: "All agents",
			URL:   T.WebPrefix() + "/agent/" + fleetSecurityID + "/access",
		}),
		ShowTitle: true,
		BackURL:   T.WebPrefix() + "/agent/" + url.PathEscape(ap.agent.ID),
		MaxWidth:  "980px",
		// Tabs across the top, the same control the admin pages use: sections
		// sharing a Group land under one tab. Four, because four different
		// questions arrive here and one long scroll made the reader sort them.
		Tabbed:   true,
		Sections: ap.sections(),
	}
}

// sections is the page's sections, tab by tab in the order the tabs appear: a
// tab is placed where its first section is, so the order is the page's.
func (ap *accessPage) sections() []ui.Section {
	var out []ui.Section
	for _, part := range [][]ui.Section{
		ap.requestsSections(),
		ap.toolsSections(),
		ap.delegationSections(),
		ap.limitsSections(),
		ap.guardrailsSections(),
		ap.networkSections(),
		ap.accessSections(),
		ap.shareSections(),
		ap.accessReachSections(),
	} {
		out = append(out, part...)
	}
	return out
}

// requestsSections is the Requests tab's sections, from "Waiting on you" on.
func (ap *accessPage) requestsSections() []ui.Section {
	return []ui.Section{
		{
			Group:    "Requests",
			Title:    "Waiting on you",
			Subtitle: "A run has stopped and is holding for an answer. Nothing here is a setting: each row is one decision, once.",
			Detail: "Allow once runs this call and asks again next time. Always allow runs it and records the grant, which then appears under the tab it belongs to. " +
				"Offers are different: nothing is blocked on them and the tool already works, so they read Scope it and Dismiss rather than borrowing approval words for a refusal that is not happening.",
			Body: ui.Table{
				Source:    ap.decisions("requests", ""),
				RowKey:    "_id",
				EmptyText: "Nothing is waiting. A run that stops for an answer appears here.",
				Columns: []ui.Col{
					{Field: "Who", Label: ""},
					{Field: "Detail", Label: "", Mute: true},
					{Field: "Requested", Label: "Asked", Mute: true},
				},
				// Ported from the card list one for one, conditions
				// included. These unblock a stopped run, so a control that
				// silently stopped appearing would leave a turn waiting
				// with no way to answer it.
				RowActions: []ui.RowAction{
					// Activating a drafted sub-agent is a ONE-TIME decision:
					// approving it consumes the authorization, so "once" and
					// "always" have nothing to mean.
					{Type: "button", Label: "Approve", Variant: "success", OnlyIf: "_oneshot",
						PostTo:  ap.approveURL,
						Confirm: "Approve this sub-agent? It goes live and becomes dispatchable."},
					{Type: "button", Label: "Allow once", OnlyIf: "_pending", HideIf: "_oneshot",
						PostTo:  ap.approveURL,
						Confirm: "Approve and run this once?"},
					{Type: "button", Label: "Always allow", Variant: "success", OnlyIf: "_pending", HideIf: "_oneshot",
						PostTo:  ap.alwaysURL,
						Confirm: "Approve, run, and always allow this in future?"},
					{Type: "button", Label: "Deny", Variant: "danger", OnlyIf: "_pending",
						PostTo: ap.denyURL},
					// An OFFER, not a request: nothing is blocked on it and
					// the tool already works. It never borrows approval
					// verbs, and neither button is destructive enough to
					// need a confirm.
					{Type: "button", Label: "Scope it", Variant: "success", OnlyIf: "_suggestion",
						PostTo: ap.approveURL},
					{Type: "button", Label: "Dismiss", OnlyIf: "_suggestion",
						PostTo: ap.denyURL},
				},
			},
		},
	}
}

// toolsSections is the Tools tab's sections, from "What this agent can do" on.
func (ap *accessPage) toolsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "What this agent can do",
			Group:    "Tools",
			Subtitle: agentAccessSummary(ap.agent, ap.reach) + " " + accessCaveat,
			Detail: "Grouped by what a call can touch, because that is what a decision here is about. A tool that reaches a system you connected or the open internet " +
				"is worth a per-call answer, and so is one that runs code - the sandbox bounds WHERE that code runs, not whether it runs at all. " +
				"A tool that only reads this deployment's own state is not, so it always runs and shows no controls. " +
				"What those always-on tools may do is set elsewhere: whether the agent loads the tool at all is the Tools modal, and Guardrails read the turn itself.\n\n" +
				"Where the controls do appear there are two, about two different situations. IN CHAT is ask-before-every-call: the turn stops and waits for you. " +
				"UNATTENDED is the gate's answer for a scheduled or standing run, where nobody is watching, and none of it applies in chat.\n\n" +
				"The band a tool lands in is decided by where it came from and what it declares it reaches, never by the name or category it claims - a tool can edit those, and a band it could relabel itself out of would not be a boundary.",
			Body: ui.Table{
				Source:            ap.src,
				RowKey:            "name",
				EmptyText:         agentToolsEmptyText(ap.agent),
				Search:            true,
				SearchPlaceholder: "Find a tool",
				// One band per heading, drawn as its own bordered block.
				// Rows arrive in band order, which is what decides the
				// order the headings appear in.
				GroupBy: "band",
				Columns: []ui.Col{
					{Field: "name", Label: "Tool"},
					// WHICH system, where there is one to name. The band
					// says a tool reaches something you connected; most of
					// the decision is knowing what.
					{Field: "reaches", Label: "", Type: "badge", Badges: []ui.BadgeMapping{}},
					// Whether the agent LOADS it, which the controls
					// cannot say: they set what happens when it is called,
					// not whether it is there to call. The supervision
					// state moved onto the toggle beside it, where it was
					// being shown twice.
					{Field: "loaded", Label: "Loaded", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "On", Label: "On", Color: "success"},
						{Value: "Off", Label: "Off", Color: "mute"},
					}},
					// Second line: what the tool IS. The first line is the
					// name, whether it is loaded, and the two controls,
					// which is already as much as fits across. Ellipsizing
					// the description to make room cuts the part that
					// answers what you are deciding about.
					{Field: "origin", Label: "From", Mute: true, Line: 2},
					{Field: "category", Label: "", Mute: true, Line: 2},
					{Field: "detail", Label: "", Mute: true, Line: 2},
				},
				RowActions: []ui.RowAction{
					// Both are offered ONLY on a row that can hold the
					// setting. A framework tool has no record of its own,
					// so a switch on its row would take the click, show
					// the new state, and revert on the next reload.
					//
					// Both are LABELLED. A row carries two ladders about
					// two different situations, and a bare control says
					// what its options are but never what question it
					// answers: an unlabelled switch beside an unlabelled
					// track is a guess either way.
					// The same ladder, one segment short, rather than a
					// switch. A switch asked the reader to recognise this
					// decision in a second shape, and gave it no words.
					{
						Type: "segmented", Field: "chat", OnlyIf: "governable",
						// Shown as Ask and fixed when the tool asks for every
						// agent: that is the tool's own setting.
						DisableIf: "chat_locked",
						Label:     "In chat",
						Options:   permissionLadderNoNever(),
						PostTo:    ap.toolWrite, Method: "PATCH",
					},
					{
						Type: "segmented", Field: "unattended", OnlyIf: "governable",
						Label:  "Unattended",
						PostTo: ap.toolWrite, Method: "PATCH",
						// The same three words as every other ladder
						// here. It used to say Runs / Queues / Never, which
						// was accurate - nobody is watching an unattended
						// run, so asking means filing a request rather than
						// waiting - but being right one row at a time is
						// what produced three vocabularies for one idea.
						// That difference is in the Detail above.
						Options: permissionLadder(),
					},
				},
			},
		},
		{
			Group:    "Tools",
			Title:    "Parts of a tool it may not use",
			Subtitle: "A grouped tool is one grant with several jobs inside it: workspace reads files, writes them, and runs commands.",
			Detail: "Without this the choice is all of it or none, because a tool is offered on the union of what its actions need.\n\n" +
				"Ticked here, the action is dropped from the schema the model sees, so it never plans around one it cannot have, and refused at the call as well for a name it guessed or carried over.\n\n" +
				"Only the actions that DO something are listed: withholding a read is the reason the tool was granted. It inherits downward, so a sub-agent cannot run what its parent was denied.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "disabled_tool_actions", Type: "checklist", Label: "Switched-off sub-actions",
						Options:     narrowableActionOptions(ap.user),
						Placeholder: "(nothing here can be narrowed on its own)",
						Help:        "Parts of a grouped tool this agent may not use, while it keeps the rest."},
				},
			},
		},
	}
}

// delegationSections is the Delegation tab's sections, from "Which agents it can call at all" on.
func (ap *accessPage) delegationSections() []ui.Section {
	T := ap.T
	return []ui.Section{
		{
			Group:    "Delegation",
			Title:    "Which agents it can call at all",
			Subtitle: "The first of two layers, and the kill switch. Allow none stops every call to another agent whatever is decided further down, except to this agent's own sub-agents, which it can always call; Builder answers only to its own switch below. The target list is read only by the two \"selected\" modes.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "dispatch_mode", Type: "select", Label: "Dispatch policy",
						Options: dispatchModeOptions(effectiveDispatchMode(ap.agent)),
						Help:    "Which other agents this one may call via agents(action=\"run\").",
						Detail:  "This is the blast radius, and it bounds damage in a way the tool list cannot: whatever this agent calls runs with ITS catalog, not this one's. Allow all means any non-hidden agent, and is the default. Only allow, and Allow all except, draw from the target list below. Allow none blocks all dispatch to other agents and is the actual delegation kill switch. Builder is not one of your agents and is governed by Can dispatch Builder alone."},
					{Field: "allow_builder_dispatch", Type: "toggle", Label: "Can dispatch Builder",
						Help:   "Lets this agent hand work to Builder, to author an agent, tool or app on its behalf. Holds under every dispatch policy, Allow none included.",
						Detail: "The call is agents(action=\"run\", agent=\"builder\"). Off by default and normally reserved to conductor agents, because authoring expects a human in the loop: the intake conversation, its clarifying pauses, and your review of the draft.\n\nSeparate from the authoring tools, which have the agent build things ITSELF; this one has it ask Builder to.\n\nThis is the only switch for Builder. Allow none stops every other agent but not Builder when this is on, so turn this off to stop Builder. Conductor agents can reach Builder without it, but only while their policy is not Allow none. Whatever Builder produces on a dispatch still waits for your approval."},
				},
			},
		},
		{
			Group:    "Delegation",
			Title:    "Dispatch target list",
			Subtitle: dispatchTargetSubtitle(effectiveDispatchMode(ap.agent)),
			Body: ui.ChipPicker{
				OptionsSource: T.WebPrefix() + "/api/agents?role=dispatch-target&self=" + url.QueryEscape(ap.agent.ID),
				GroupByField:  "group",
				RecordSource:  ap.patchURL,
				Field:         "allowed_dispatch_targets",
				PostTo:        ap.patchURL,
				Method:        "PATCH",
				NameField:     "id",
				LabelField:    "name",
				DescField:     "description",
			},
		},
		{
			Group: "Delegation",
			Title: "What happens when it calls one",
			Subtitle: "The policy above decides WHICH agents it can call. This decides what happens when it calls one of them: " +
				"runs straight away, stops and asks you first, or is refused.",
			Detail: "Two layers, and this is the second. A decision here about an agent the dispatch policy does not reach does nothing, " +
				"because the call never gets this far: widen the policy first, or pick a target that is already on the list.\n\n" +
				"Ask first is the default for a target you have decided nothing about, which is why most agents do not appear here until you have.",
			Body: ui.FormPanel{
				PostURL:     ap.grantURL("agent"),
				Method:      "POST",
				SubmitLabel: "Grant",
				Fields: []ui.FormField{
					{Field: "subject", Type: "select", Label: "Which agent", Options: ap.callable,
						Help:   "What this one may hand work to.",
						Detail: "Whatever it calls runs with ITS catalog, not this agent's, so this is the blast radius rather than the tool list. Granted for THIS agent only."},
					{Field: "value", Type: "select", Label: "And then", Options: ap.policyChoice,
						Help: "Always allow runs it without asking. Ask first stops and waits for you. Never refuses it outright."},
				},
			},
		},
		{
			Group: "Delegation",
			Title: "This agent's own",
			Subtitle: "Decisions that apply to this agent and nothing else. " +
				"Where a control on this tab already carries its own state it is not repeated here, because the control IS the decision.",
			Body: ui.Table{
				Source:    ap.decisions("delegation", "agent"),
				RowKey:    "_id",
				EmptyText: "No standing decisions about what it may call.",
				Columns: []ui.Col{
					{Field: "Who", Label: ""},
					{Field: "Detail", Label: "", Mute: true},
				},
				RowActions: append(ap.policyLadder(), ap.scopeMove(false)),
			},
		},
		{
			Group: "Delegation",
			Title: "Applies to every agent",
			Subtitle: "Decisions you made once for the whole fleet, which this agent reads because it has decided nothing of its own. " +
				"Shown apart because they reach further: changing one here changes it for every agent that has not overridden it. Set them together under Security for all agents.",
			Body: ui.Table{
				Source:    ap.decisions("delegation", "fleet"),
				RowKey:    "_id",
				EmptyText: "None. Everything here is this agent's own.",
				Columns: []ui.Col{
					{Field: "Who", Label: ""},
					{Field: "Detail", Label: "", Mute: true},
				},
				RowActions: append(ap.policyLadder(), ap.scopeMove(true)),
			},
		},
		{
			Title: "What it can call",
			Group: "Delegation",
			Subtitle: "The blast radius. Whatever this agent hands work to runs with ITS catalog, not this one's, " +
				"so the tools above are the floor and this list is how far past them the agent reaches.",
			Detail: "Only targets that WIDEN it are listed: something that adds nothing it already has cannot extend the damage. " +
				"A recipe appears when one of its steps runs an agent. Narrow this on the agent's own editor, under delegation.\n\n" +
				"First handoff applies when this agent's tools are narrowed. Handing work to another agent could take it past that list, so the first time it hands a given agent work, you are asked: as a card in the chat, or on the Requests tab when nobody is watching, as in a group chat. " +
				"This is separate from the dispatch setting, which says who it may reach, and Allow all does not answer it. " +
				"Approve here to answer it ahead of time, or Remove to have it ask again. Its own sub-agents and the agents on its dispatch list never ask.",
			Body: ui.Table{
				Source:    ap.src + "&view=reach",
				RowKey:    "name",
				EmptyText: "Nothing. This agent cannot hand work to anything that would widen it.",
				Columns: []ui.Col{
					{Field: "name", Label: "Target"},
					{Field: "kind", Label: "Kind", Mute: true},
					{Field: "adds", Label: "What it adds", Mute: true},
					{Field: "first_handoff", Label: "First handoff", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Approve", OnlyIf: "_asks", PostTo: ap.handoffURL + "&allow=1"},
					{Type: "button", Label: "Remove", Variant: "danger", OnlyIf: "_granted", PostTo: ap.handoffURL + "&allow=0",
						Confirm: "Take back this approval? The next time this agent hands that one work, it asks you again."},
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
				Source:    ap.src + "&view=subagents",
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
			Group:    "Delegation",
			Title:    "Who may call THIS agent",
			Subtitle: "The other direction. The rest of this tab is what this agent may call; this is what may call it, decided here rather than on every other agent.",
			Detail: "Without it, \"only these two may call me\" could only be arranged by visiting every other agent in the fleet and excluding this one, which nobody does and nothing checks held.\n\n" +
				"Separate from being listed in the fleet, which is visibility: a caller that names a hidden agent still reaches it. This is permission, and nothing on the caller's side overrides it.\n\n" +
				"A sub-agent's parent is always exempt. Ownership is the link, and a rule that locked a parent out of its own child would leave the child unreachable by anything.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "hidden", Type: "toggle", Invert: true, Label: "Listed in the agent fleet",
						Help:   "On (default) = other agents can see and call it. Off drops it from the fleet and refuses dispatch.",
						Detail: "Globally callable means it appears in every other agent's Available Agents block and is dispatchable via agents(action=\"run\"). Hidden, it is dropped from that block and dispatch is refused, UNLESS a specific caller has this agent's ID on its Allowed Dispatch Targets list.\n\nThis affects FLEET visibility only. The agent still appears in your own Agents picker and stays reachable at its dashboard URL when published."},
					{Field: "inbound_mode", Type: "select", Label: "Accepts dispatches from",
						Options: []ui.SelectOption{
							{Value: "", Label: "Any agent"},
							{Value: "only", Label: "Only the agents I list"},
							{Value: "none", Label: "No agent"},
						},
						Help: "Any agent still means the caller's own policy applies. No agent is absolute.",
					},
				},
			},
		},
		{
			Group:    "Delegation",
			Title:    "Agents that may call it",
			Subtitle: "Read only while the setting above is \"Only the agents I list\". Empty there means nothing reaches it, which is the same as No agent.",
			Body: ui.ChipPicker{
				OptionsSource: T.WebPrefix() + "/api/agents?role=dispatch-target&self=" + url.QueryEscape(ap.agent.ID),
				GroupByField:  "group",
				RecordSource:  ap.patchURL,
				Field:         "allowed_callers",
				PostTo:        ap.patchURL,
				Method:        "PATCH",
				NameField:     "id",
				LabelField:    "name",
				DescField:     "description",
			},
		},
	}
}

// limitsSections is the Limits tab's sections, from "Who may change any of this" on.
func (ap *accessPage) limitsSections() []ui.Section {
	return []ui.Section{
		{
			Group:    "Limits",
			Title:    "Who may change any of this",
			Subtitle: "You, from here and from the editor. An agent that authors asks you first.",
			Detail: "An agent holding the authoring toolset (Builder, or one you gave it to) can change any agent you own: its prompt, its rules, its tool allowlist, and what is attached to it. Before it changes or deletes one that already exists, or attaches a tool, skill, pipeline, machine or bulletin board to it, it asks you, once per agent per chat. With nobody watching, the change waits in your approval queue instead.\n\n" +
				"It cannot LOOSEN a ceiling either way: the spend cap, the action limits and the privacy lock only ratchet tighter, and an attempt to raise one is reverted and reported.",
		},
		{
			Group:    "Limits",
			Title:    "How much it may spend and how often",
			Subtitle: "Ceilings the framework keeps, not rules the agent is asked to follow.",
			Detail: "Written into a prompt instead - \"post at most six times a day\" - these are rules the model has to count for itself, and one did: it counted its own posts out of a listing, read UTC timestamps as local, and posted anyway.\n\n" +
				"Counted here, a call is refused when the allowance is spent and the agent is told when it frees up. Only SUCCESSFUL calls count, so an outage never spends the day.\n\n" +
				"These ratchet. An agent with authoring tools may lower its own limits and cannot raise or clear them - an update that tries is reverted and the agent is told to ask you.",
			Body: ui.FormPanel{
				Assist:  true,
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "action_quotas", Type: "tags", Label: "Action limits (per 24 hours)",
						Placeholder: "moltbook/create_post = 6",
						Help:        "How often one action may run in a rolling 24 hours, one per line as `action = number`.",
						Detail:      "Name a grouped tool's action (moltbook/create_post) or a whole tool (send_email). The action wins where both are set. Empty = no limit."},
					{Field: "daily_spend_usd", Type: "number", Label: "Spend limit (US$ per 24 hours)", Min: 0, Max: 1000,
						Placeholder: "0",
						Help:        "What this agent may cost in a rolling 24 hours. 0 = no limit.",
						Detail: "A turn already running is never cut off. Crossing the line drops the rest of it to the local worker model, and the NEXT turn is declined until the window frees up.\n\n" +
							"Priced from what the provider reports, so it does nothing on a deployment with no cost rates configured. Worth setting on anything scheduled against a paid model: one unattended turn can cost more than a day of chat."},
				},
			},
		},
		{
			Group:    "Limits",
			Title:    "How long a turn may run",
			Subtitle: "Explorer mode lets the worker lift its own round budget mid-turn, for an agent mapping something unfamiliar.",
			Detail:   "Off, the budget is the budget. On, the agent may raise it and the ceiling below is where it stops regardless. The ceiling is what makes this a limit rather than a blank cheque.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "allow_explorer", Type: "toggle", Label: "Let it lift its own round budget",
						Help: "For agents mapping unfamiliar APIs, where the work is not knowable in advance."},
					{Field: "explorer_hard_cap", Type: "number", Label: "Explorer ceiling", ShowWhen: "allow_explorer",
						Help: "Max rounds once it has lifted the budget. Blank or 0 = the default of 50. Only applies while the switch above is on."},
				},
			},
		},
	}
}

// guardrailsSections is the Guardrails tab's sections, from "Checks that hold whether or not it agrees" on.
func (ap *accessPage) guardrailsSections() []ui.Section {
	return []ui.Section{
		{
			Group: "Guardrails",
			Title: "Checks that hold whether or not it agrees",
			Subtitle: "A guardrail is enforcement, not guidance. An independent check reads the turn and stops it, " +
				"so it holds even when the agent decided otherwise, and the agent cannot edit it away.",
			Detail: "Its counterpart is Rules, under Configure: guidance prepended to the prompt that the agent follows, " +
				"and that the agent and Builder can both rewrite. Rules shape behaviour; these enforce it.\n\n" +
				"They inherit downward, so whatever this agent hands work to carries them too. A deployment's own rules sit beneath these as a floor you cannot lift here: they are checked on every request, action and reply, whichever checks you pick for your own rules.",
			Body: ui.ClientRegion{
				Action: "orchestrate_rules_modal",
				Args:   map[string]any{"only": "guardrails", "agent": ap.agent.ID},
			},
		},
		{
			Group:    "Guardrails",
			Title:    "How carefully they are checked",
			Subtitle: "Quick answers straight off. Moderate and Thorough reason first, which catches more and false-alarms less, at a few seconds per checked reply.",
			Body: ui.FormPanel{
				// Owner-only, like the guardrails themselves: never the
				// general PATCH, which an agent's own edit paths reach.
				Source:  ap.depthURL,
				PostURL: ap.depthURL,
				Fields: []ui.FormField{
					{Field: "guardrail_depth", Type: "select", Label: "Check depth for this agent's own guardrails",
						Options: settingOptions(RootDB, defaultGuardrailDepth),
						Help:    settingSource(RootDB, ap.agent, defaultGuardrailDepth),
						Detail: "This is about the rules written above. The deployment's own Rules are checked at the depth an administrator set for them, and when a check judges both, the more careful depth is used.\n\n" +
							"A reply is held until its check clears, so the depth is time added to every reply this agent's guardrails judge."},
				},
			},
		},
	}
}

// networkSections is the Network tab's sections, from "What its sandbox may reach" on.
func (ap *accessPage) networkSections() []ui.Section {
	return []ui.Section{
		{
			Group:    "Network",
			Title:    "What its sandbox may reach",
			Subtitle: "Shell and file work happen in one sandbox, and these govern all of it.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "workspace_network", Type: "select", Label: "Network access from the workspace",
						Options: settingOptions(RootDB, defaultWorkspaceNetwork),
						// The live state, and nothing else. What Blocked
						// MEANS is behind the ⓘ with the rest; repeating
						// it here is the line people learn to skip.
						Help: workspaceNetworkSource(RootDB, ap.agent),
						Detail: "Blocked stops code running in the workspace from dialling out; the agent keeps its tools and its model either way. For an agent that should process text or files locally and never phone anywhere from in there - it can still read, write and run commands in there.\n\n" +
							"The default is set once for the whole deployment, by an administrator.\n\n" +
							"Enforced at both ways out: the sandbox gets no network namespace, and the gohort.fetch helper refuses. Closing one alone would just move a script from one to the other.\n\n" +
							"It inherits downward, so a sub-agent cannot dial on this one's behalf, and it only ever narrows: Private mode still blocks a turn outright.\n\n" +
							"A tool already in your pool is not stopped by this: it reaches out through the brokered fetch helper, which is a path you approved and which gohort dials on its behalf. What this stops is code the agent writes and runs on the spot."},
				},
			},
		},
		{
			Group:    "Network",
			Title:    "What may leave this deployment at all",
			Subtitle: "The ceiling above is about the sandbox. This one is about the turn, and it takes the agent's model with it.",
			Detail: "Private mode drops every network tool for the turn and holds the run on the local worker, so nothing reaches a hosted model or an outside service. The workspace ceiling above is narrower on purpose: it stops code in the sandbox dialling out while the agent keeps its tools and its model.\n\n" +
				"FORCED is the one that does not move. The per-turn toggle cannot clear it, a dispatch to this agent inherits it, and an agent with authoring tools cannot turn it off on itself - an update that tries is reverted.\n\n" +
				"Allowing the toggle is a weaker thing: it puts a Private switch on the agent's chat for whoever is using it, per turn. Leave it off for an agent whose job needs the network every time.\n\n" +
				"Both are per agent and have no fleet-wide default: turning privacy on for every agent at once would silently ground a fleet, so it is a decision made one agent at a time.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "force_private", Type: "toggle", Label: "Force Private mode: never reaches out",
						Help: ap.forcePrivateHelp},
					// Moot while Private mode is forced: there is nothing left to toggle.
					{Field: "allow_private_mode", Type: "toggle", Label: "Offer a Private toggle on its chat", ShowWhen: "!force_private",
						Help: "Lets whoever is using the agent drop network tools for one turn."},
				},
			},
		},
		{
			Title:    "How it stands now",
			Group:    "Network",
			Subtitle: "The sandbox its shell and file work happen in.",
			Detail:   "Separate from its tools, and answering for all of them: a custom shell tool and the workspace tool run in the same sandbox, so these rows govern both.",
			Body: ui.Table{
				Source:    ap.src + "&view=workspace",
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
	}
}

// accessSections is the Access tab's sections, from "This agent's own" on.
func (ap *accessPage) accessSections() []ui.Section {
	return []ui.Section{
		{
			Group: "Access",
			Title: "This agent's own",
			Subtitle: "Decisions that apply to this agent and nothing else. " +
				"Where a control on this tab already carries its own state it is not repeated here, because the control IS the decision.",
			Body: ui.Table{
				Source:    ap.decisions("access", "agent"),
				RowKey:    "_id",
				EmptyText: "No standing decisions about who it may reach.",
				Columns: []ui.Col{
					{Field: "Who", Label: ""},
					{Field: "Detail", Label: "", Mute: true},
				},
				RowActions: append(ap.policyLadder(), ap.scopeMove(false)),
			},
		},
	}
}

// shareURL is the agent's one share list (agentShareGet).
func (ap *accessPage) shareURL() string {
	return ap.T.WebPrefix() + "/api/console/permissions/share?agent=" + url.QueryEscape(ap.agent.ID)
}

// publishPending reports whether the owner's Everyone is waiting on an
// administrator.
func (ap *accessPage) publishPending() bool {
	return publishPending(agentPromotionKind, ap.agent.Owner, agentPromotionName(ap.agent.ID, "exposed"))
}

// shareSections is the Share tab's sections, from "Who may run this agent" on.
func (ap *accessPage) shareSections() []ui.Section {
	T := ap.T
	return []ui.Section{
		{
			Group: "Share",
			Title: "Who may run this agent",
			// Says where Everyone stands: granted, waiting on an
			// administrator, or not asked for.
			Subtitle: shareSubtitleFor(ap.agent, ap.publishPending()),
			Detail: "People and teams are yours to add and take effect at once. Everyone reaches every signed-in user, so it is REQUESTED rather than applied: it goes to the administrator's pending queue and takes effect once approved. Taking it back out is yours and takes effect at once; the people and teams in the list keep it either way.\n\n" +
				"What the agent uses travels with it either way: your tools, your documents, your skills, readable through this agent and nowhere else. Each person gets their own sessions and memory under it. A credential is the exception, because it is whose identity a call goes out as rather than a copy anybody is missing: decide that per key.",
			// Its own door, not the record PATCH: Everyone is publishing,
			// which is requested rather than applied and has to go through
			// the one place that rule is enforced (agentShareSave).
			Body: ui.ACLPicker(ui.ACLPickerConfig{
				OptionsSource: T.WebPrefix() + "/api/user-candidates?groups=1&everyone=" + everyoneParam(ap.agent.Everyone),
				RecordSource:  ap.shareURL(),
				Field:         "allowed_users",
				PostTo:        ap.shareURL(),
				Method:        "POST",
				Noun:          "user",
				Intro:         "Everyone, people, or a team you are on, who may run this agent.",
				EmptyText:     "No other users to share with yet.",
			}),
		},
		{
			Group:    "Share",
			Title:    "Reachable from outside",
			Subtitle: "Whether an external MCP client can dispatch to this agent over gohort's /mcp/ endpoint.",
			Detail: "For example a desktop client with a bridge key calling ask_agent. Off by default, and REQUESTED rather than applied for the same reason publishing is: it takes this agent, with your tools and your documents, outside the deployment. Turning it back off is yours.\n\n" +
				"Independent of who may run it above. An agent nobody else may run can still be reachable this way, and one shared with everyone need not be.",
			Body: ui.FormPanel{
				Source: ap.patchURL,
				// The same door as the audience, and for the same reason:
				// this is reach an administrator grants, so it cannot be
				// applied by writing the record.
				PostURL: T.WebPrefix() + "/api/console/permissions/audience?agent=" + url.QueryEscape(ap.agent.ID),
				Method:  "POST",
				Fields: []ui.FormField{
					{Field: "mcp_exposed", Type: "toggle", Label: "Reachable over MCP",
						Help: "An administrator approves this. Turning it off is yours and takes effect at once."},
				},
			},
		},
		{
			Group:    "Share",
			Title:    "Published app name",
			Subtitle: "What this agent is called to the people it is published to. Blank uses its own name.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					{Field: "public_name", Type: "text", Label: "Published app name",
						Placeholder: "(uses the agent name when blank)"},
				},
			},
		},
		{
			Group:    "Share",
			Title:    "What a recipient sees",
			Subtitle: "Which of its memory layers travel with the agent when somebody else runs it.",
			Detail:   "Each is enforcement, not guidance: a recipient either reads the layer or does not, whatever the agent would say. Their own sessions and memory under it stay theirs.",
			Body: ui.FormPanel{
				Source:  ap.patchURL,
				PostURL: ap.patchURL,
				Method:  "PATCH",
				Fields: []ui.FormField{
					// Each states what a recipient GETS, and each carries
					// the third state: an agent that has not answered reads
					// the default for all agents. The help line says which
					// it is doing, so an override reads AS an override.
					//
					// The Invert hack these replace is gone with them: the
					// stored values are positive now, so nothing has to be
					// turned round on the way to the screen.
					shareChoice("share_cortex", "Its standing activity", defaultShareCortex, RootDB, ap.user, ap.agent),
					shareChoice("share_reference", "What it worked out", defaultShareReference, RootDB, ap.user, ap.agent),
					shareChoice("share_notes", "Its saved notes", defaultShareNotes, RootDB, ap.user, ap.agent),
					shareChoice("share_uploads", "Adding documents of their own", defaultShareUploads, RootDB, ap.user, ap.agent),
				},
			},
		},
	}
}

// accessReachSections is the Access tab's sections, from "Let this agent message somebody" on.
func (ap *accessPage) accessReachSections() []ui.Section {
	return []ui.Section{
		{
			Group:    "Access",
			Title:    "Let this agent message somebody",
			Subtitle: "Grants a decision that does not exist yet. Everything else on this tab changes one that does.",
			Body: ui.FormPanel{
				PostURL:     ap.grantURL("contact"),
				Method:      "POST",
				SubmitLabel: "Grant",
				Fields: []ui.FormField{
					{Field: "subject", Type: "text", Label: "Who", Presets: ap.contactPresets,
						Help:   "The handle this agent may message. The chips are people you have already decided about elsewhere.",
						Detail: "Granted for THIS agent only. An agent carries its own persona and its own rules about what it may say to somebody, so a permission the whole fleet shares is one any other agent can spend. Widen it afterwards if you mean every agent to have it."},
					{Field: "value", Type: "select", Label: "And then", Options: ap.policyChoice,
						Help: "Always allow runs it without asking. Ask first stops and waits for you. Never refuses it outright."},
				},
			},
		},
		{
			Group: "Access",
			Title: "Applies to every agent",
			Subtitle: "Decisions you made once for the whole fleet, which this agent reads because it has decided nothing of its own. " +
				"Shown apart because they reach further: changing one here changes it for every agent that has not overridden it. Set them together under Security for all agents.",
			Body: ui.Table{
				Source:    ap.decisions("access", "fleet"),
				RowKey:    "_id",
				EmptyText: "None. Everything here is this agent's own.",
				Columns: []ui.Col{
					{Field: "Who", Label: ""},
					{Field: "Detail", Label: "", Mute: true},
				},
				RowActions: append(ap.policyLadder(), ap.scopeMove(true)),
			},
		},
		{
			Title:    "Knowledge and memory",
			Group:    "Access",
			Subtitle: "What it reads before answering.",
			Detail:   "Attached collections travel with the agent to anybody it is shared with. Its memory layers are per-person: what one user tells it is not what another gets back.",
			Body: ui.Table{
				Source:    ap.src + "&view=knowledge",
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
	}
}

func (ap *accessPage) serve() bool {
	T := ap.T
	// A sub-agent belongs to its parent (agent_family.go): what only the
	// parent decides is not offered here, and a line says where it is.
	if top := topAgentOf(ap.udb, ap.agent.ID); top != ap.agent.ID {
		ap.page.Sections = subAgentSecuritySections(ap.page.Sections, ap.udb, top)
	}
	// Per-agent credential scoping, on the tab about what the agent may call.
	// Appended rather than declared inline because it is conditional: a
	// sub-agent runs on its parent's credentials, so scoping it separately
	// would offer a decision that never takes effect.
	//
	// ABSOLUTE urls. This page sits one level deeper than the editor it came
	// from (/agent/<id>/access), so the relative paths it used there resolve
	// against THIS page and land somewhere else entirely.
	if strings.TrimSpace(ap.agent.OwnedBy) == "" {
		credAPI := T.WebPrefix() + "/api/agent-credentials?id=" + url.QueryEscape(ap.agent.ID)
		ap.page.Sections = append(ap.page.Sections, ui.Section{
			Group:    "Tools",
			Title:    "External credentials",
			Subtitle: "The APIs you have been granted. All are on by default.",
			Detail:   "Uncheck any this agent should not reach; that drops the tools which dispatch through them from its kit.\n\nSecured credentials are not listed. Their access follows their tool bindings, not per-agent scope.",
			Body: ui.ChipPicker{
				Mode:          "attach",
				OptionsSource: credAPI,
				RecordsField:  "credentials",
				AttachedField: "enabled_credentials",
				PostTo:        credAPI,
				SaveKey:       "enabled_credentials",
				NameField:     "value",
				LabelField:    "label",
				DescField:     "desc",
				Noun:          "credential",
				Intro:         "Checked = this agent may use it.",
				EmptyText:     "No credentials have been granted to you yet.",
			},
		})
	}
	// ?format=json hands back the DECLARATION rather than a document, so the
	// chat overlay can draw this page where the conversation normally sits.
	// Same page either way: one call builds it and the two callers differ only
	// in what they do with it, which is what stops the panel and the page
	// drifting into two surfaces that merely resemble each other.
	if strings.TrimSpace(ap.r.URL.Query().Get("format")) == "json" {
		blob, err := ap.page.ConfigJSON()
		if err != nil {
			http.Error(ap.w, err.Error(), http.StatusInternalServerError)
			return false
		}
		ap.w.Header().Set("Content-Type", "application/json")
		ap.w.Header().Set("Cache-Control", "no-store")
		_, _ = ap.w.Write(blob)
		return false
	}
	ap.page.ServeHTTP(ap.w, ap.r)
	return true
}

// subAgentSecuritySections is a sub-agent's Security page: the parent's own
// decisions taken off, each replaced by a line naming where it is made.
//   - Share: a sub-agent is never shared, published or on a dashboard
//     (enforceSubAgentPosture), so nothing set there could take effect.
//   - Delegation: it calls nothing (agentsToolWanted), and only its parent,
//     and Builder checking what it built, can call it (agents run refuses
//     everything else), whatever the controls said.
//   - Who it may message: it sends through its parent's channels, so the
//     people its parent may message are its people (familyContactBlocked).
//   - Limits: what it spends and runs counts against its parent's
//     (familyBudgets), and it has none of its own (enforceSubAgentPosture).
func subAgentSecuritySections(secs []ui.Section, udb Database, top string) []ui.Section {
	parent := top
	if p, ok := loadAgent(udb, top); ok {
		parent = chFirst(p.Name, p.ID)
	}
	drop := map[string]bool{
		"Let this agent message somebody":     true,
		"How much it may spend and how often": true,
	}
	out := make([]ui.Section, 0, len(secs)+2)
	for _, s := range secs {
		// Delegation is the parent's: a sub-agent calls nothing and is
		// called only by its parent, so of that tab only the list of the
		// sub-agents it owns is left.
		if s.Group == "Share" || drop[s.Title] || (s.Group == "Delegation" && s.Title != "Sub-agents") {
			continue
		}
		out = append(out, s)
	}
	return append(out,
		ui.Section{
			Group: "Delegation",
			Title: "A sub-agent does not delegate",
			Subtitle: "It calls no other agent, pipeline or machine: it does its work with its own tools, and " + parent + " does the delegating. " +
				"Only " + parent + ", the agent it belongs to, and Builder checking what it built, can call it. So there is nothing to set here.",
		},
		ui.Section{
			Group:    "Limits",
			Title:    "How much it may spend and how often",
			Subtitle: "Decided on " + parent + "'s Security page. What this agent spends and runs counts against " + parent + "'s limits, so they hold for the whole family and there is nothing to set here.",
		},
		ui.Section{
			Group:    "Access",
			Title:    "Who it may message",
			Subtitle: "Decided on " + parent + "'s Security page. This agent sends through " + parent + "'s channels, so the people " + parent + " may message, or must ask about, are its people too. A block already recorded for this agent still holds.",
		},
	)
}
