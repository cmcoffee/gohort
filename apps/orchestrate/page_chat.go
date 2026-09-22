package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handleChatPage renders the orchestrate chat surface — a single
// AgentLoopPanel with an Agent picker in the ExtraFields strip
// (mirroring servitor's appliance dropdown). Switching agents
// re-fetches the conversation list and scopes every send to the
// active agent. CRUD on the agent itself (edit / clone / delete /
// new) is exposed via toolbar actions that pivot off whatever the
// dropdown currently has selected.
func (T *OrchestrateApp) handleChatPage(w http.ResponseWriter, r *http.Request) {
	// Where a slow render actually goes.
	//
	// Reported at 2.3 SECONDS for a plain GET of this page, with no way to
	// tell which part of it — and reading the handler for a likely suspect is
	// how the last few investigations went wrong. Phases are timed and, when
	// the whole render is slow enough to notice, reported. Silent below the
	// threshold, so an ordinary load costs two clock reads and no log line.
	phases := newPhaseTimer()
	defer phases.report("chat page")

	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	phases.mark("auth")
	// Load the user's visible agents into the picker. listAgents
	// already merges in-code seeds with any per-user shadows, so each
	// agent appears exactly once whether or not the user has tweaked
	// it. Built-ins (Builder, Chat, Research — in that explicit order)
	// at the top under the "Built-in" optgroup; everything else under
	// "Custom" sorted alphabetically. The "— select agent —"
	// placeholder is bare (no group) so it lands above both groups.
	agentOpts := []ui.SelectOption{
		{Value: "", Label: "(select agent)"},
	}
	agents := listAgents(udb, user)
	phases.mark(fmt.Sprintf("list %d agents", len(agents)))
	// First-run setup: a user who owns no agents of their own is walked
	// into creating a personal assistant (agent/wizard first-run mode)
	// instead of landing on a picker of framework seeds, which are on
	// their way out of the user-facing list. Deep links (?agent= /
	// ?session=) bypass the redirect so a shared link still lands where
	// it points. The wizard's skip pill comes back here with
	// ?skip_first_run=1 — record the dismissal on the user record
	// (cross-device) and forward to the /agents directory when other
	// users' agents are shared with them, else fall through to the
	// normal surface.
	// Redirect targets are built from WebPrefix() rather than written
	// relative. The app is mounted with http.StripPrefix, so by the time a
	// handler runs, r.URL.Path has already lost the mount prefix — a relative
	// target like "agent/wizard" resolves against "/" and sends the browser to
	// the SERVER root, which 404s whenever the app is mounted anywhere but
	// root. It only ever worked in standalone mode, where the prefix is empty.
	// ("/agents" below is genuinely root-absolute: that is a different app.)
	q := r.URL.Query()
	if q.Get("skip_first_run") == "1" {
		AuthSetFirstRunDismissed(AuthDB(), user, true)
		if T.hasSharedReachableAgents(r, user) {
			http.Redirect(w, r, "/agents", http.StatusFound)
		} else {
			http.Redirect(w, r, T.WebPrefix()+"/", http.StatusFound)
		}
		return
	}
	if needsFirstRunSetup(agents, user) && q.Get("agent") == "" && q.Get("session") == "" &&
		!AuthGetFirstRunDismissed(AuthDB(), user) {
		http.Redirect(w, r, T.WebPrefix()+"/agent/wizard?kind=assistant&first_run=1", http.StatusFound)
		return
	}
	// Grouped picker options (Built-in / Conversation Agents / Specialized
	// Agents / per-app) + the cortex-session and sub-agent maps. Built by the
	// shared agentPickerOptions so the client-side refreshAgentDropdown can
	// rebuild the SAME grouping from /api/agent-options instead of collapsing it
	// to Built-in/Custom after a Builder action. See agent_options.go.
	// pickerAgents drops the retired framework seeds (Builder stays) —
	// the same filter every picker surface applies.
	phases.mark("first-run checks")
	grouped, cortexAgents, subAgentsByParent := agentPickerOptions(pickerAgents(agents))
	phases.mark("picker options")
	agentOpts = append(agentOpts, grouped...)

	// Default the dropdown to the requested agent if the URL carries
	// `?agent=<id>` AND the user can see it — used by the editor's
	// save-redirect so you land back on the agent you just edited.
	// With no deep link, the user's Default agent preference decides:
	// a specific agent, or (unset) the last-accessed cookie. When
	// neither resolves, fall back to the user's first OWN agent —
	// Builder (the one seed left in the picker) only when they have
	// nothing of their own yet.
	fallbackAgent := ""
	for _, opt := range agentOpts {
		if opt.Value == "" {
			continue
		}
		if !isSeedID(opt.Value) {
			fallbackAgent = opt.Value
			break
		}
		if fallbackAgent == "" {
			fallbackAgent = opt.Value
		}
	}
	defaultAgent := resolveDefaultAgent(r, user, fallbackAgent, agentOpts)
	phases.mark("resolve default agent")
	if a := r.URL.Query().Get("agent"); a != "" {
		for _, opt := range agentOpts {
			if opt.Value == a {
				defaultAgent = a
				break
			}
		}
	}

	// Embed the worker-tool catalog into the page so the Tools modal
	// can render checkboxes without an extra round-trip. Same source
	// the editor uses (availableWorkerToolOptions) — sorted by group
	// + name. Inline JSON is fine; the list is short (<50 entries).
	catalogJSON, _ := json.Marshal(availableWorkerToolOptions(user))
	phases.mark("worker tool catalog")
	// Parallel list of tool names that contact the network. The Tools
	// button label uses this to subtract network-capability tools from
	// the count when Private mode is enabled (which hides them at
	// runtime — see filteredWorkerTools in runner.go). Kept as a
	// separate JS var rather than a field on SelectOption so the
	// shared SelectOption type stays domain-agnostic.
	internetJSON, _ := json.Marshal(internetWorkerToolNames())
	// subAgentsByParent → JS map for the secondary picker. Empty map
	// (no sub-agents in the fleet) is fine — the JS hides the picker
	// when the selected parent has no children.
	subAgentsJSON, _ := json.Marshal(subAgentsByParent)
	cortexAgentsJSON, _ := json.Marshal(cortexAgents)
	phases.mark("marshal head json")
	headHTML := "<script>window.ORCH_TOOL_CATALOG = " + string(catalogJSON) +
		";\nwindow.ORCH_INTERNET_TOOLS = " + string(internetJSON) +
		";\nwindow.ORCH_SUB_AGENTS = " + string(subAgentsJSON) +
		";\nwindow.ORCH_CHANNEL_AGENTS = " + string(cortexAgentsJSON) +
		";</script>\n" + TranscribeRuntimeFlagScript() + "\n" + orchestrateWebAssets

	// Builder handoff: a ?builder_brief=<id> deep-link (from the send_to_builder
	// tool or the toolbar "Send to Builder" button) carries a one-shot brief.
	// Read + consume it server-side and hand it to the chat panel as AutoSend,
	// so Builder receives it on mount through its own send path — no fragile
	// client-side fetch + DOM injection.
	phases.mark("head html concat")
	builderBrief := ""
	if bid := strings.TrimSpace(r.URL.Query().Get("builder_brief")); bid != "" {
		if udb := UserDB(T.DB, user); udb != nil {
			var brief builderBriefRecord
			if udb.Get(builderBriefTable, bid, &brief) {
				udb.Unset(builderBriefTable, bid) // one-shot
				builderBrief = brief.Text
			}
		}
	}

	// A toolbar entry that opens onto nothing is worse than a missing
	// one: it reads as a broken feature, and every user who has never
	// registered a source would meet it that way. Sources is the only
	// Configure entry whose subject can be entirely absent — an agent
	// always has tools, memory, rules — so it is the only one that
	// hides. Computed once here; the picker itself still says what to
	// do when a source disappears while the modal is open.
	phases.mark("builder brief")
	hiddenActions := map[string]bool{}
	// AnyReferenceSource, not len(ReferenceGroups(...)): the question is
	// whether there is anything to show, and building the whole catalog to
	// answer it made every source enumerate everything it owns on every page
	// render. Measured as the largest single cost in this handler.
	if !AnyReferenceSource(user) {
		hiddenActions["orchestrate_sources_modal"] = true
	}
	// Nobody to ask when everything you can open is yours. See
	// hasSomeoneElsesAgent: the toolbar is fixed at render and the agent is
	// picked afterwards, so this is per-user rather than per-agent.
	if !hasSomeoneElsesAgent(agents, user) {
		hiddenActions["orchestrate_ask_owner"] = true
	}

	// The three CALLS inside the page literal, hoisted so each is timed on its
	// own. A 340-line composite literal reads as data, and two of these reach
	// registries that walk every app and every machine — which is invisible at
	// the call site and was where thirteen seconds went.
	phases.mark("reference check")
	nav := HubNav("/orchestrate")
	phases.mark("hub nav")
	phases.mark("pre-literal")
	page := ui.Page{
		Title:         "Agents",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "100%",
		ExtraHeadHTML: headHTML,
		// Top nav — the shared hub tabs, single-sourced from the WebApp registry
		// (each member app's HubTab()); Agents is marked active here. Adding a
		// member is one HubTab() method on that app, no change to this list.
		Nav: nav,
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					// All session URLs carry {agent_id}; the runtime
					// substitutes it from the ExtraFields select value
					// every fetch. Sessions live in their agent's bucket
					// so switching the picker swaps the rail contents.
					ListURL:   "api/sessions?agent_id={agent_id}",
					LoadURL:   "api/sessions/{id}?agent_id={agent_id}",
					DeleteURL: "api/sessions/{id}?agent_id={agent_id}",
					// Channels rail section — its own region above Sessions with
					// add/edit/remove. {id} → channel id on delete; save upserts.
					ChannelsURL:    "api/channels?agent_id={agent_id}",
					DiagnosticsURL: "api/session-diag?agent={agent_id}&session={session}",
					// Phase pill — empty for every session not running a
					// machine, which is the default (docs/agent-machines.md).
					StatusURL:        "api/session-status?agent={agent_id}&session={session}",
					ChannelSaveURL:   "api/channels?agent_id={agent_id}",
					ChannelDeleteURL: "api/channels?id={id}",
					ChannelAgentsURL: "api/agents",
					// Canonical default wake rule, so the channel editor can offer
					// "Reset to default" on the gatekeeper rules (source of truth is Go).
					DefaultGatekeeperRule: DefaultDMGatekeeperRule,
					TruncateURL:           "api/sessions/{id}?agent_id={agent_id}",
					// Per-turn scrub (✕ on each bubble) — replaces the separate
					// History view's row-delete; works on every thread, not just
					// the home thread. See docs/channel-model.md.
					MessageScrub:   true,
					RenameURL:      "api/sessions/rename?agent_id={agent_id}",
					MarkAllReadURL: "api/sessions/mark-all-read?agent_id={agent_id}",
					ListTitle:      "Sessions",
					NewLabel:       "New session",
					// Same chat-app layout the public /agents/ surface
					// uses: sessions rail extends full-height on the
					// left, topbar lives inside the chat pane (not
					// spanning the rail), action buttons sit on the
					// right of the topbar.
					ListPosition: "top",
					// Move the Agent picker into the rail (above the
					// session list). The rail's sessions are scoped to
					// the active agent, so the picker reads naturally
					// as a rail header rather than a topbar control.
					ExtraFieldsInSidebar: true,
					SendURL:              "api/send",
					CancelURL:            "api/cancel",
					ConfirmURL:           "api/confirm",
					InjectURL:            "api/inject",
					// Lets an actionable card (credential setup, a config-change
					// approval) record that it was answered. Without it those
					// cards settle in the DOM only and replay with live buttons
					// on every reload, asking again for a decision already made.
					BlockResolveURL: "api/sessions/{id}/blocks/{block_id}/resolve?agent_id={agent_id}",
					// Enables the run-resume probe in the chat panel:
					// on session-load the runtime asks
					// api/runs/active?session_id=… and, if there's an
					// in-flight run, opens api/runs/<id>/stream to pick
					// up live where the prior client left off. See
					// runs.go / runs_http.go for the server contract.
					RunsURLBase:   "api/runs/",
					DeepLinkParam: "session",
					AutoSend:      builderBrief,
					LockActivity:  true,
					EmptyText:     "Pick an agent from the rail, then ask anything. The orchestrator plans the steps, the worker runs each one (tool calls appear inline), then the orchestrator replies.",
					Placeholder:   "What do you want to do?",
					// Orchestrator-mode agents (the Operator) swap the session
					// list for this nav; normal agents ignore it entirely.
					// No "Channel" or "History" rows anymore (channel model — see
					// docs/channel-model.md): the home thread is just the pinned
					// session at the top of the list (open it to read it), and
					// per-turn scrubbing is the inline ✕ on each bubble (works on
					// every thread, not only the home thread). What remains is the
					// fleet-management views + the channel-wide actions.
					// The menu is grouped, because it answers two different
					// questions and used to answer them in one undifferentiated
					// list. Everything under "This agent" acts on or reports on
					// the agent whose topbar the menu is in; everything under
					// "Your fleet" is about all of them. Without the headings the
					// fleet entries read as the open agent's, since that is what
					// every other control in this topbar means.
					//
					// "Active now" is deliberately absent. The Monitor app's first
					// table reads the same endpoint on the same interval and also
					// covers app and pipeline activity, so a second copy here was
					// the lesser of two identical views.
					OrchestratorNav: []ui.OrchestratorNavItem{
						// What this agent has been doing, in one read: how much it
						// has run, what it costs, what it has standing, and what
						// needs a person. Every figure is gathered through the same
						// helper the detailed pane below uses, so the summary and
						// the list it summarizes cannot disagree. Details opens the
						// same run record the Runs pane opens.
						{Label: "Overview", Menu: "Manage", AllAgents: true, Source: "api/console/overview", Layout: "cards",
							RowActions: []ui.OrchestratorRowAction{
								{Label: "Details", Method: "GET", URL: "api/console/run-detail", ShowResult: true, OnlyIf: "_run"},
								// The failure count was a dead end: it counts a week
								// while the list under "Needs attention" is capped at
								// three, so a bad week said nine and showed three. This
								// opens the Runs pane holding exactly what it counted —
								// carrying {agent} so the per-agent figure does not land
								// on the whole fleet's failures.
								{Label: "Show failures", View: "Fleet/Runs", Query: "status=failed&agent={agent}", Note: "Showing this agent's failed runs", OnlyIf: "_failed"},
							}},
						// ONE entry for everything that runs on a clock. It was three
						// — Enabled agents, Recurring tasks, Event monitors — which is
						// three lists to open before you know what an agent will do on
						// its own, and the honest answer to "aren't these the same
						// thing" is nearly yes: two are a clock and an agent, the third
						// is a clock and a condition.
						//
						// They stay three RECORDS, so the rows still differ (a mission,
						// a fire count, an interval) and each keeps its own actions. The
						// section headings say which is which; the server composes one
						// flag per action so a row shows only its own (see
						// console_scheduler.go), which is why the same label appears
						// several times below pointing at different endpoints.
						//
						// PINNED, not a menu entry. It was a button on the left before
						// the rail was retired, and the page it opens is the same one
						// either way — what changed was how many clicks it took to
						// reach the answer to "what will this thing do on its own",
						// which is a question asked far too often to live behind a
						// dropdown.
						//
						// The two creators ride along as VIEW actions rather than
						// separate menu items, so the button that adds a schedule sits
						// on the page that lists them.
						// Goals — every objective in flight, across all three
						// scheduling surfaces. Beside the Scheduler because it
						// lists the same records, and NOT pinned because it is a
						// question you go and ask rather than a queue you work.
						//
						// Read-only. Each of these rows is editable, resumable and
						// deletable one entry up; a second surface with its own
						// copies of those buttons would be a second set of rules
						// for the same record.
						// Breakdown: the shape of the work, drawn under what it
						// is part of. Beside Goals because they are read from
						// the same records, and separate because they sort by
						// different things: Goals by what needs you first, this
						// alphabetically, so a row stays where you last saw it.
						//
						// Read-only, like Goals. Every row here is editable one
						// page over, and the arrangement is the only thing this
						// page adds.
						{Label: "Breakdown", Menu: "Manage", Icon: "⑂", AllAgents: true, Source: "api/console/breakdown", Layout: "cards",
							SearchPlaceholder: "Search the work",
							RowActions: []ui.OrchestratorRowAction{
								{Label: "Notes", Method: "client", URL: "orchestrate_task_notes", OnlyIf: "_notes"},
							}},
						{Label: "Goals", Menu: "Manage", Icon: "◎", AllAgents: true, Source: "api/console/goals", Layout: "cards",
							SearchPlaceholder: goalsSearchHint,
							Filters:           goalsFilters(),
							// The one action this page carries, and it does not
							// break the read-only rule above: that rule is about
							// the verbs that CHANGE a schedule, which stay where
							// the record lives. What a goal's runs have worked
							// out is the same question this page asks, one row
							// down, and this is the Scheduler's own action
							// rather than a copy of it.
							RowActions: []ui.OrchestratorRowAction{
								{Label: "Notes", Method: "client", URL: "orchestrate_task_notes", OnlyIf: "_notes"},
							}},
						{Label: "Scheduler", Icon: "⏰", Pinned: true, AllAgents: true, Source: "api/console/scheduler", Layout: "cards",
							SearchPlaceholder: schedulerSearchHint,
							Filters:           schedulerFilters(),
							ViewActions: []ui.OrchestratorRowAction{
								{Label: "New recurring task", Method: "client", URL: "orchestrate_new_recurring"},
								{Label: "New machine run", Method: "client", URL: machineRunCreatorAction},
							},
							RowActions: []ui.OrchestratorRowAction{
								// What this task's fires have left for each other. On the task
								// rather than on the agent, because that is the whole scope of
								// the thing: these notes end when the task does, and the card
								// listing its goal, its attempts and its next fire is where
								// somebody reading them already is.
								{Label: "Notes", Method: "client", URL: "orchestrate_task_notes", OnlyIf: "_notes"},
								// What larger piece of work this one belongs to.
								// On every row because the decomposition people
								// actually do crosses the three kinds: a goal
								// checked by a standing agent, gathered by a
								// recurring task, watched by a monitor is one
								// piece of work in three shapes.
								// Whether this one finishes when everything under
								// it does. A toggle, and opt-in, because a parent
								// is either a real check or a heading and the link
								// cannot tell them apart: rolling up by default
								// would declare the first kind finished on
								// somebody else's evidence.
								{Label: "Finish on children", Method: "POST", URL: "api/console/scheduler/rollup", OnlyIf: "_notes",
									Confirm: "Toggle whether this finishes when everything under it has finished? With it on, it stops on its own and does not wait for its own check."},
								{Label: "Part of…", Method: "POST", URL: "api/console/scheduler/parent",
									PickerSource: "api/console/scheduler/parent-options",
									PickerTitle:  "Part of which larger piece of work?", OnlyIf: "_notes"},
								// Scheduled agents. "Edit schedule" is a CLIENT action: the
								// modal that changes a cron or an interval lives in this app's
								// own JS, which is where anything knowing what a cron is
								// belongs.
								{Label: "Edit schedule", Method: "client", URL: "orchestrate_edit_standing", OnlyIf: "_edit_standing"},
								{Label: "Run now", Method: "POST", URL: "api/console/agents/run", OnlyIf: "_run_standing", Confirm: "Run this agent's mission once right now? This is a one-off test and does not change its schedule."},
								{Label: "Pause", Method: "POST", URL: "api/console/agents/pause", OnlyIf: "_pause_standing"},
								{Label: "Resume", Method: "POST", URL: "api/console/agents/resume", OnlyIf: "_resume_standing"},
								{Label: "Relink", Method: "POST", URL: "api/console/agents/relink", PickerSource: "api/console/agent-options", PickerTitle: "Relink to a live target", OnlyIf: "_relink_standing"},
								{Label: "Move to…", Method: "POST", URL: "api/console/agents/move", PickerSource: "api/console/surface-options", PickerTitle: "Where the per-run report lands (cortex / session / background)", OnlyIf: "_move_standing"},
								{Label: "Delete", Method: "DELETE", URL: "api/console/agents/delete", Variant: "danger", OnlyIf: "_del_standing", Confirm: "Delete this standing agent and cancel its schedule?"},
								// Recurring tasks.
								{Label: "Edit schedule", Method: "client", URL: "orchestrate_edit_schedule", OnlyIf: "_edit_recurring"},
								{Label: "Run now", Method: "POST", URL: "api/console/recurring/run", OnlyIf: "_run_recurring", Confirm: "Run this recurring task's prompt once right now? This is a one-off test: it does not change the schedule or count against the fire cap."},
								{Label: "Relink", Method: "POST", URL: "api/console/recurring/relink", PickerSource: "api/console/agent-options", PickerTitle: "Relink to a live agent", OnlyIf: "_relink_recurring"},
								{Label: "Resume", Method: "POST", URL: "api/console/recurring/resume", OnlyIf: "_resume_recurring", Confirm: "Put this parked task back on its schedule? A stalled objective gets a fresh attempt allowance; what it already tried is kept."},
								{Label: "Delete", Method: "DELETE", URL: "api/console/recurring/delete", Variant: "danger", OnlyIf: "_del_recurring", Confirm: "Delete this recurring task and cancel its schedule?"},
								// Event monitors.
								{Label: "Edit schedule", Method: "client", URL: "orchestrate_edit_monitor", OnlyIf: "_edit_monitor"},
								{Label: "Test", Method: "POST", URL: "api/console/monitors/run", OnlyIf: "_test_monitor", Confirm: "Run this monitor's check once right now? If its condition matches, it will fire (wake/notify) as it would on a normal poll."},
								{Label: "Pause", Method: "POST", URL: "api/console/monitors/pause", OnlyIf: "_pause_monitor"},
								{Label: "Resume", Method: "POST", URL: "api/console/monitors/resume", OnlyIf: "_resume_monitor"},
								{Label: "Relink", Method: "POST", URL: "api/console/monitors/relink", PickerSource: "api/console/agent-options?with_default=1", PickerTitle: "Relink (Default agent, or pick a specific one)", OnlyIf: "_relink_monitor"},
								{Label: "Move to…", Method: "POST", URL: "api/console/monitors/move", PickerSource: "api/console/surface-options", PickerTitle: "Move this monitor: its card, badge & wake all follow", OnlyIf: "_move_monitor"},
								{Label: "Delete", Method: "DELETE", URL: "api/console/monitors/delete", Variant: "danger", OnlyIf: "_del_monitor", Confirm: "Delete this event monitor?"},
							}},
						// Permissions — pinned ABOVE the session list (it's an action
						// queue, not browse-config), and combines BOTH pending
						// approval requests AND the standing grants you've given on
						// one page. Approve/Always/Deny show only on pending rows
						// (_pending); Revoke only on granted rows (_granted). The
						// rail badge counts just the pending ones.
						// Permissions: pending requests render as approval cards
						// (Deny / Allow once / Always allow); standing-policy rows
						// render with a segmented Always allow - Needs approval -
						// Blocked control + Remove. _pending vs _managed picks which.
						//
						// Scoped to the agent it is opened from. It carries no
						// Scope:"fleet", so the client stamps ?agent=<id> on the
						// source and the handler narrows to it: these are the
						// permissions of AN AGENT. AllAgents stays, and is a
						// different question - it decides which agents show the
						// button, and every agent should.
						//
						// Decisions that bind every agent (a contact policy with
						// no scope, a tool set to ask wherever it appears) are
						// still listed, because they govern this agent too and
						// hiding them would let the page lie by omission.
						// PageSource, not Source: Security is the agent's whole
						// console - tabs, forms, its own sections - and it is the
						// SAME page served at /agent/<id>/access. Declared once,
						// rendered here and there, so the panel and the page
						// cannot become two surfaces that resemble each other.
						//
						// Source stays alongside it for the badge, which counts
						// pending requests and has to be a number this menu can
						// read without rendering the page.
						{Label: "Security", Icon: "🔑", PageSource: "agent/{agent}/access?format=json",
							Source: "api/console/permissions", Topbar: true, AllAgents: true, BadgeField: "_pending", Layout: "cards",
							// Tabs along the top, because four different
							// questions arrive here and an undifferentiated
							// list made the reader sort them in their head:
							// which tools it has and do they need watching,
							// what its sandbox may reach, who it may talk to,
							// what it may hand work to.
							//
							// All is first and so is the default, which is
							// what keeps a pending request in view: those
							// block a run and must not sit behind a tab
							// nobody clicked. Each chip carries its count, so
							// an empty tab says so before it is opened.
							Filters: []ui.OrchestratorViewFilter{{
								Options: []ui.OrchestratorFilterOption{
									{Label: "All"},
									{Label: "Tools", Field: "_kind", Equals: "tools"},
									{Label: "Workspace", Field: "_kind", Equals: "workspace"},
									{Label: "Access", Field: "_kind", Equals: "access"},
									{Label: "Delegation", Field: "_kind", Equals: "delegation"},
									{Label: "Requests", Field: "_kind", Equals: "requests"},
								},
							}},
							StateField: "_policy",
							StateOptions: []ui.OrchestratorStateOption{
								{Label: "Always allow", Value: "allow", URL: "api/console/permissions/policy"},
								// Hidden on a row that cannot hold it. A sandbox does
								// not queue: its network namespace is cut at spawn or
								// it is not, and a switched-off sub-action is gone
								// from the schema rather than waiting on anybody.
								{Label: "Needs approval", Value: "ask", URL: "api/console/permissions/policy", HideIf: "_noask"},
								// Hidden on a row whose only two states are ask and
								// allow: Blocked is the never-unattended mark, and on
								// an in-chat prompt row it would read as switching the
								// tool off.
								//
								// On a tool row this now means something the runtime
								// enforces: refused on any run with nobody watching,
								// and not queued, because the owner is not being
								// asked. It was hidden here while that was untrue,
								// since a segment reading "never" over a tool that
								// went on queueing for approval is a control lying
								// about the state it sets.
								{Label: "Blocked", Value: "block", URL: "api/console/permissions/policy", HideIf: "_noblock"},
							},
							// The console for this agent, reached from here and
							// nowhere else. It used to live behind a button at the
							// bottom of the EDITOR, which is the wrong place for a
							// question you ask when you are not editing, and is
							// exactly what that page's own header complains about.
							ViewActions: []ui.OrchestratorRowAction{
								{Label: "Open the full console", Method: "client", URL: "orchestrate_secure_agent"},
							},
							RowActions: []ui.OrchestratorRowAction{
								// Widen a grant that belongs to one agent. Only on
								// the scoped rows: the all-agents row has nowhere
								// wider to go.
								{Label: "All agents", Method: "POST", URL: "api/console/permissions/promote", OnlyIf: "_promotable",
									Confirm: "Let every agent you have message this contact, with whatever persona and rules each of them carries? Any agent you have specifically blocked stays blocked."},
								{Label: "Deny", Method: "POST", URL: "api/console/approvals/deny", Variant: "danger", OnlyIf: "_pending"},
								// One-shot decisions (activating a drafted sub-agent): just
								// Approve — "Allow once"/"Always" don't apply, since approving
								// activates the agent and consumes the authorization.
								{Label: "Approve", Method: "POST", URL: "api/console/approvals/approve", Variant: "success", OnlyIf: "_oneshot", Confirm: "Approve this sub-agent? It goes live and becomes dispatchable."},
								{Label: "Allow once", Method: "POST", URL: "api/console/approvals/approve", OnlyIf: "_pending", HideIf: "_oneshot", Confirm: "Approve and run this once?"},
								{Label: "Always allow", Method: "POST", URL: "api/console/approvals/always", Variant: "success", OnlyIf: "_pending", HideIf: "_oneshot", Confirm: "Approve, run, and always allow this in future?"},
								// Suggestions (_suggestion) are offers, not requests —
								// nothing is blocked on them and the tool already works.
								// They reuse the same resolve endpoints but never borrow
								// approval verbs: "Deny" would imply a refusal that isn't
								// happening, and neither button is destructive enough to
								// need a Confirm.
								{Label: "Scope it", Method: "POST", URL: "api/console/approvals/approve", Variant: "success", OnlyIf: "_suggestion"},
								{Label: "Dismiss", Method: "POST", URL: "api/console/approvals/deny", OnlyIf: "_suggestion"},
								{Label: "Remove", Method: "POST", URL: "api/console/permissions/remove", Variant: "danger", OnlyIf: "_managed", Confirm: "Forget this permission entirely? It returns to the default (needs approval) and leaves this page."},
							}},
						// The Cortex commands are the agent's, so they belong in the
						// agent's menu — but they act on its THREAD rather than on
						// what it has standing, so they sit under their own heading
						// instead of continuing the list above. A menu of their own
						// was two entries wide and put a third button in a bar that
						// already has six.
						{Label: "Compact Cortex", Menu: "Manage", Group: "Cortex", ActionURL: "api/console/channel/compact",
							Confirm: "Compact this Cortex thread now? Older messages fold into its rolling summary (still searchable via history recall); the recent tail is kept verbatim. Runs in the background: reopen the thread to see the shorter view."},
						{Label: "Clear Cortex", Menu: "Manage", Group: "Cortex", ActionURL: "api/console/channel/clear", Variant: "warning",
							Confirm: "Clear this Cortex thread's conversation and rolling summary? Your monitors, standing agents, and approvals are kept."},

						// --- Your fleet: everything the user owns, not the agent in
						// view. Each of these is fleet-scoped, so the selected agent
						// is NOT appended to the request.

						// What every agent is doing on the owner's behalf, in one
						// read: how much has run, what it cost, what is standing, and
						// what has stopped and is waiting on a person.
						{Label: "Overview", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/fleet", Layout: "cards",
							RowActions: []ui.OrchestratorRowAction{
								{Label: "Details", Method: "GET", URL: "api/console/run-detail", ShowResult: true, OnlyIf: "_run"},
								// No {agent}: this figure counts the fleet, so the list
								// it opens is the fleet's.
								{Label: "Show failures", View: "Fleet/Runs", Query: "status=failed", Note: "Showing failed runs across the fleet", OnlyIf: "_failed"},
							}},
						// The same three panes as "This agent", asked about everyone —
						// and named the same, because they ARE the same view. The
						// heading is what differs, which is the one thing that
						// differs about them; qualifying the labels as well said it
						// twice and made the pair read as two features.
						// Their handlers have always answered fleet-wide when given
						// no agent — the standing filter's own comment calls that
						// "unscoped view: everything" — but the menu stamped the
						// selected agent onto every request, so nothing could ask.
						// Each row keeps the full control it has on the per-agent
						// pane, which is what makes the inventory actionable rather
						// than a report.
						//
						// This is what replaced Decommission. That button deleted
						// every monitor, standing agent and grant the owner had, in
						// one irreversible click, showing no list of what it was
						// about to destroy — and it missed recurring tasks, so the
						// clean slate it promised left a third of the standing work
						// still firing. Everything it did is here, per row, in front
						// of the thing it acts on.
						// The same one entry, fleet-wide. Scope "fleet" sends no agent
						// parameter, so the handler answers for everything the owner
						// has rather than for the agent whose pane this is — the only
						// difference between this and the Manage entry above, and the
						// reason both exist.
						//
						// Its action set stays narrower than Manage's, as it was when
						// these were three entries: no Move to…, which relocates where
						// a report lands and belongs where you are looking at one
						// agent's arrangements rather than sweeping the fleet.
						{Label: "Goals", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/goals", Layout: "cards",
							SearchPlaceholder: goalsSearchHint,
							Filters:           goalsFilters(),
							// Same one action as the Manage entry, for the same
							// reason: reading what a goal's runs worked out is
							// this page's own question, and it is the
							// Scheduler's action rather than a copy of it.
							RowActions: []ui.OrchestratorRowAction{
								{Label: "Notes", Method: "client", URL: "orchestrate_task_notes", OnlyIf: "_notes"},
							}},
						{Label: "Scheduler", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/scheduler", Layout: "cards",
							SearchPlaceholder: schedulerSearchHint,
							Filters:           schedulerFilters(),
							RowActions: []ui.OrchestratorRowAction{
								// Scheduled agents. "Edit schedule" is a CLIENT action: the
								// modal that changes a cron or an interval lives in this app's
								// own JS, which is where anything knowing what a cron is
								// belongs.
								{Label: "Edit schedule", Method: "client", URL: "orchestrate_edit_standing", OnlyIf: "_edit_standing"},
								{Label: "Run now", Method: "POST", URL: "api/console/agents/run", OnlyIf: "_run_standing", Confirm: "Run this agent's mission once right now? This is a one-off test and does not change its schedule."},
								{Label: "Pause", Method: "POST", URL: "api/console/agents/pause", OnlyIf: "_pause_standing"},
								{Label: "Resume", Method: "POST", URL: "api/console/agents/resume", OnlyIf: "_resume_standing"},
								{Label: "Relink", Method: "POST", URL: "api/console/agents/relink", PickerSource: "api/console/agent-options", PickerTitle: "Relink to a live target", OnlyIf: "_relink_standing"},
								{Label: "Delete", Method: "DELETE", URL: "api/console/agents/delete", Variant: "danger", OnlyIf: "_del_standing", Confirm: "Delete this standing agent and cancel its schedule?"},
								// Recurring tasks.
								{Label: "Edit schedule", Method: "client", URL: "orchestrate_edit_schedule", OnlyIf: "_edit_recurring"},
								{Label: "Run now", Method: "POST", URL: "api/console/recurring/run", OnlyIf: "_run_recurring", Confirm: "Run this recurring task's prompt once right now? This is a one-off test: it does not change the schedule or count against the fire cap."},
								{Label: "Relink", Method: "POST", URL: "api/console/recurring/relink", PickerSource: "api/console/agent-options", PickerTitle: "Relink to a live agent", OnlyIf: "_relink_recurring"},
								{Label: "Resume", Method: "POST", URL: "api/console/recurring/resume", OnlyIf: "_resume_recurring", Confirm: "Put this parked task back on its schedule? A stalled objective gets a fresh attempt allowance; what it already tried is kept."},
								{Label: "Delete", Method: "DELETE", URL: "api/console/recurring/delete", Variant: "danger", OnlyIf: "_del_recurring", Confirm: "Delete this recurring task and cancel its schedule?"},
								// Event monitors.
								{Label: "Edit schedule", Method: "client", URL: "orchestrate_edit_monitor", OnlyIf: "_edit_monitor"},
								{Label: "Pause", Method: "POST", URL: "api/console/monitors/pause", OnlyIf: "_pause_monitor"},
								{Label: "Resume", Method: "POST", URL: "api/console/monitors/resume", OnlyIf: "_resume_monitor"},
								{Label: "Relink", Method: "POST", URL: "api/console/monitors/relink", PickerSource: "api/console/agent-options?with_default=1", PickerTitle: "Relink (Default agent, or pick a specific one)", OnlyIf: "_relink_monitor"},
								{Label: "Delete", Method: "DELETE", URL: "api/console/monitors/delete", Variant: "danger", OnlyIf: "_del_monitor", Confirm: "Delete this event monitor?"},
							}},
						// The durable record behind the live view: every scheduled,
						// standing, monitor and dispatched run this user owns, newest
						// first, long after the activity registry has forgotten it.
						// Details opens the full record — the step trace with each
						// call's arguments and result, the output, and the prompt
						// digest — which until now only the Operator agent could read
						// (list_runs / inspect_run), so "what did my 3am run actually
						// do" meant asking an agent.
						{Label: "Runs", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/runs", Layout: "cards", RowActions: []ui.OrchestratorRowAction{
							{Label: "Details", Method: "GET", URL: "api/console/run-detail", ShowResult: true},
						}},
						// What each agent costs: every run banks its own scoped usage
						// against the agent it ran (agent_spend.go), priced at read time
						// with the configured rates and in tokens always. One row per
						// agent, so this is a fleet view however it is opened.
						{Label: "Spend", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/spend", Layout: "cards"},
						// What the enforced rules have actually STOPPED, across every
						// agent. The per-agent log lives in the Rules modal, which is
						// the right place while editing one agent's rules and the
						// wrong one for "is anything being blocked that shouldn't
						// be" — that question is about the fleet. Read-only: the
						// block already happened; the only action is to go and look
						// at the rule.
						//
						// Fleet-scoped on purpose. This handler answers fleet-wide
						// only when given no agent, and the menu appended one to
						// every request, so until now it could never actually do so.
						{Label: "Guardrail blocks", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/guardrail-blocks", Layout: "cards"},
						// Tool actions that have failed repeatedly and never once
						// succeeded — the standing tally the outcome ledger keeps per
						// action, which until now surfaced as ONE breadcrumb on the
						// fifth failure in whichever thread tripped it. Forget clears
						// one tally after the definition is fixed; a success clears it
						// on its own.
						{Label: "Broken tools", Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/broken-tools", Layout: "cards", RowActions: []ui.OrchestratorRowAction{
							{Label: "Forget", Method: "POST", URL: "api/console/broken-tools/forget", Confirm: "Forget this action's failure tally? It starts counting again from zero; if the definition is still wrong it will be back here after five more failures."},
						}},
					},
					// core/ui is domain-agnostic: it reads the opt-in agent set
					// from the named window-global this app sets — an agentId→
					// pinned-session map — so it knows which agents get the nav
					// and what thread to pin each to, without hardcoding the id
					// scheme.
					AltNavFlag:      "ORCH_CHANNEL_AGENTS",
					AltPrimaryLabel: "Cortex",
					// "+ New ▾" offers a clean-room session. Picking it opens a
					// fresh thread and arms incognito on the first send, so the
					// runner stamps the session as a clean room at creation: no
					// cortex standing context, no memory/facts carried in, and
					// nothing stored back. A creation-time choice, which is why
					// it lives here rather than in the per-turn Modes pills.
					NewVariants: []ui.NewSessionVariant{
						{
							Label:  "New incognito session",
							Title:  "Start a clean-room session: no cortex standing context and no memory/facts carried in, and nothing stored back. Set when the session is created.",
							Extras: map[string]any{"incognito": true},
						},
					},
					Markdown:    true,
					BulkSelect:  true,
					Attachments: true,
					ExtraFields: []ui.ChatField{
						{
							Name:        "agent_id",
							Label:       "Agent",
							Type:        "select",
							OptionPairs: agentOpts,
							// Defaults to the Chat seed normally; when the
							// editor's save-redirect carries `?agent=<id>`
							// we land on THAT agent instead so the user
							// stays where they were editing.
							Default: defaultAgent,
						},
					},
					Modes: []ui.ChatMode{
						{
							Label:     "Private",
							Title:     "Drop internet tools (web_search, fetch_url, browse_page, …) for this and subsequent turns. Local + agent-management tools still work.",
							GetURL:    "api/settings/private",
							PostURL:   "api/settings/private/set",
							Field:     "private_mode",
							SendField: "private_mode",
						},
						{
							Label:     "Clean",
							Title:     "Suppress the Reference Memory layer for this turn: memory_save / memory_search / memory_forget stripped from the agent's catalog so it can't write to or read from its accumulated derived store. The agent answers fresh from the user's question plus the Knowledge layer (uploaded files) and Explicit Memory (facts), without prior memory_save findings coloring the response. Use when you want the agent unbiased by its own accumulated history.",
							GetURL:    "api/settings/memory",
							PostURL:   "api/settings/memory/set",
							Field:     "inferred_disabled",
							SendField: "inferred_disabled",
						},
					},
					// Incognito is NOT a live toggle — it's a creation-time
					// choice (it severs cortex standing context + memory for the
					// NEW session and stores nothing back). Presenting it as a
					// pill alongside Private/Clean read as a current-thread
					// switch, which it never was. It now rides the rail's
					// "+ New ▾" menu, where it actually applies: picking it
					// opens a fresh session and arms incognito onto that
					// session's first send (the server stamps it at creation).
					// Edit stays a flat button (the one you reach for mid-chat);
					// everything else collapses into Agent / Configure / Session
					// overflow menus so the toolbar isn't a wall of 15 buttons.
					//
					// These three and the nav dropdowns (Manage / Fleet)
					// share one bar, so no name may appear in both — two buttons
					// reading alike with different contents behind them makes both
					// meaningless. This group is the agent RECORD: create it,
					// clone it, delete it. What the agent is DOING is Manage.
					Actions: pruneToolbar(hiddenActions, []ui.ToolbarAction{
						{Group: "Agent", Label: "Edit", Title: "Edit the active agent",
							Method: "client", URL: "orchestrate_edit_agent"},
						{Group: "Agent", Label: "Create", Title: "Create a new agent. When a parent agent is currently selected, asks whether to mint a top-level agent or a sub-agent owned by that parent (sub-agent layout masks public / intake / memory fields).",
							Method: "client", URL: "orchestrate_create_agent"},
						{Group: "Agent", Label: "Clone", Title: "Clone the active agent into a new draft",
							Method: "client", URL: "orchestrate_clone_agent"},
						{Group: "Agent", Label: "Import", Title: "Import an agent recipe from a JSON file",
							Method: "client", URL: "orchestrate_import_agent"},
						{Group: "Agent", Label: "Export", Title: "Download the active agent as a portable JSON recipe",
							Method: "client", URL: "orchestrate_export_agent"},
						{Group: "Agent", Label: "Delete", Title: "Delete the active agent",
							Method: "client", URL: "orchestrate_delete_agent", Variant: "danger"},
						{Group: "Configure", Label: "Tools", Title: "Review and edit the active agent's tool allowlist",
							Method: "client", URL: "orchestrate_tools_modal"},
						{Group: "Configure", Label: "Memory", Title: "Review and prune the active agent's learned notes",
							Method: "client", URL: "orchestrate_memory_modal"},
						{Group: "Configure", Label: "Knowledge", Title: "Manage what data this agent draws on: your uploaded docs + attached Document Collections.",
							Method: "client", URL: "orchestrate_knowledge_modal"},
						{Group: "Configure", Label: "Sources", Title: "Attach what this agent can reach INTO: file stores, servitor systems, connected document services. Each attachment adds its own named tools to the agent.",
							Method: "client", URL: "orchestrate_sources_modal"},
						{Group: "Configure", Label: "Rules", Title: "Review and edit the active agent's standing rules",
							Method: "client", URL: "orchestrate_rules_modal"},
						{Group: "Configure", Label: "Skills", Title: "Manage what this agent can do: allowlist skills (behavior modifications) and experts (consultable brains).",
							Method: "client", URL: "orchestrate_skills_modal"},
						{Group: "Configure", Label: "Pipelines", Title: "Attach saved multi-stage pipelines to this agent: each becomes a callable run_<pipeline> tool.",
							Method: "client", URL: "orchestrate_pipelines_modal"},
						{Group: "Configure", Label: "Machines", Title: "Phase machines: give this agent a workflow it moves through and stays in, instead of re-deciding its approach every turn.",
							Method: "client", URL: "orchestrate_machines_modal"},
						// Opens the agent's Security page rather than a modal of
						// its own. It used to carry force_private, hidden and the
						// dispatch targets directly, which made three surfaces
						// holding one fact.
						{Group: "Configure", Label: "Security", Title: "Everything that bounds this agent: its tools and whether they need watching, what its sandbox may reach, who it may talk to, and what it may hand work to.",
							Method: "client", URL: "orchestrate_secure_agent"},
						{Group: "Session", Label: "Copy session", Title: "Copy the full session as markdown (every user message, every assistant round, every tool call/result) for pasting into a prompt-tuning chat.",
							Method: "client", URL: "copy_session"},
						{Group: "Session", Label: "Save log", Title: "Download the current session as a Markdown transcript (full trace with tool calls). Useful for sharing or debugging.",
							Method: "client", URL: "orchestrate_export_session"},
						{Group: "Session", Label: "Send to Builder", Title: "Something wrong with this agent? Say what it is, and the session goes to Builder with it so Builder fixes what you meant rather than whatever it notices first.",
							Method: "client", URL: "orchestrate_send_to_builder"},
						// Its sibling for an agent that is not yours to fix.
						// Send to Builder assumes you can change the thing;
						// this one asks the person who can. The agent offers
						// the same by tool when it hits a wall — a button is
						// for the user who has already decided to ask and
						// should not have to phrase it so a model picks a tool.
						{Group: "Session", Label: "Ask the owner", Title: "Need something this agent cannot reach — a document collection, a tool, a credential? Ask the person who owns it. It goes to their notifications; there is no reply here, so try again once they grant it.",
							Method: "client", URL: "orchestrate_ask_owner"},
					}),
				},
			},
		},
	}
	phases.mark("assemble page")
	// Counted, because "render" includes writing the body to the client and
	// those are different problems with different fixes. A slow ASSEMBLE is
	// server work to optimize; a slow SERVE of a large body is a page that is
	// too big, and no amount of tuning the handler touches it.
	counted := &countingWriter{ResponseWriter: w}
	page.ServeHTTP(counted, r)
	phases.mark(fmt.Sprintf("serve %s", HumanSize(counted.n)))
}

