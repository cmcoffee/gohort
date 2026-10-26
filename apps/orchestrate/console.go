// Orchestrator data endpoints — the fleet (standing agents), the activity
// feed (run-ledger), and pending authorizations. These back the orchestrator's
// "Enabled agents" / "Authorizations" controls.
//
// Decision (model A): a channel agent is NOT a bespoke console. It's a normal
// Agency agent (Chat is the primary one) and reuses Agency's full agent surface
// — picker, toolbar, chat — wholesale (see page_chat.go). Its channel-specific
// controls are added as sidebar nav backed by these endpoints, so it has every
// feature a normal agent has plus the fleet/authorization views.
//
// Owner-scoped, reusing the shared core spine (run-ledger + standing-agent
// store). Approvals + delegation (model A) wiring lands in a later stage.

package orchestrate

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// liveProviderOnce guards one-time registration of the agent-activity
// LiveProvider into the global live ribbon.
var liveProviderOnce sync.Once

// runIndentPrefix renders a run's tree depth as a text indent, so a sub-agent
// a turn dispatched shows nested under it in the flat live surfaces (no
// renderer change needed). Depth 0 (top-level) gets no prefix.
func runIndentPrefix(depth int) string {
	if depth <= 0 {
		return ""
	}
	return strings.Repeat("  ", depth-1) + "↳ "
}

// defaultConsoleAgent is the channel agent the console endpoints — and the
// event-monitor wake fallback — default to when no agent is specified. Chat is
// the primary channel agent now (the Operator folded into it), so legacy
// monitors with an empty WakeAgent wake Chat's channel automatically.
const defaultConsoleAgent = "seed-chat"

func consoleAgentID(r *http.Request) string {
	if a := strings.TrimSpace(r.URL.Query().Get("agent")); a != "" {
		return a
	}
	return defaultConsoleAgent
}

