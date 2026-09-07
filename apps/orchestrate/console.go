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
	g := T.adminGated
	gw := T.adminGatedWrite // admin + rejects safe methods (mutating actions)

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
					status += fmt.Sprintf(" · round %d", s.Round)
				}
				if s.LastTool != "" {
					status += " · " + s.LastTool
				}
				// A run with no rounds and no tools has nothing that changes —
				// a detached task is one call waiting on a backend, so it never
				// reports progress and sat in the ribbon as a motionless "task"
				// for the whole fifteen minutes of a render. Elapsed is the only
				// honest motion it has, and without it the surface meant to say
				// "this is still going" said nothing of the kind.
				if s.Round == 0 && s.LastTool == "" {
					status += " · " + shortElapsed(time.Since(s.StartedAt))
				}
				label := s.Label
				if label == "" {
					label = name
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
					URL:      "/monitor?run=" + url.QueryEscape(s.ID),
					OwnerURL: runOwnerDestination(T.WebPrefix(), s),
					// And a way to STOP it. The endpoint has existed all along,
					// ownership-checked, with a live cancel func behind it —
					// nothing ever called it, so a fifteen-minute render or a
					// four-piece set the user had changed their mind about had
					// no off switch but waiting.
					CancelURL: "/orchestrate/api/runs/" + url.PathEscape(s.ID) + "/cancel",
					Order:     100 + i, // after in-view app tasks (default 0), preserving tree order
					// The label is truncateObs(the user's message) —
					// /api/live masks it for every viewer but this owner.
					Owner: s.UserID,
				})
			}
			return out
		})
	})
	T.HandleFunc("/api/console/agents", g(T.handleConsoleAgents))
	T.HandleFunc("/api/console/agents/delete", gw(T.handleConsoleAgentDelete))
	T.HandleFunc("/api/console/agents/pause", gw(T.handleConsoleAgentPause))
	T.HandleFunc("/api/console/agents/resume", gw(T.handleConsoleAgentResume))
	T.HandleFunc("/api/console/agents/run", gw(T.handleConsoleAgentRun))
	T.HandleFunc("/api/console/agents/relink", gw(T.handleConsoleAgentRelink))
	// Shared relink picker source: the owner's agents as {value:id,label:name}.
	T.HandleFunc("/api/console/agent-options", g(T.handleConsoleAgentOptions))
	// The owner's view of an agent's picture library — the first surface that
	// shows what is in it rather than describing it in the agent's own words.
	T.HandleFunc("/api/agent-images", g(T.handleAgentImages))
	T.HandleFunc("/api/agent-images/raw", g(T.handleAgentImageRaw))
	T.HandleFunc("/api/agent-images/action", gw(T.handleAgentImageAction))
	// Fleet-wide guardrail review — read-only, so no gw() write wrapper.
	T.HandleFunc("/api/console/guardrail-blocks", g(T.handleConsoleGuardrails))
	T.HandleFunc("/api/console/monitors", g(T.handleConsoleMonitors))
	T.HandleFunc("/api/console/monitors/delete", gw(T.handleConsoleMonitorDelete))
	T.HandleFunc("/api/console/monitors/pause", gw(T.handleConsoleMonitorPause))
	T.HandleFunc("/api/console/monitors/resume", gw(T.handleConsoleMonitorResume))
	T.HandleFunc("/api/console/monitors/relink", gw(T.handleConsoleMonitorRelink))
	// "Move to…" (Surface: Cortex / Session / Background) — shared dynamic picker
	// source (cortex gated on the agent) + per-type in-place setters, no
	// delete+recreate.
	T.HandleFunc("/api/console/surface-options", g(T.handleConsoleSurfaceOptions))
	T.HandleFunc("/api/console/monitors/move", gw(T.handleConsoleMonitorMove))
	T.HandleFunc("/api/console/agents/move", gw(T.handleConsoleStandingMove))
	T.HandleFunc("/api/console/recurring/move", gw(T.handleConsoleRecurringMove))
	T.HandleFunc("/api/console/monitors/run", gw(T.handleConsoleMonitorRun))
	T.HandleFunc("/api/console/monitors/get", g(T.handleConsoleMonitorGet))
	T.HandleFunc("/api/console/monitors/update", gw(T.handleConsoleMonitorUpdate))
	T.HandleFunc("/api/console/agents/get", g(T.handleConsoleAgentGet))
	T.HandleFunc("/api/console/agents/update", gw(T.handleConsoleAgentUpdate))
	T.HandleFunc("/api/console/activity", g(T.handleConsoleActivity))
	T.HandleFunc("/api/console/activity/cancel", gw(T.handleConsoleActivityCancel))
	T.HandleFunc("/api/console/runs", g(T.handleConsoleRuns))
	T.HandleFunc("/api/console/run-detail", g(T.handleConsoleRunDetail))
	T.HandleFunc("/api/console/approvals", g(T.handleConsoleApprovals))
	T.HandleFunc("/api/console/permissions", g(T.handleConsolePermissions))
	T.HandleFunc("/api/console/permissions/policy", g(T.handleConsolePermissionPolicy))
	T.HandleFunc("/api/console/permissions/remove", g(T.handleConsolePermissionRemove))
	T.HandleFunc("/api/console/privileges", g(T.handleConsolePrivileges))
	T.HandleFunc("/api/console/approvals/approve", gw(T.handleApprovalApprove))
	T.HandleFunc("/api/console/approvals/always", gw(T.handleApprovalAlways))
	T.HandleFunc("/api/console/approvals/deny", gw(T.handleApprovalDeny))
	T.HandleFunc("/api/console/credential-update/apply", gw(T.handleCredentialUpdateApply))
	T.HandleFunc("/api/console/channel/clear", g(T.handleChannelClear))
	T.HandleFunc("/api/console/channel/compact", g(T.handleChannelCompact))
	T.HandleFunc("/api/console/channel/decommission", g(T.handleChannelDecommission))
	T.HandleFunc("/api/console/grants", g(T.handleConsoleGrants))
	T.HandleFunc("/api/console/grants/revoke", g(T.handleGrantRevoke))
	// Bridges — deployment-wide admin management of the credential-
	// polling bridges agents have created, regardless of owner. This
	// is the admin's enable/disable switch for a bridge: a paused
	// bridge stops polling AND agents can't resume it themselves
	// (bridge tools have no resume action; only these routes and the
	// owner's console do).
	T.HandleFunc("/api/console/bridges", g(T.handleConsoleBridges))
	T.HandleFunc("/api/console/bridges/pause", gw(T.handleConsoleBridgePause))
	T.HandleFunc("/api/console/bridges/resume", gw(T.handleConsoleBridgeResume))
	T.HandleFunc("/api/console/bridges/delete", gw(T.handleConsoleBridgeDelete))
	// Hook/unhook a bridge to a channel (Stage C: the UI equivalent of the
	// bridge tool's update action).
	T.HandleFunc("/api/console/bridges/set-channel", gw(T.handleConsoleBridgeSetChannel))
	// Channel picker for the Connect action — an owner's channels as
	// {id, label, desc}. owner is passed per-row (a bridge's own owner).
	T.HandleFunc("/api/console/bridge-channels", g(T.handleConsoleBridgeChannels))
	// Recent activity in a poll bridge's connected channel — the HistoryPanel
	// expand on the /bridges/ poll table.
	T.HandleFunc("/api/console/bridge-thread", g(T.handleConsoleBridgeThread))
	// Per-agent schedules rail — the agent's own event monitors + scheduled
	// runs + recurring tasks, so a schedule is visible within the agent it fires
	// (any agent, not just controllers).
	T.HandleFunc("/api/schedules", g(T.handleSchedules))
	// Making one, rather than only managing what an agent made
	// (console_machine_schedule.go).
	T.HandleFunc("/api/console/machine-options", g(T.handleConsoleMachineOptions))
	T.HandleFunc("/api/console/machine-schedule/create", g(T.handleConsoleMachineScheduleCreate))
	// Delete a recurring task (the `recurring` tool's session updates) from the
	// schedules rail. Owner-checked against the task payload before unscheduling.
	T.HandleFunc("/api/console/recurring", g(T.handleConsoleRecurring))
	T.HandleFunc("/api/console/recurring/run", gw(T.handleConsoleRecurringRun))
	T.HandleFunc("/api/console/recurring/delete", gw(T.handleConsoleRecurringDelete))
	T.HandleFunc("/api/console/recurring/relink", gw(T.handleConsoleRecurringRelink))
	T.HandleFunc("/api/console/recurring/resume", gw(T.handleConsoleRecurringResume))
	// Get one recurring task's editable fields (for the rail's edit modal) and
	// update its schedule in place (re-validate + reschedule, prompt preserved).
	T.HandleFunc("/api/console/recurring/get", g(T.handleConsoleRecurringGet))
	T.HandleFunc("/api/console/recurring/update", gw(T.handleConsoleRecurringUpdate))
	// Create a NEW recurring task from the Scheduler modal's "New recurring task"
	// button (agent + session supplied in the body; timing same shape as update).
	T.HandleFunc("/api/console/recurring/create", gw(T.handleConsoleRecurringCreate))
}

// handleSchedules returns an agent's scheduled runs + event monitors for the
// per-agent Schedules rail. Scoped "where it fires": monitors by WakeAgent,
// standing runs by AgentID (the agent that runs on the schedule) — matching
// introspect(section="schedules"). Each row embeds its own pause/resume/delete
// URLs so the rail JS stays generic across the two record types.
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
		return " · set up from " + name(manager)
	case manager:
		return " · runs as " + name(runner)
	}
	return ""
}