// countingWriter records how many bytes a handler wrote, so a slow response
// can be attributed to its size rather than guessed at.
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when it supports it — a page render
// does not stream, but wrapping a ResponseWriter that silently loses Flush is
// the kind of thing that breaks something else later.
func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// pruneToolbar drops the toolbar entries whose client action is in hide.
// Written as a filter over the whole list rather than a conditional
// append inside it, because the list is one long literal and splicing a
// conditional entry into the middle of it is how the Configure group
// ends up in a different order depending on what you have configured.
func pruneToolbar(hide map[string]bool, in []ui.ToolbarAction) []ui.ToolbarAction {
	if len(hide) == 0 {
		return in
	}
	out := in[:0:0]
	for _, a := range in {
		if a.Method == "client" && hide[a.URL] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// phaseTimer records how long the named parts of a request took, and says so
// only when the whole thing was slow enough to be worth a line.
//
// Cheap by construction: a slice append and a clock read per phase, and no
// output at all on a normal request. The alternative — reading a long handler
// and picking a likely suspect — is what turned a 2.3-second page load into
// several rounds of confident wrong answers elsewhere in this codebase.
type phaseTimer struct {
	start  time.Time
	last   time.Time
	phases []string
}

// slowPageThreshold is when a render becomes worth reporting. Well above a
// healthy load and well below anything a person would call slow, so the log
// stays empty until something is actually wrong.
const slowPageThreshold = 750 * time.Millisecond

// The goroutine dump that used to live here is gone. It answered its one
// question — every phase in this handler is map and slice work that cannot
// take seconds, so was the goroutine BLOCKED? — and the answer was no: on
// every slow render it was the only one running while the rest of the process
// sat in IO wait. That redirected the search from locks to actual work.
//
// Removed rather than kept, because runtime.Stack(_, true) writes every
// goroutine in the process: close to a megabyte per slow load on a server with
// live terminals and SSE streams open. A diagnostic that costs a megabyte to
// answer a question already answered is noise, and it lands in the log the
// operator has to read.

func newPhaseTimer() *phaseTimer {
	now := time.Now()
	return &phaseTimer{start: now, last: now}
}

// mark closes the phase that just ran.
func (p *phaseTimer) mark(name string) {
	if p == nil {
		return
	}
	now := time.Now()
	p.phases = append(p.phases, fmt.Sprintf("%s %s", name, now.Sub(p.last).Round(time.Millisecond)))
	p.last = now
}

// report logs the breakdown if the total crossed the threshold.
func (p *phaseTimer) report(what string) {
	if p == nil {
		return
	}
	total := time.Since(p.start)
	if total < slowPageThreshold {
		return
	}
	// Only add a trailing bucket when the caller did not close the last phase
	// itself. A handler that marks its own final step would otherwise show a
	// spurious near-zero "render" after it.
	if time.Since(p.last) > time.Millisecond {
		p.mark("remainder")
	}
	Log("[orchestrate.page] %s took %s: %s", what, total.Round(time.Millisecond), strings.Join(p.phases, ", "))
}