func (T *OrchestrateApp) registerConsoleRoutes() {
	// The console is mostly somebody's OWN operator view: their agents, runs,
	// monitors, approvals, goals, broken tools. Those routes carry no admin
	// gate, for the reason the agent routes lost theirs — each handler
	// resolves the session user and reads that user's own store, and the gate
	// meant the landing page 403'd in sixty-one places for anybody who is not
	// an administrator.
	//
	// What keeps the gate is the view ACROSS owners: the bridges table lists
	// every user's, deliberately, for an administrator to govern
	// (T.adminOpt()).
	//
	// A user-scoped action is a bare jsonapi.Post, which takes any
	// state-changing method and nothing else: the method guard the CSRF
	// protection relies on, without a gate.

	// Feed live AGENT activity into the global live ribbon — the same pill that
	// shows running apps. Agents were invisible there because they run through
	// the runs registry, not the app TaskQueue; this maps every active run
	// (chat / scheduled / standing) into a LiveEntry so "what the AI is doing"
	// shows in the same place as app work. sync.Once so a re-mount can't
	// double-register. Global scope matches the other LiveProviders.
	liveProviderOnce.Do(func() {
		RegisterLiveProvider(func() []LiveEntry {
			var out []LiveEntry
			// ActiveSnapshots is tree-ordered (parent→child) with Depth set.
			snaps := T.runsRegistry().ActiveSnapshots()
			// Decided once for the whole set, at each run's ROOT — a sub-agent
			// inside a chat turn is work you are waiting on, not work that
			// started itself, however deep it nests.
			background := markBackground(snaps)
			for i, s := range snaps {
				name := s.AgentName
				if name == "" {
					name = s.AgentID
				}
				status := s.Kind
				if s.Round > 0 {
					status += fmt.Sprintf(" - round %d", s.Round)
				}
				if s.LastTool != "" {
					status += " - " + s.LastTool
				}
				// A run with no rounds and no tools has nothing that changes —
				// a detached task is one call waiting on a backend, so it never
				// reports progress and sat in the ribbon as a motionless "task"
				// for the whole fifteen minutes of a render. Elapsed is the only
				// honest motion it has, and without it the surface meant to say
				// "this is still going" said nothing of the kind.
				if s.Round == 0 && s.LastTool == "" {
					status += " - " + shortElapsed(time.Since(s.StartedAt))
				}
				// And how far it has got, when it says: a command in the
				// background reports how much it has printed and its latest line.
				if s.Detail != "" {
					status += " - " + s.Detail
				}
				label := s.Label
				if label == "" {
					label = name
				}
				// And a way to STOP it — but only on work that HAS one. A run
				// created with no cancel func cannot be stopped, and offering
				// the control anyway is how "I pressed Stop and it kept going"
				// happens. LiveEntry.CancelURL was built for exactly this
				// distinction: empty means it cannot be stopped from here.
				cancelURL := ""
				if s.Cancellable {
					cancelURL = "/orchestrate/api/runs/" + url.PathEscape(s.ID) + "/cancel"
				}
				out = append(out, LiveEntry{
					ID: s.ID, Label: runIndentPrefix(s.Depth) + label, App: name, Status: status,
					Background: background[s.ID],
					// Where to go to actually WATCH it. For everyone but the
					// owner that is the run's own row on the monitor. The owner
					// of a run that lives in a conversation goes to the
					// conversation — the chat page reattaches to the run in
					// flight and puts Cancel on it — which is what "take me to
					// it" means to the person who was in that thread.
					URL:       "/monitor?run=" + url.QueryEscape(s.ID),
					OwnerURL:  runOwnerDestination(T.WebPrefix(), s),
					CancelURL: cancelURL,
					Order:     100 + i, // after in-view app tasks (default 0), preserving tree order
					// The label is truncateObs(the user's message) —
					// /api/live masks it for every viewer but this owner.
					Owner: s.UserID,
				})
			}
			return out
		})
	})
	T.HandleFunc("/api/console/agents", jsonapi.Get(T.DB, T.handleConsoleAgents))
	T.HandleFunc("/api/console/agents/delete", jsonapi.Delete(T.DB, T.handleConsoleAgentDelete))
	T.HandleFunc("/api/console/agents/pause", jsonapi.Post(T.DB, T.handleConsoleAgentPause))
	T.HandleFunc("/api/console/agents/resume", jsonapi.Post(T.DB, T.handleConsoleAgentResume))
	T.HandleFunc("/api/console/agents/run", jsonapi.Post(T.DB, T.handleConsoleAgentRun))
	T.HandleFunc("/api/console/agents/relink", jsonapi.Post(T.DB, T.handleConsoleAgentRelink))
	// Shared relink picker source: the owner's agents as {value:id,label:name}.
	T.HandleFunc("/api/console/agent-options", jsonapi.Get(T.DB, T.handleConsoleAgentOptions))
	// The owner's view of an agent's picture library — the first surface that
	// shows what is in it rather than describing it in the agent's own words.
	T.HandleFunc("/api/agent-images", jsonapi.Get(T.DB, T.handleAgentImages))
	T.HandleFunc("/api/agent-images/raw", T.handleAgentImageRaw)
	T.HandleFunc("/api/agent-images/action", jsonapi.Post(T.DB, T.handleAgentImageAction))
	// Fleet-wide guardrail review — read-only, so no gw() write wrapper.
	T.HandleFunc("/api/console/guardrail-blocks", jsonapi.Get(T.DB, T.handleConsoleGuardrails))
	// Deployment-wide review of every correction check and guardrail that
	// fired, admin only (judge_records.go). Read-only, so no gw() wrapper.
	T.HandleFunc("/api/console/judge-records", jsonapi.Get(T.DB, T.handleConsoleFirings))
	T.HandleFunc("/api/console/judge-records/export", T.handleConsoleFiringExport)
	// Reply guards per model, and the flagged-replies queue (reply_flags.go).
	// Admin only, checked in each handler; w() rejects a GET that writes.
	T.HandleFunc("/api/console/reply-guards", jsonapi.Get(T.DB, T.handleReplyGuards, T.adminOnlyOpt()))
	T.HandleFunc("/api/console/reply-guards/mode", jsonapi.Post(T.DB, T.handleReplyGuardMode, T.adminOnlyOpt(), jsonapi.EmptyBodyOK, jsonapi.MaxBody(1<<16)))
	T.HandleFunc("/api/console/reply-guards/setting", jsonapi.Post(T.DB, T.handleReplyGuardSetting, T.adminOnlyOpt(), jsonapi.EmptyBodyOK, jsonapi.MaxBody(1<<16)))
	T.HandleFunc("/api/console/reply-guards/clear", jsonapi.Post(T.DB, T.handleReplyGuardClear, T.adminOnlyOpt()))
	T.HandleFunc("/api/console/reply-flags", jsonapi.Get(T.DB, T.handleReplyFlags, T.adminOnlyOpt()))
	T.HandleFunc("/api/console/reply-flags/status", jsonapi.Post(T.DB, T.handleReplyFlagStatus, T.adminOnlyOpt()))
	// Every tool test and app verify, per target (build_ledger.go). Admin
	// only, read-only.
	T.HandleFunc("/api/console/build-outcomes", jsonapi.Get(T.DB, T.handleBuildOutcomes, T.adminOnlyOpt()))
	// Drafting guards from flagged replies (reply_guard_drafts.go).
	T.HandleFunc("/api/console/reply-guards/draft", jsonapi.Post(T.DB, T.handleGuardDraft, T.adminOnlyOpt()))
	T.HandleFunc("/api/console/reply-guards/authored", jsonapi.Get(T.DB, T.handleAuthoredGuards, T.adminOnlyOpt()))
	T.HandleFunc("/api/console/reply-guards/authored/act", jsonapi.Post(T.DB, T.handleAuthoredGuard, T.adminOnlyOpt(), jsonapi.EmptyBodyOK, jsonapi.MaxBody(1<<16)))
	// Anyone's thumbs-up or thumbs-down on one of their own replies.
	T.HandleFunc("/api/reply-flag", jsonapi.Methods{"POST": jsonapi.Post(T.DB, T.handleReplyFlag, jsonapi.EmptyBodyOK, jsonapi.MaxBody(1<<20)), "DELETE": jsonapi.Delete(T.DB, T.handleReplyFlag, jsonapi.EmptyBodyOK, jsonapi.MaxBody(1<<20))}.Serve)
	// Tool health: actions that have failed repeatedly and never once worked.
	T.HandleFunc("/api/console/broken-tools", jsonapi.Get(T.DB, T.handleConsoleBrokenTools))
	T.HandleFunc("/api/console/broken-tools/forget", jsonapi.Methods{"POST": jsonapi.Post(T.DB, T.handleConsoleBrokenToolForget), "DELETE": jsonapi.Delete(T.DB, T.handleConsoleBrokenToolForget)}.Serve)
	// Per-agent spend: each agent's LLM usage, banked per run (agent_spend.go).
	T.HandleFunc("/api/console/spend", jsonapi.Get(T.DB, T.handleConsoleSpend))
	T.HandleFunc("/api/console/monitors", jsonapi.Get(T.DB, T.handleConsoleMonitors))
	T.HandleFunc("/api/console/monitors/delete", jsonapi.Delete(T.DB, T.handleConsoleMonitorDelete))
	T.HandleFunc("/api/console/monitors/pause", jsonapi.Post(T.DB, T.handleConsoleMonitorPause))
	T.HandleFunc("/api/console/monitors/resume", jsonapi.Post(T.DB, T.handleConsoleMonitorResume))
	T.HandleFunc("/api/console/monitors/relink", jsonapi.Post(T.DB, T.handleConsoleMonitorRelink))
	T.HandleFunc("/api/console/monitors/run", jsonapi.Post(T.DB, T.handleConsoleMonitorRun))
	// A read: the row's Recent checks button GETs it.
	T.HandleFunc("/api/console/monitors/checks", jsonapi.Get(T.DB, T.handleConsoleMonitorChecks))
	T.HandleFunc("/api/console/monitors/get", jsonapi.Get(T.DB, T.handleConsoleMonitorGet))
	T.HandleFunc("/api/console/monitors/update", jsonapi.Post(T.DB, T.handleConsoleMonitorUpdate, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/console/agents/get", jsonapi.Get(T.DB, T.handleConsoleAgentGet))
	T.HandleFunc("/api/console/agents/update", jsonapi.Post(T.DB, T.handleConsoleAgentUpdate, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/console/activity", jsonapi.Get(T.DB, T.handleConsoleActivity))
	T.HandleFunc("/api/console/activity/cancel", jsonapi.Post(T.DB, T.handleConsoleActivityCancel))
	// The two summaries. Overview answers for the agent in view; fleet answers
	// for everything the user owns and is marked fleet-scoped in the menu so no
	// agent is appended to its request.
	T.HandleFunc("/api/console/overview", jsonapi.Get(T.DB, T.handleConsoleOverview))
	T.HandleFunc("/api/console/fleet", jsonapi.Get(T.DB, T.handleConsoleFleet))
	T.HandleFunc("/api/console/runs", jsonapi.Get(T.DB, T.handleConsoleRuns))
	T.HandleFunc("/api/console/run-detail", jsonapi.Get(T.DB, T.handleConsoleRunDetail))
	T.HandleFunc("/api/console/approvals", jsonapi.Get(T.DB, T.handleConsoleApprovals))
	T.HandleFunc("/api/console/permissions", jsonapi.Get(T.DB, T.handleConsolePermissions))
	// Widen one agent's contact grant to every agent. See
	// handleConsolePermissionPromote for why the scoped row is the default.
	T.HandleFunc("/api/console/permissions/promote", jsonapi.Post(T.DB, T.handleConsolePermissionPromote))
	T.HandleFunc("/api/console/permissions/policy", jsonapi.Post(T.DB, T.handleConsolePermissionPolicy, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/console/permissions/remove", jsonapi.Post(T.DB, T.handleConsolePermissionRemove))
	// promote widens a decision to every agent; narrow is its opposite. The
	// pair is what makes the two scopes usable in both directions.
	T.HandleFunc("/api/console/permissions/narrow", jsonapi.Post(T.DB, T.handleConsolePermissionNarrow))
	// grant CREATES one, which every other control here cannot: they all act
	// on a decision that already exists.
	T.HandleFunc("/api/console/permissions/grant", jsonapi.Post(T.DB, T.handleConsolePermissionGrant, jsonapi.EmptyBodyOK))
	// Who may RUN the agent. Its own door because publishing is requested,
	// not applied: see handleConsolePermissionAudience.
	T.HandleFunc("/api/console/permissions/audience", jsonapi.Post(T.DB, T.handleConsolePermissionAudience, jsonapi.EmptyBodyOK))
	// The agent's one share list, Everyone in it (agent_promotion.go).
	T.HandleFunc("/api/console/permissions/share", jsonapi.Methods{
		"GET":   jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) { return T.handleAgentShare(c, nil) }),
		"PATCH": jsonapi.Patch(T.DB, T.handleAgentShare, jsonapi.EmptyBodyOK),
		"POST":  jsonapi.Post(T.DB, T.handleAgentShare, jsonapi.EmptyBodyOK),
	}.Serve)
	// What holds for every agent until one says otherwise.
	// ONE default for every agent, and the ceiling over all of them. Admin-gated
	// inside the handler. There was a per-OWNER rung here too, on its own
	// endpoint, and it was one rung too many: two pages answered the same
	// question and an owner reading theirs could not see the deployment's.
	T.HandleFunc("/api/console/deployment-settings", jsonapi.Methods{
		"GET":   jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) { return T.handleDeploymentSettings(c, nil) }, T.adminOnlyOpt()),
		"PATCH": jsonapi.Patch(T.DB, T.handleDeploymentSettings, T.adminOnlyOpt(), jsonapi.EmptyBodyOK),
		"POST":  jsonapi.Post(T.DB, T.handleDeploymentSettings, T.adminOnlyOpt(), jsonapi.EmptyBodyOK),
	}.Serve)
	T.HandleFunc("/api/console/privileges", jsonapi.Post(T.DB, T.handleConsolePrivileges, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/console/approvals/approve", jsonapi.Post(T.DB, T.handleApprovalApprove))
	T.HandleFunc("/api/console/approvals/always", jsonapi.Post(T.DB, T.handleApprovalAlways))
	T.HandleFunc("/api/console/approvals/deny", jsonapi.Post(T.DB, T.handleApprovalDeny))
	T.HandleFunc("/api/console/credential-update/apply", jsonapi.Post(T.DB, T.handleCredentialUpdateApply, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/console/credential-key/replace", jsonapi.Post(T.DB, T.handleKeyReplacement, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/console/channel/clear", jsonapi.Post(T.DB, T.handleChannelClear))
	T.HandleFunc("/api/console/channel/compact", jsonapi.Post(T.DB, T.handleChannelCompact))
	T.HandleFunc("/api/console/grants", jsonapi.Get(T.DB, T.handleConsoleGrants))
	T.HandleFunc("/api/console/grants/revoke", jsonapi.Post(T.DB, T.handleGrantRevoke))
	// Bridges — deployment-wide admin management of the credential-
	// polling bridges agents have created, regardless of owner. This
	// is the admin's enable/disable switch for a bridge: a paused
	// bridge stops polling AND agents can't resume it themselves
	// (bridge tools have no resume action; only these routes and the
	// owner's console do).
	T.HandleFunc("/api/console/bridges", jsonapi.Get(T.DB, T.handleConsoleBridges, T.adminOpt()))
	T.HandleFunc("/api/console/bridges/pause", jsonapi.Post(T.DB, T.handleConsoleBridgePause, T.adminOpt()))
	T.HandleFunc("/api/console/bridges/resume", jsonapi.Post(T.DB, T.handleConsoleBridgeResume, T.adminOpt()))
	T.HandleFunc("/api/console/bridges/delete", jsonapi.Post(T.DB, T.handleConsoleBridgeDelete, T.adminOpt()))
	// Hook/unhook a bridge to a channel (Stage C: the UI equivalent of the
	// bridge tool's update action).
	T.HandleFunc("/api/console/bridges/set-channel", jsonapi.Post(T.DB, T.handleConsoleBridgeSetChannel, T.adminOpt(), jsonapi.EmptyBodyOK))
	// Channel picker for the Connect action — an owner's channels as
	// {id, label, desc}. owner is passed per-row (a bridge's own owner).
	T.HandleFunc("/api/console/bridge-channels", jsonapi.Get(T.DB, T.handleConsoleBridgeChannels, T.adminOpt()))
	// Recent activity in a poll bridge's connected channel — the HistoryPanel
	// expand on the /bridges/ poll table.
	T.HandleFunc("/api/console/bridge-thread", jsonapi.Get(T.DB, T.handleConsoleBridgeThread, T.adminOpt()))
	// Per-agent schedules rail — the agent's own event monitors + scheduled
	// runs + recurring tasks, so a schedule is visible within the agent it fires
	// (any agent, not just controllers).
	// Making one, rather than only managing what an agent made
	// (console_machine_schedule.go).
	T.HandleFunc("/api/console/machine-options", jsonapi.Get(T.DB, T.handleConsoleMachineOptions))
	T.HandleFunc("/api/console/machine-schedule/create", jsonapi.Post(T.DB, T.handleConsoleMachineScheduleCreate, jsonapi.EmptyBodyOK))
	// Delete a recurring task (the `recurring` tool's session updates) from the
	// schedules rail. Owner-checked against the task payload before unscheduling.
	T.HandleFunc("/api/console/recurring", jsonapi.Get(T.DB, T.handleConsoleRecurring))
	// The merged view the Scheduler nav entry reads — the three lists above in
	// one page, grouped. See console_scheduler.go.
	T.HandleFunc("/api/console/scheduler", jsonapi.Get(T.DB, T.handleConsoleScheduler))
	// One task's working notes: what its fires have left for each other. GET
	// reads, POST replaces (the same write path update_notes uses). Scoped by
	// (kind, id) from the row, and only for tasks this user owns. See
	// task_notes.go and docs/design/task-notes.md.
	T.HandleFunc("/api/console/scheduler/notes", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) { return T.handleTaskNotes(c, nil) }),
		"POST": jsonapi.Post(T.DB, T.handleTaskNotes, jsonapi.EmptyBodyOK),
	}.Serve)
	T.HandleFunc("/api/console/goals", jsonapi.Get(T.DB, T.handleConsoleGoals))
	// Link groups: schedules that run together at one time, one after another.
	// See schedule_links.go.
	T.HandleFunc("/api/console/links/options", jsonapi.Get(T.DB, T.handleConsoleLinkOptions))
	T.HandleFunc("/api/console/links/get", jsonapi.Get(T.DB, T.handleConsoleLinkGet))
	for _, verb := range []string{"link", "unlink", "up", "down", "pause", "resume", "run", "update"} {
		T.HandleFunc("/api/console/links/"+verb, jsonapi.Post(T.DB, T.handleConsoleLink))
	}
	T.HandleFunc("/api/console/recurring/run", jsonapi.Post(T.DB, T.handleConsoleRecurringRun))
	T.HandleFunc("/api/console/recurring/delete", jsonapi.Delete(T.DB, T.handleConsoleRecurringDelete))
	T.HandleFunc("/api/console/recurring/relink", jsonapi.Post(T.DB, T.handleConsoleRecurringRelink))
	T.HandleFunc("/api/console/recurring/resume", jsonapi.Post(T.DB, T.handleConsoleRecurringResume))
	T.HandleFunc("/api/console/recurring/pause", jsonapi.Post(T.DB, T.handleConsoleRecurringPause))
	// Get one recurring task's editable fields (for the rail's edit modal) and
	// update its schedule in place (re-validate + reschedule, prompt preserved).
	T.HandleFunc("/api/console/recurring/get", jsonapi.Get(T.DB, T.handleConsoleRecurringGet))
	T.HandleFunc("/api/console/recurring/update", jsonapi.Post(T.DB, T.handleConsoleRecurringUpdate, jsonapi.EmptyBodyOK))
	// Create a NEW recurring task from the Scheduler modal's "New recurring task"
	// button (agent + session supplied in the body; timing same shape as update).
	T.HandleFunc("/api/console/recurring/create", jsonapi.Post(T.DB, T.handleConsoleRecurringCreate, jsonapi.EmptyBodyOK))
	// Who a new task can be for: the agent itself or one of its sub-agents.
	T.HandleFunc("/api/console/recurring/for-options", jsonapi.Get(T.DB, T.handleConsoleRecurringForOptions))
}

// standingAgentOnRailOf reports whether a standing agent belongs on agentID's
// surfaces. Two agents can have a legitimate claim and BOTH get it:
//
//	AgentID       the agent that RUNS it every fire
//	ReportAgentID the agent whose session created it, and where runs report
//
// Scoping by the reporter alone hid a job from the agent actually doing it —
// you could watch an agent run a mission every morning and find nothing about
// it anywhere on that agent. Scoping by the runner alone hid it from its own
// controller, which is why it was changed to the reporter in the first place.
// They are not alternatives; each was half the answer.
//
// Its siblings on these surfaces already scope by who runs: event monitors by
// WakeAgent, recurring tasks by AgentID — the recurring block's comment even
// claims it matches "the monitor / standing-run scoping above", which until now
// it did not.
//
// A schedule that targets a PIPELINE or a MACHINE has no runner agent at all
// (exactly one of AgentID / PipelineID / MachineID is set), so the reporter is
// the only link it will ever have to an agent — it lives on the rail of
// whichever agent's session created it, and nowhere else. That is inherent, not
// a filter to relax: nothing else about it is an agent.
func standingAgentOnRailOf(sa StandingAgent, agentID string) bool {
	if agentID == "" {
		return true // unscoped view: everything
	}
	if sa.AgentID == agentID || sa.ReportAgentID == agentID {
		return true
	}
	// Knows neither who runs it nor who manages it: show it rather than let it
	// fall out of every surface there is.
	return sa.AgentID == "" && sa.ReportAgentID == ""
}

// standingRoleSuffix says WHY a standing agent is on this particular rail, when
// the answer is not the obvious one. A row that appears on two agents needs to
// read correctly on both, or it looks duplicated by mistake.
func standingRoleSuffix(udb Database, sa StandingAgent, agentID string) string {
	runner, manager := sa.AgentID, sa.ReportAgentID
	if agentID == "" || runner == "" || manager == "" || runner == manager {
		return ""
	}
	name := func(id string) string {
		if a, ok := loadAgent(udb, id); ok && strings.TrimSpace(a.Name) != "" {
			return a.Name
		}
		return id
	}
	switch agentID {
	case runner:
		return " - set up from " + name(manager)
	case manager:
		return " - runs as " + name(runner)
	}
	return ""
}

// errNotFound answers a JSON route naming something the caller does not have,
// as http.NotFound did.
var errNotFound = jsonapi.NotFound("404 page not found")
