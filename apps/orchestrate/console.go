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
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// cortexSessionID is the session id of a channel agent's persistent home
// thread. Per-agent so every channel agent has its own ongoing conversation.
// The client gets this value (per agent) from the host page, so it never
// hardcodes the scheme. (The retired Operator's pre-split "operator-thread"
// bridge was removed when the Operator seed was dropped — see
// dropLegacyOperator.)
func cortexSessionID(agentID string) string {
	return "channel:" + agentID
}

// resolveSurface maps a scheduled thing's Surface mode to the session its fire
// should land in, plus whether to surface it at all. `home` is the record's own
// home session — a monitor's WakeSession, a standing agent's ReportSessionID, or
// a recurring task's SessionID. Shared by monitors / standing / recurring so all
// three behave identically:
//
//	"" / "session" → the home session (or the cortex home thread if home is
//	                 empty — the legacy fallback so a fire still surfaces).
//	"cortex"       → the agent's cortex home thread.
//	"background"   → record=false: NO agent visibility (external delivery only).
//
// The home is never overwritten by a move, so switching back to "session" works.
func resolveSurface(surface, home, agentID string) (session string, record bool) {
	switch strings.TrimSpace(surface) {
	case "background":
		return "", false
	case "cortex":
		return cortexSessionID(agentID), true
	default: // "" / "session"
		if strings.TrimSpace(home) == "" {
			return cortexSessionID(agentID), true
		}
		return home, true
	}
}

// surfaceOptionsFor returns the "Move to…" picker options for an agent, gated on
// whether it has a cortex: Cortex is offered only when the agent has one. Shared
// by the monitor / standing / recurring move pickers.
func surfaceOptions(agentHasCortex bool) []struct {
	Value string `json:"value"`
	Label string `json:"label"`
} {
	type opt = struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	var out []opt
	if agentHasCortex {
		out = append(out, opt{Value: "cortex", Label: "Cortex home thread"})
	}
	out = append(out,
		opt{Value: "session", Label: "Session (where it was created)"},
		opt{Value: "background", Label: "Background (No Agent Visibility)"},
	)
	return out
}

// scheduleSurfaceDefault picks where a schedule REPORTS when nobody chose. An
// agent with a cortex has a standing thread built for exactly this — background
// work the user reads when they choose to, instead of fires interleaved into
// whatever conversation happened to author them — so a cortex agent defaults to
// "cortex" and everything else keeps the creating session. Applied on create AND
// on edit, which is why an explicit choice must be STORED rather than left empty:
// normalizeSurface keeps "session" as "session" so a deliberate move back is not
// re-defaulted to cortex by the next timing edit.
func scheduleSurfaceDefault(chosen string, hasCortex bool) string {
	if c := strings.TrimSpace(chosen); c != "" {
		return c
	}
	if hasCortex {
		return "cortex"
	}
	return ""
}

// surfaceDestLabel names a stored Surface mode in a sentence, for the confirmation
// the tool hands back and the model repeats to the user. Where a schedule reports
// is the one thing they need told, and "" (defaulted to the session) reads the
// same as an explicit "session".
func surfaceDestLabel(surface string) string {
	switch strings.TrimSpace(surface) {
	case "cortex":
		return "this agent's Cortex mind thread"
	case "background":
		return "no thread (background — the run happens, nothing is posted)"
	default:
		return "this session"
	}
}

// surfaceSuffix is the one-clause tail a console row adds so WHERE a schedule
// reports is visible without opening it — the destination is now defaulted per
// agent, so a row that says only its cadence hides the difference. Empty for the
// session, which is what the row's own context already implies.
func surfaceSuffix(surface string) string {
	switch strings.TrimSpace(surface) {
	case "cortex":
		return " · reports to cortex"
	case "background":
		return " · background (no agent visibility)"
	}
	return ""
}

// hasCortexThread reports whether this user's agent maintains a cortex thread.
// Tolerant by design: an unknown agent is simply "no cortex", so a caller
// defaulting a surface degrades to the session rather than erroring.
func hasCortexThread(user, agentID string) bool {
	if strings.TrimSpace(agentID) == "" {
		return false
	}
	a, ok := loadAgent(agentUserDB(RootDB, user), agentID)
	return ok && a.Cortex
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
					// Where to go to actually WATCH it. A background run has no
					// session to reopen — a scheduled fire is not a thread you
					// were in — so the destination is its own row on the
					// monitor rather than a page that does not exist.
					URL: "/monitor?run=" + url.QueryEscape(s.ID),
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

func (T *OrchestrateApp) handleSchedules(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []map[string]any{}
	for _, m := range ListEventMonitors(RootDB, user) {
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		// Bridges have their own app (and are push-oriented, not polled
		// schedules), so they're excluded from the unified Scheduler view.
		if isBridgeMonitor(m) {
			continue
		}
		kind := string(m.Kind)
		id := url.QueryEscape(m.Name)
		row := map[string]any{
			"name":           m.Name,
			"detail":         fmt.Sprintf("%s · every %ds", kind, m.IntervalSeconds),
			"paused":         m.Paused,
			"pause_url":      "api/console/monitors/pause?id=" + id,
			"resume_url":     "api/console/monitors/resume?id=" + id,
			"delete_url":     "api/console/monitors/delete?id=" + id,
			"category":       "monitor",
			"category_label": "Event monitors",
		}
		// Schedulable kinds (poll / http_poll / watch) get a click-to-edit-interval
		// modal; webhook monitors are push-only, so no edit affordance.
		if IsScheduledEventKind(m.Kind) {
			row["id"] = m.Name
			row["edit_action"] = "orchestrate_edit_monitor"
		}
		rows = append(rows, row)
	}
	for _, sa := range ListStandingAgents(RootDB, user) {
		// Scope to the CONTROLLER that created + manages the task (ReportAgentID) —
		// the same owner the "Enabled agents" card uses (handleConsoleAgents) — so a
		// task that RUNS as a sub-agent shows on the PARENT's Scheduler rail where
		// it's managed, not on the sub-agent that merely executes it. Fall back to
		// the runner AgentID for legacy records that carry no controller, so they
		// still surface somewhere.
		if !standingAgentOnRailOf(sa, agentID) {
			continue
		}
		// Say what it RUNS, not just that it is scheduled. A row reading
		// "scheduled run · every 24h" is the same sentence whether it fires
		// an agent or a pipeline, and the two behave differently enough
		// that the rail should not make somebody open it to find out.
		what := "scheduled run"
		switch {
		case sa.TargetsPipeline():
			what = "pipeline run"
			if def, ok := pipelineForUser(user, sa.PipelineID); ok {
				what = "pipeline · " + def.Name
				if def.Owner != "" && def.Owner != user {
					what += " (shared by " + def.Owner + ")"
				}
			}
		case sa.TargetsMachine():
			what = "machine run"
			if def, ok := machineForUser(user, sa.MachineID); ok {
				what = "machine · " + def.Name
				if def.Owner != "" && def.Owner != user {
					what += " (shared by " + def.Owner + ")"
				}
			}
		}
		id := url.QueryEscape(sa.Name)
		rows = append(rows, map[string]any{
			"name":           sa.Name,
			"detail":         what + " · " + StandingScheduleLabel(sa) + surfaceSuffix(sa.Surface) + standingRoleSuffix(udb, sa, agentID),
			"paused":         sa.Paused,
			"pause_url":      "api/console/agents/pause?id=" + id,
			"resume_url":     "api/console/agents/resume?id=" + id,
			"delete_url":     "api/console/agents/delete?id=" + id,
			"category":       "standing",
			"category_label": "Scheduled agents",
			"id":             sa.Name,
			"edit_action":    "orchestrate_edit_standing",
		})
	}
	// Recurring tasks (the `recurring` tool → per-session scheduled updates).
	// Delete-only: these have no pause concept, so the row omits pause/resume
	// URLs and the rail renders just name + detail + ×. The label is the prompt's
	// first line (they carry no user-set name). Scoped to the agent that runs
	// them (AgentID), matching the monitor / standing-run scoping above.
	for _, rt := range listAgentRecurringTasks(user, agentID) {
		label := firstLineLabel(rt.Payload.Prompt)
		if label == "" {
			label = "recurring task"
		}
		rows = append(rows, map[string]any{
			"name":           label,
			"detail":         recurringDetail(rt.Payload) + surfaceSuffix(rt.Payload.Surface),
			"paused":         false,
			"delete_url":     "api/console/recurring/delete?id=" + url.QueryEscape(rt.TaskID),
			"id":             rt.TaskID,
			"edit_action":    "orchestrate_edit_schedule",
			"category":       "recurring",
			"category_label": "Recurring tasks",
		})
	}
	writeJSON(w, rows)
}

type consoleRecurringRow struct {
	// Recurring tasks carry no user-set name — the label is the prompt's first
	// line (matching the Schedules rail). It renders as the card title.
	Name    string `json:"name"`
	Cadence string `json:"cadence"`            // human cadence ("recurring · every 30m")
	Fires   string `json:"fires,omitempty"`    // "<fired> / <cap>" so far
	NextRun string `json:"next_run,omitempty"` // RFC3339 next fire (matches consoleAgentRow)
	State   string `json:"state,omitempty"`    // visible only when broken ("⚠ needs relink — …")
	ID      string `json:"_id"`                // hidden; row-action target (the scheduler task id)
	Broken  bool   `json:"_broken,omitempty"`  // hidden gate (Delete-only on a broken row)
}

// handleConsoleRecurring lists the owner's recurring tasks (the `recurring` tool
// → per-session scheduled updates) for the Recurring-tasks nav card view — the
// status-card sibling of Enabled agents / Event monitors. Scoped to the agent
// that runs them (AgentID), matching the other two panes. Recurring tasks have
// no pause concept, so the only row action is Delete.
func (T *OrchestrateApp) handleConsoleRecurring(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []consoleRecurringRow{}
	for _, rt := range listAgentRecurringTasks(user, agentID) {
		label := firstLineLabel(rt.Payload.Prompt)
		if label == "" {
			label = "recurring task"
		}
		// Uncapped continuous-random tasks report effectiveMaxFires as MaxInt32
		// (no real ceiling) — show just the count run so far, not "3 / 2147483647".
		fires := fmt.Sprintf("%d fired", rt.Payload.FireCount)
		if cap := rt.Payload.effectiveMaxFires(); cap < math.MaxInt32 {
			fires = fmt.Sprintf("%d / %d fired", rt.Payload.FireCount, cap)
		}
		row := consoleRecurringRow{
			Name:    label,
			Cadence: recurringDetail(rt.Payload) + surfaceSuffix(rt.Payload.Surface),
			Fires:   fires,
			NextRun: rt.RunAt,
			ID:      rt.TaskID,
		}
		if rt.Payload.Broken {
			row.Broken = true
			row.State = brokenStateLabel(rt.Payload.BrokenReason)
			row.NextRun = "" // parked: the dormant re-check isn't a real next run
		}
		rows = append(rows, row)
	}
	writeJSON(w, rows)
}

// handleConsoleRecurringDelete removes a recurring task by scheduler id from the
// per-agent schedules rail. The scheduler bucket is global and keyed by opaque
// UUID, so ownership is enforced by matching the task's payload username (via
// listAgentRecurringTasks) before unscheduling — a user can't cancel another
// user's task by guessing its id.
func (T *OrchestrateApp) handleConsoleRecurringDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			UnscheduleTask(id)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
}

// handleConsoleAgentOptions lists the owner's agents as picker options
// ({value:id,label:name}) — the shared source for the "Relink" row action across
// the monitors / enabled-agents / recurring panes.
func (T *OrchestrateApp) handleConsoleAgentOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type opt struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	opts := []opt{}
	// A relink picker for a schedule that runs a PIPELINE must offer
	// pipelines. The picker source is one URL for the whole column, so it
	// is told which row it is choosing for (?row=, added by openRowPicker)
	// and answers for that row's target kind. Offering agents there would
	// be worse than offering nothing: every choice in the list gets
	// refused by the relink handler.
	if row := strings.TrimSpace(r.URL.Query().Get("row")); row != "" {
		if sa, found := GetStandingAgent(RootDB, user, row); found && sa.TargetsMachine() {
			// A machine schedule relinks to a machine, and only to one that
			// can be run this way: offering a conversational machine would
			// be offering a choice the relink handler then refuses.
			for _, d := range ListMachineDefs(UserDB(T.DB, user), user) {
				if !d.Unattended {
					continue
				}
				label := strings.TrimSpace(d.Name)
				if label == "" {
					label = d.ID
				}
				opts = append(opts, opt{Value: d.ID, Label: label})
			}
			// A schedule can fire a machine somebody shared, so relinking one
			// has to be able to CHOOSE it. Labelled with the sharer: two people
			// can name a machine the same thing, and a picker that cannot tell
			// them apart is how a schedule gets pointed at the wrong one.
			for _, sm := range sharedMachinesFor(user) {
				if !sm.Def.Unattended {
					continue
				}
				label := strings.TrimSpace(sm.Def.Name)
				if label == "" {
					label = sm.Def.ID
				}
				opts = append(opts, opt{Value: sm.Def.ID, Label: label + " (shared by " + sm.Owner + ")"})
			}
			writeJSON(w, opts)
			return
		}
		if sa, found := GetStandingAgent(RootDB, user, row); found && sa.TargetsPipeline() {
			for _, d := range ListPipelineDefs(UserDB(T.DB, user), user) {
				label := strings.TrimSpace(d.Name)
				if label == "" {
					label = d.ID
				}
				opts = append(opts, opt{Value: d.ID, Label: label})
			}
			// A schedule can fire a pipeline somebody shared, so relinking one
			// has to be able to CHOOSE it. Labelled with the sharer: two people
			// can name a pipeline the same thing, and a picker that cannot tell
			// them apart is how a schedule gets pointed at the wrong one.
			for _, sp := range sharedPipelinesFor(user) {
				label := strings.TrimSpace(sp.Def.Name)
				if label == "" {
					label = sp.Def.ID
				}
				opts = append(opts, opt{Value: sp.Def.ID, Label: label + " (shared by " + sp.Owner + ")"})
			}
			writeJSON(w, opts)
			return
		}
	}
	// with_default=1 (the monitor relink picker only) leads with a "use the
	// deployment default" choice so relinking a monitor never REQUIRES naming a
	// specific agent — you pick one only when the persona matters. Standing/
	// recurring relink omit the param: those genuinely need a real target.
	if r.URL.Query().Get("with_default") == "1" {
		opts = append(opts, opt{Value: "__default__", Label: "Default agent (recommended)"})
	}
	for _, a := range listAgents(UserDB(T.DB, user), user) {
		// Relink targets are things a person may point work at: hidden
		// app-internal agents, clone-only templates, and retired seeds are not.
		if fleetHidden(a.ID) {
			continue
		}
		label := strings.TrimSpace(a.Name)
		if label == "" {
			label = a.ID
		}
		opts = append(opts, opt{Value: a.ID, Label: label})
	}
	writeJSON(w, opts)
}

// handleConsoleMonitorRelink re-points a broken monitor's wake agent at a live
// one and clears the broken flag — but LEAVES it paused, so recovery finishes
// with an explicit Resume (which re-checks the now-healthy dependency).
func (T *OrchestrateApp) handleConsoleMonitorRelink(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	newAgent := strings.TrimSpace(r.URL.Query().Get("value"))
	// The agent is OPTIONAL for a monitor: an empty WakeAgent resolves to the
	// deployment default channel agent, and a direct-delivery monitor doesn't use
	// the agent for delivery at all — so "" / the __default__ sentinel means
	// "just use the default", and the picker no longer forces a specific choice.
	// A real id is still validated + used when the user DOES want a specific
	// persona (the case that actually matters, notify=channel).
	useDefault := newAgent == "" || newAgent == "__default__"
	if !useDefault {
		if _, ok := loadAgent(UserDB(T.DB, user), newAgent); !ok {
			http.Error(w, "no such agent", http.StatusBadRequest)
			return
		}
	}
	m, found := GetEventMonitor(RootDB, user, name)
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	if useDefault {
		m.WakeAgent = "" // deployment default channel agent
	} else {
		m.WakeAgent = newAgent
	}
	m.Broken = false
	m.BrokenReason = ""
	// Paused stays true on purpose — the user resumes explicitly.
	SaveEventMonitor(RootDB, m)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleSurfaceOptions is the SHARED "Move to…" picker source for
// monitors / standing agents / recurring: it returns Cortex/Session/Background,
// with Cortex offered only when the pane's agent has a cortex (?agent=<id>).
func (T *OrchestrateApp) handleConsoleSurfaceOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	hasCortex := false
	if a, ok := loadAgent(agentUserDB(RootDB, user), agentID); ok && a.Cortex {
		hasCortex = true
	}
	writeJSON(w, surfaceOptions(hasCortex))
}

// normalizeSurface maps a picker value to a stored Surface mode. "session"
// stores as "session" — NOT as "" — even though resolveSurface treats the two
// identically at fire time: empty means "nobody chose", which is what
// scheduleSurfaceDefault fills in with the cortex on a cortex agent. Storing the
// word is what makes a deliberate move back to the session survive the next
// edit. An empty picker value is still accepted (a client sending nothing means
// the default) and stays empty.
func normalizeSurface(val string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "":
		return "", true
	case "session":
		return "session", true
	case "cortex":
		return "cortex", true
	case "background":
		return "background", true
	}
	return "", false
}

// handleConsoleMonitorMove sets a monitor's Surface in place — moves the whole
// monitor (card + rail badge + wake all follow) without a delete+recreate. The
// home session (WakeSession) is left intact, so Session always works to return.
func (T *OrchestrateApp) handleConsoleMonitorMove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	surface, valid := normalizeSurface(r.URL.Query().Get("value"))
	if !valid {
		http.Error(w, "move target must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	m, found := GetEventMonitor(RootDB, user, name)
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	m.Surface = surface
	SaveEventMonitor(RootDB, m)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleStandingMove sets a standing agent's Surface in place — same
// Cortex/Session/Background modes as monitors; its per-run report follows.
func (T *OrchestrateApp) handleConsoleStandingMove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	surface, valid := normalizeSurface(r.URL.Query().Get("value"))
	if !valid {
		http.Error(w, "move target must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	sa.Surface = surface
	SaveStandingAgent(RootDB, sa)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleRecurringMove sets a recurring task's Surface in place. Recurring
// lives only as a scheduler entry, so this re-arms it with the updated payload
// (mirrors relink). The home session (SessionID) is left intact.
func (T *OrchestrateApp) handleConsoleRecurringMove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	surface, valid := normalizeSurface(r.URL.Query().Get("value"))
	if !valid {
		http.Error(w, "move target must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		UnscheduleTask(id)
		p := rt.Payload
		p.Surface = surface
		p.LastActive = time.Now().Format(time.RFC3339)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(user)))
		if err != nil {
			http.Error(w, "moved but couldn't reschedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, r)
}

// handleConsoleAgentRelink re-points a broken standing agent at a live target
// agent and clears broken (leaves paused — an explicit Resume finishes recovery).
func (T *OrchestrateApp) handleConsoleAgentRelink(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	value := strings.TrimSpace(r.URL.Query().Get("value"))
	// Load the schedule FIRST: what the value has to be depends on what
	// this schedule runs. Validating it as an agent before knowing that
	// is how a pipeline schedule ends up with an AgentID set alongside
	// its PipelineID — the one state ValidateTarget exists to refuse,
	// where whichever the runner checks first wins forever.
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	if sa.TargetsMachine() {
		// The same resolver the picker offered from, so a choice the picker
		// listed cannot be refused here — an offer the handler rejects is
		// worse than no offer.
		def, ok := machineForUser(user, value)
		if !ok {
			http.Error(w, "no such machine", http.StatusBadRequest)
			return
		}
		// The same two refusals the runner makes, made HERE instead, so a
		// repair that would fail at 3am fails now with somebody reading it.
		if !def.Unattended {
			http.Error(w, "that machine converses rather than runs: a schedule fires with nobody there to answer a step that waits", http.StatusBadRequest)
			return
		}
		if probs := def.Problems(); len(probs) > 0 {
			http.Error(w, "that machine will not run yet: "+probs[0], http.StatusBadRequest)
			return
		}
		sa.MachineID = def.ID
		sa.Broken = false
		sa.BrokenReason = ""
		SaveStandingAgent(RootDB, sa)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if sa.TargetsPipeline() {
		// pipelineForUser, not the own-store load: the picker above offers
		// pipelines somebody shared, and this refused every one of them.
		def, ok := pipelineForUser(user, value)
		if !ok {
			http.Error(w, "no such pipeline", http.StatusBadRequest)
			return
		}
		// A pipeline that would not run is not a repair. Refusing here
		// keeps the schedule broken-and-visible rather than pointing it at
		// something that fails on its next fire.
		if err := def.Validate(); err != nil {
			http.Error(w, "that pipeline would not run: "+err.Error(), http.StatusBadRequest)
			return
		}
		sa.PipelineID = def.ID
		sa.Broken = false
		sa.BrokenReason = ""
		SaveStandingAgent(RootDB, sa)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, ok := loadAgent(UserDB(T.DB, user), value); !ok {
		http.Error(w, "no such agent", http.StatusBadRequest)
		return
	}
	sa.AgentID = value
	sa.Broken = false
	sa.BrokenReason = ""
	SaveStandingAgent(RootDB, sa)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleRecurringRelink re-points a parked (broken) recurring task at a
// live agent and puts it straight back on its real cadence. Recurring has no
// pause/resume concept (unlike monitors/standing), so relink resumes it
// directly; LastActive is refreshed so the idle guard doesn't immediately reap a
// just-relinked task.
func (T *OrchestrateApp) handleConsoleRecurringRelink(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	newAgent := strings.TrimSpace(r.URL.Query().Get("value"))
	if _, ok := loadAgent(UserDB(T.DB, user), newAgent); !ok {
		http.Error(w, "no such agent", http.StatusBadRequest)
		return
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		UnscheduleTask(id)
		p := rt.Payload
		p.AgentID = newAgent
		p.Broken = false
		p.BrokenReason = ""
		p.LastActive = time.Now().Format(time.RFC3339)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(user)))
		if err != nil {
			http.Error(w, "relinked but couldn't schedule: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
}

// handleConsoleRecurringRun fires a recurring task's prompt once immediately from
// the Recurring-tasks pane's "Run now" button — a one-off manual test that does
// NOT touch the schedule (RunOrchestrateUpdateNow fires without rescheduling or
// consuming the fire budget). Ownership is enforced inside RunOrchestrateUpdateNow
// (payload-username match), and we pre-check existence here to return a clean 404.
// The fire runs off-request in a goroutine with a background context (it replays a
// full agent-loop turn and must outlive the response); the reply is appended to
// the task's thread and the run ledger updates on reload. id = the scheduler task
// id. Returns 202 once the fire is launched.
func (T *OrchestrateApp) handleConsoleRecurringRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	found := false
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "recurring task not found", http.StatusNotFound)
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				Log("[orchestrate/console] run-now panicked for recurring task %s: %v", id, rec)
			}
		}()
		if err := RunOrchestrateUpdateNow(context.Background(), user, id); err != nil {
			Log("[orchestrate/console] run-now failed for recurring task %s: %v", id, err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// handleConsoleRecurringGet returns one recurring task's editable fields for the
// rail's edit modal. Owner-checked via listAgentRecurringTasks. GET ?id=<id>.
func (T *OrchestrateApp) handleConsoleRecurringGet(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID != id {
			continue
		}
		p := rt.Payload
		pattern := p.Pattern
		if pattern == "" {
			pattern = RecurringFixed
		}
		writeJSON(w, map[string]any{
			"id":               rt.TaskID,
			"name":             recurringName(p),
			"prompt":           p.Prompt,
			"pattern":          pattern,
			"interval_minutes": p.IntervalSeconds / 60,
			"times_per_day":    p.TimesPerDay,
			"min_gap_minutes":  p.MinGapSeconds / 60,
			"max_gap_minutes":  p.MaxGapSeconds / 60,
			"has_window":       p.HasWindow,
			"active_from":      fmtHHMM(p.WindowFromMin),
			"active_to":        fmtHHMM(p.WindowToMin),
			"max_fires":        p.MaxFires,
			"fire_count":       p.FireCount,
			"cadence":          recurringDetail(p),
		})
		return
	}
	http.Error(w, "recurring task not found", http.StatusNotFound)
}

// handleConsoleRecurringUpdate edits a recurring task's schedule in place: rebuild
// the spec from the stored task (session / agent / prompt preserved) with the
// posted timing, unschedule the old, schedule the edited one. On a validation
// error the original is restored so a bad edit never destroys the task. POST
// ?id=<id> with a JSON timing body.
func (T *OrchestrateApp) handleConsoleRecurringUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	var found *orchUpdatePayload
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == id {
			p := rt.Payload
			found = &p
			break
		}
	}
	if found == nil {
		http.Error(w, "recurring task not found", http.StatusNotFound)
		return
	}
	var body struct {
		Pattern         string `json:"pattern"`
		IntervalMinutes int    `json:"interval_minutes"`
		TimesPerDay     int    `json:"times_per_day"`
		MinGapMinutes   int    `json:"min_gap_minutes"`
		MaxGapMinutes   int    `json:"max_gap_minutes"`
		ActiveFrom      string `json:"active_from"`
		ActiveTo        string `json:"active_to"`
		MaxFires        int    `json:"max_fires"`
		// Pointers so "field omitted" (nil = preserve the stored value) is
		// distinguishable from "sent empty". Prompt is required, so an empty
		// prompt preserves too; Name may legitimately be cleared (empty falls
		// back to the prompt's first line at render).
		Prompt *string `json:"prompt"`
		Name   *string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Directive + name are editable now (not just timing). Base = the stored
	// values; a provided prompt (non-empty; prompt is required) or name overrides.
	// Computed into LOCALS, not mutated onto found — the rejected-edit restore path
	// below re-schedules *found and must keep the original directive/name intact.
	prompt, name := found.Prompt, found.Name
	if body.Prompt != nil {
		if p := strings.TrimSpace(*body.Prompt); p != "" {
			prompt = p
		}
	}
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	spec := RecurringSpec{
		SessionID: found.SessionID,
		AgentID:   found.AgentID,
		Username:  found.Username,
		Prompt:    prompt,
		Name:      name,
		// An edit re-schedules the task, so the destination has to travel with
		// it or a retime would silently send the reports somewhere else. A
		// surface the user actually chose is preserved verbatim; an unchosen one
		// takes the agent's default (its cortex, when it has one).
		Surface:         scheduleSurfaceDefault(found.Surface, hasCortexThread(user, found.AgentID)),
		FireCount:       found.FireCount, // preserve run history across an edit (don't reset the budget)
		CreatedAt:       found.CreatedAt, // keep the original creation time, not "now"
		Pattern:         strings.ToLower(strings.TrimSpace(body.Pattern)),
		IntervalSeconds: body.IntervalMinutes * 60,
		TimesPerDay:     body.TimesPerDay,
		MinGapSeconds:   body.MinGapMinutes * 60,
		MaxGapSeconds:   body.MaxGapMinutes * 60,
		MaxFires:        body.MaxFires,
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	from := strings.TrimSpace(body.ActiveFrom)
	to := strings.TrimSpace(body.ActiveTo)
	if (from == "") != (to == "") {
		http.Error(w, "active_from and active_to must be set together", http.StatusBadRequest)
		return
	}
	if from != "" {
		fMin, ferr := parseHHMM(from)
		if ferr != nil {
			http.Error(w, ferr.Error(), http.StatusBadRequest)
			return
		}
		tMin, terr := parseHHMM(to)
		if terr != nil {
			http.Error(w, terr.Error(), http.StatusBadRequest)
			return
		}
		spec.HasWindow = true
		spec.WindowFromMin = fMin
		spec.WindowToMin = tMin
	}
	// Remove the old first (so the per-session cap has room for the replacement).
	UnscheduleTask(id)
	newID, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		// Restore the original so a rejected edit doesn't destroy the task.
		if next, cerr := computeNextFire(found, time.Now().In(UserLocation(found.Username))); cerr == nil {
			_, _ = ScheduleTask(OrchestrateScheduledUpdateKind, *found, next)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"id": newID})
}

// handleConsoleRecurringCreate creates a NEW recurring task from the Scheduler
// modal's "New recurring task" button. Unlike update (which edits an existing
// task by id), this needs a target: agent_id (the rail's current agent) and
// session_id (the open thread), defaulting to the agent's home thread when none
// is open so an agent-level task always lands somewhere sensible. The per-session
// active-task cap is enforced inside ScheduleOrchestrateUpdate. POST JSON
// {agent_id, session_id, name, prompt, pattern, timing…}.
func (T *OrchestrateApp) handleConsoleRecurringCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AgentID         string `json:"agent_id"`
		SessionID       string `json:"session_id"`
		Name            string `json:"name"`
		Prompt          string `json:"prompt"`
		Pattern         string `json:"pattern"`
		IntervalMinutes int    `json:"interval_minutes"`
		TimesPerDay     int    `json:"times_per_day"`
		MinGapMinutes   int    `json:"min_gap_minutes"`
		MaxGapMinutes   int    `json:"max_gap_minutes"`
		ActiveFrom      string `json:"active_from"`
		ActiveTo        string `json:"active_to"`
		MaxFires        int    `json:"max_fires"`
		// Where its fires report — "" (let the agent decide), "session",
		// "cortex", or "background". The modal doesn't offer it today; the
		// field is here so a client that wants to choose can, and so the
		// default below is the only thing an omission relies on.
		Surface string `json:"surface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return
	}
	if _, ok := loadAgent(UserDB(T.DB, user), agentID); !ok {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "directive (prompt) is required", http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		sessionID = cortexSessionID(agentID) // no open thread → attach to the agent's home thread
	}
	surface, valid := normalizeSurface(body.Surface)
	if !valid {
		http.Error(w, "surface must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	// Unchosen → the agent's default: its cortex when it has one. The home
	// session stays the open thread either way, so Move-to → Session returns it.
	surface = scheduleSurfaceDefault(surface, hasCortexThread(user, agentID))
	spec := RecurringSpec{
		SessionID:       sessionID,
		Surface:         surface,
		AgentID:         agentID,
		Username:        user,
		Prompt:          strings.TrimSpace(body.Prompt),
		Name:            strings.TrimSpace(body.Name),
		Pattern:         strings.ToLower(strings.TrimSpace(body.Pattern)),
		IntervalSeconds: body.IntervalMinutes * 60,
		TimesPerDay:     body.TimesPerDay,
		MinGapSeconds:   body.MinGapMinutes * 60,
		MaxGapSeconds:   body.MaxGapMinutes * 60,
		MaxFires:        body.MaxFires,
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	from := strings.TrimSpace(body.ActiveFrom)
	to := strings.TrimSpace(body.ActiveTo)
	if (from == "") != (to == "") {
		http.Error(w, "active_from and active_to must be set together", http.StatusBadRequest)
		return
	}
	if from != "" {
		fMin, ferr := parseHHMM(from)
		if ferr != nil {
			http.Error(w, ferr.Error(), http.StatusBadRequest)
			return
		}
		tMin, terr := parseHHMM(to)
		if terr != nil {
			http.Error(w, terr.Error(), http.StatusBadRequest)
			return
		}
		spec.HasWindow = true
		spec.WindowFromMin = fMin
		spec.WindowToMin = tMin
	}
	newID, err := ScheduleOrchestrateUpdate(spec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Same pre-flight the recurring() tool reports, for the human-authored path:
	// what this task would be refused when it fires unattended. Advisory — the
	// task is created either way, and an empty warning means it's clear.
	resp := map[string]any{"id": newID}
	if findings := T.PreflightAutonomous(spec.Username, spec.AgentID); len(findings) > 0 {
		resp["warnings"] = findings
		resp["warning"] = PreflightSummary(findings)
	}
	writeJSON(w, resp)
}

// handleConsoleMonitorGet returns an event monitor's editable schedule for the
// Scheduler edit modal. Only scheduled kinds (poll / http_poll / watch) carry an
// interval; a webhook monitor is push-triggered and reports schedulable=false.
func (T *OrchestrateApp) handleConsoleMonitorGet(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"name":             m.Name,
		"kind":             m.Kind,
		"interval_seconds": m.IntervalSeconds,
		"interval_minutes": m.IntervalSeconds / 60,
		"schedulable":      IsScheduledEventKind(m.Kind),
		"paused":           m.Paused,
	})
}

// handleConsoleMonitorUpdate edits an event monitor's poll interval in place and
// re-arms it. Rejected for webhook (push-only) monitors. A paused monitor keeps
// its new interval persisted without re-arming (it applies on resume). POST
// ?id=<name> with {interval_minutes} (or {interval_seconds}).
func (T *OrchestrateApp) handleConsoleMonitorUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	if !IsScheduledEventKind(m.Kind) {
		http.Error(w, "this monitor is push-triggered — it has no schedule to edit", http.StatusBadRequest)
		return
	}
	var body struct {
		IntervalSeconds int `json:"interval_seconds"`
		IntervalMinutes int `json:"interval_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	secs := body.IntervalSeconds
	if secs == 0 && body.IntervalMinutes > 0 {
		secs = body.IntervalMinutes * 60
	}
	if secs < 5 {
		http.Error(w, "interval too small — minimum 5 seconds", http.StatusBadRequest)
		return
	}
	m.IntervalSeconds = secs
	if m.Paused {
		SaveEventMonitor(RootDB, m)
	} else if err := ScheduleEventMonitor(RootDB, m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleAgentGet returns a standing agent's editable schedule for the
// Scheduler edit modal.
func (T *OrchestrateApp) handleConsoleAgentGet(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sa, found := GetStandingAgent(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	startAt := ""
	if !sa.StartAt.IsZero() {
		startAt = sa.StartAt.Format(time.RFC3339)
	}
	writeJSON(w, map[string]any{
		"name":             sa.Name,
		"mission":          sa.Mission,
		"cron":             sa.Cron,
		"interval_seconds": sa.IntervalSeconds,
		"interval_minutes": sa.IntervalSeconds / 60,
		"start_at":         startAt,
		"schedule_label":   StandingScheduleLabel(sa),
		"paused":           sa.Paused,
	})
}

// handleConsoleAgentUpdate edits a standing agent's schedule (a cron spec OR an
// interval) in place and re-arms it. Cron takes precedence when set, matching
// StandingAgent semantics. A bad schedule (e.g. unparseable cron) is rejected
// and the original restored so an edit never strands the agent. POST ?id=<name>
// with {cron} and/or {interval_minutes}.
func (T *OrchestrateApp) handleConsoleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sa, found := GetStandingAgent(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	var body struct {
		Cron            string `json:"cron"`
		IntervalMinutes int    `json:"interval_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	cron := strings.TrimSpace(body.Cron)
	secs := body.IntervalMinutes * 60
	if cron == "" && secs <= 0 {
		http.Error(w, "set a cron schedule or an interval (minutes)", http.StatusBadRequest)
		return
	}
	prevCron, prevInterval, prevSurface := sa.Cron, sa.IntervalSeconds, sa.Surface
	if cron != "" {
		sa.Cron, sa.IntervalSeconds = cron, 0
	} else {
		sa.Cron, sa.IntervalSeconds = "", secs
	}
	// A destination nobody chose takes the agent's default on edit, same as on
	// create: a cortex controller reads its scheduled runs in its cortex. An
	// explicit choice (stored, including "session") is left alone.
	sa.Surface = scheduleSurfaceDefault(sa.Surface, hasCortexThread(user, sa.ReportAgentID))
	var err error
	if sa.Paused {
		SaveStandingAgent(RootDB, sa)
	} else {
		err = ScheduleStandingAgent(RootDB, sa)
	}
	if err != nil {
		// Restore so a rejected edit doesn't strand the agent.
		sa.Cron, sa.IntervalSeconds, sa.Surface = prevCron, prevInterval, prevSurface
		if sa.Paused {
			SaveStandingAgent(RootDB, sa)
		} else {
			_ = ScheduleStandingAgent(RootDB, sa)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// channelLabelForRow returns the friendly name of a bridge's target channel
// for the console row, or "" when it has none (wakes the agent's own thread).
func channelLabelForRow(m EventMonitor) string {
	if ch := strings.TrimSpace(m.WakeChannel); ch != "" {
		if c, ok := GetChannel(RootDB, m.Owner, ch); ok && strings.TrimSpace(c.Name) != "" {
			return c.Name
		}
		return ch
	}
	return ""
}

// handleConsoleBridgeSetChannel hooks the bridge (owner+name) to a channel, or
// detaches it when channel_id is empty. Reuses the same shared logic the bridge
// tool's update action calls, so UI and tool behave identically.
func (T *OrchestrateApp) handleConsoleBridgeSetChannel(w http.ResponseWriter, r *http.Request) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	if channelID == "" {
		// Also accept it in the JSON body (ActionList posts {id} → channel_id).
		var body struct {
			ChannelID string `json:"channel_id"`
			ID        string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		channelID = strings.TrimSpace(body.ChannelID)
		if channelID == "" {
			channelID = strings.TrimSpace(body.ID)
		}
	}
	if _, err := setBridgeChannel(m.Owner, m.Name, channelID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleBridgeChannels lists a given owner's channels as {id,label,desc}
// for the Connect picker. A bridge can deliver into ANY of the owner's channels
// (it's a delivery target, not an exclusive transport binding), so — unlike the
// messaging Connect picker — occupied channels are included.
func (T *OrchestrateApp) handleConsoleBridgeChannels(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		if u, _, ok := RequireUser(w, r, T.DB); ok {
			owner = u
		} else {
			return
		}
	}
	udb := UserDB(T.DB, owner)
	rows := []map[string]any{}
	for _, ch := range ListChannels(RootDB, owner) {
		label := ch.Name
		if label == "" {
			label = ch.ID
		}
		agentName := ch.AgentID
		if a, ok := loadAgent(udb, ch.AgentID); ok && strings.TrimSpace(a.Name) != "" {
			agentName = a.Name
		}
		rows = append(rows, map[string]any{
			"id":    ch.ID,
			"label": label,
			"desc":  "Agent: " + agentName,
		})
	}
	writeJSON(w, rows)
}

// handleConsoleBridges lists every owner's bridges (watch-kind event
// monitors created by the bridge tool) for the Admin > Bridges table.
// Admin-gated at the route (adminGated), so cross-owner listing is
// deliberate — the admin governs all bridges, not just their own.
func (T *OrchestrateApp) handleConsoleBridges(w http.ResponseWriter, r *http.Request) {
	rows := []map[string]any{}
	for _, m := range ListAllEventMonitors(RootDB) {
		if !isBridgeMonitor(m) {
			continue
		}
		state := "active"
		if m.Paused {
			state = "paused"
		}
		lastFired := ""
		if !m.LastFired.IsZero() {
			lastFired = m.LastFired.Local().Format("Jan 2 3:04 PM")
		}
		// Destination: a channel target (Stage B) delivers into that channel's
		// conversation; otherwise the alert wakes the agent's own thread.
		dest := fmt.Sprintf("wake %q", m.WakeAgent)
		if ch := strings.TrimSpace(m.WakeChannel); ch != "" {
			label := ch
			if c, ok := GetChannel(RootDB, m.Owner, ch); ok && strings.TrimSpace(c.Name) != "" {
				label = c.Name
			}
			dest = fmt.Sprintf("→ channel %q", label)
		}
		rows = append(rows, map[string]any{
			"name":         m.Name,
			"owner":        m.Owner,
			"credential":   strings.TrimPrefix(m.ToolName, bridgeCredToolPrefix),
			"detail":       fmt.Sprintf("every %ds: %v %s", m.IntervalSeconds, m.ToolArgs["url"], dest),
			"state":        state,
			"last_fired":   lastFired,
			"_paused":      m.Paused,
			"_connected":   strings.TrimSpace(m.WakeChannel) != "",
			"channel_name": channelLabelForRow(m),
		})
	}
	writeJSON(w, rows)
}

// handleConsoleBridgeThread returns the recent messages of the channel a poll
// bridge delivers into, so the /bridges/ poll table can SHOW what's landed in
// that conversation (the HistoryPanel expand). Resolves the bridge → its target
// channel → the session that channel's inbound actually runs in (the cortex
// thread for a dedicated cortex agent, else the per-source channel session, via
// effectiveChannelSession — the SAME resolver delivery uses, so the view can't
// diverge from where the messages actually land). Emits [] (not an error) when
// the bridge targets no channel or nothing has landed yet.
func (T *OrchestrateApp) handleConsoleBridgeThread(w http.ResponseWriter, r *http.Request) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	out := []ChannelLine{}
	if wc := strings.TrimSpace(m.WakeChannel); wc != "" {
		if ch, found := GetChannel(RootDB, m.Owner, wc); found && ch.AgentID != "" {
			sid := T.effectiveChannelSession(m.Owner, ch.AgentID, ChannelSessionKey(ch, "bridge:"+m.Name))
			if sess, ok := loadChatSession(UserDB(T.DB, m.Owner), ch.AgentID, sid); ok {
				out = channelLinesFromMessages(sess.Messages, 50)
			}
		}
	}
	writeJSON(w, out)
}

// channelLinesFromMessages projects a session's stored turns into the
// HistoryPanel row shape (role / sender / text / timestamp), skipping empty and
// hidden messages, keeping the last `limit` (0 = all). Sender comes from
// ReportFrom, which channel inbound cards carry (e.g. "bridge:<name>").
func channelLinesFromMessages(msgs []ChatMessage, limit int) []ChannelLine {
	out := []ChannelLine{}
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Content)
		if text == "" || msg.Hidden {
			continue
		}
		ts := ""
		if !msg.Created.IsZero() {
			ts = msg.Created.Local().Format("Jan 2 3:04 PM")
		}
		out = append(out, ChannelLine{Role: msg.Role, Sender: strings.TrimSpace(msg.ReportFrom), Text: text, Timestamp: ts})
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// consoleBridgeMonitor resolves the bridge a row action targets from
// ?owner=&name=. Returns ok=false after writing the error response.
func (T *OrchestrateApp) consoleBridgeMonitor(w http.ResponseWriter, r *http.Request) (EventMonitor, bool) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	m, found := GetEventMonitor(RootDB, owner, name)
	if !found || !isBridgeMonitor(m) {
		http.Error(w, "no such bridge", http.StatusNotFound)
		return EventMonitor{}, false
	}
	return m, true
}

func (T *OrchestrateApp) setConsoleBridgePaused(w http.ResponseWriter, r *http.Request, paused bool) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	m.Paused = paused
	if paused {
		if m.SchedulerID != "" {
			UnscheduleTask(m.SchedulerID)
			m.SchedulerID = ""
			m.NextCheck = time.Time{}
		}
		SaveEventMonitor(RootDB, m)
	} else {
		SaveEventMonitor(RootDB, m)
		_ = ScheduleEventMonitor(RootDB, m)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleBridgePause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleBridgePaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleBridgeResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleBridgePaused(w, r, false)
}

func (T *OrchestrateApp) handleConsoleBridgeDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := T.consoleBridgeMonitor(w, r)
	if !ok {
		return
	}
	if m.SchedulerID != "" {
		UnscheduleTask(m.SchedulerID)
	}
	DeleteEventMonitor(RootDB, m.Owner, m.Name)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleGrants lists the owner's STANDING authorizations — the "Always
// allow" grants that let future delegations/messages skip the approval queue.
// Distinct from the pending queue (/api/console/approvals): those are one-time
// and vanish once acted on; these persist until revoked. Each row carries a
// "_id" of "<kind>:<target>" the Revoke action round-trips. GET only.
func (T *OrchestrateApp) handleConsoleGrants(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, agent := range ListDelegationPreAuthorizations(RootDB, user) {
		out = append(out, map[string]any{"_id": "agent:" + agent, "Kind": "Agent", "Target": agent})
	}
	for _, handle := range ListContactPreAuthorizations(RootDB, user) {
		out = append(out, map[string]any{"_id": "contact:" + handle, "Kind": "Contact", "Target": handle})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleGrantRevoke clears one standing grant. id = "<kind>:<target>" from the
// grants list; future actions to that agent/contact queue for approval again.
func (T *OrchestrateApp) handleGrantRevoke(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	kind, target, found := strings.Cut(id, ":")
	if !found || target == "" {
		http.Error(w, "bad grant id", http.StatusBadRequest)
		return
	}
	switch kind {
	case "agent":
		SetDelegationPreAuthorized(RootDB, user, target, false)
	case "contact":
		SetContactPreAuthorized(RootDB, user, target, false)
	default:
		http.Error(w, "unknown grant kind", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelClear wipes an agent's Cortex home-thread conversation and its
// rolling summary / fold cursor — the cheap fix for a crystallized thread.
// Operational state (monitors, standing agents, approvals) is left untouched;
// that's Decommission's job. POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelClear(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := consoleAgentID(r)
	sid := cortexSessionID(agentID)
	deleteChatSession(udb, agentID, sid) // includes the compact state
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelCompact force-folds an agent's Cortex home thread now: messages
// older than the verbatim recent tail collapse into the rolling summary (and are
// archived to searchable history), rather than being wiped. The gentle middle
// ground between doing nothing and Clear Cortex. The fold runs in the background
// (single-flight per thread), so this returns immediately. POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelCompact(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agentID := consoleAgentID(r)
	agent, ok := loadAgent(udb, agentID)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	// Nothing to fold into when the rolling summary is turned off — the thread
	// bounds by forgetting old messages instead, so there's no summary to build.
	if agent.DisableCompaction {
		http.Error(w, "compaction is disabled for this agent — nothing to compact", http.StatusBadRequest)
		return
	}
	keepRecent := agent.ContextDepth
	if keepRecent <= 0 {
		keepRecent = 12
	}
	// Trigger=1 forces a fold regardless of how short the unsummarized tail is:
	// everything older than the KeepRecent verbatim tail folds now.
	T.maybeFoldOperatorHistory(udb, agent, cortexSessionID(agentID), CompactionConfig{KeepRecent: keepRecent, Trigger: 1})
	w.WriteHeader(http.StatusNoContent)
}

// handleChannelDecommission tears down the owner's standing fleet — every event
// monitor, standing agent, and pending authorization. Destructive and explicit
// (confirm-gated client-side); the Cortex thread itself is left intact
// (use Clear Cortex for that). POST ?agent=<id>.
func (T *OrchestrateApp) handleChannelDecommission(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	for _, m := range ListEventMonitors(RootDB, user) {
		DeleteEventMonitor(RootDB, user, m.Name)
	}
	for _, s := range ListStandingAgents(RootDB, user) {
		DeleteStandingAgent(RootDB, user, s.Name)
	}
	for _, a := range ListAuthorizations(RootDB, user) {
		DeleteAuthorization(RootDB, user, a.ID)
	}
	// Standing grants too — a clean slate means future actions all re-queue.
	for _, agent := range ListDelegationPreAuthorizations(RootDB, user) {
		SetDelegationPreAuthorized(RootDB, user, agent, false)
	}
	for _, handle := range ListContactPreAuthorizations(RootDB, user) {
		SetContactPreAuthorized(RootDB, user, handle, false)
	}
	w.WriteHeader(http.StatusNoContent)
}

type consoleAgentRow struct {
	Name string `json:"name"`
	// Mission is the standing brief handed to the agent each run — "what it's
	// told to do." Surfaced so the Enabled-agents view shows each agent's
	// instructions, not just its schedule/status. Renders as a detail line
	// under the name in the cards layout.
	Mission  string `json:"mission,omitempty"`
	State    string `json:"state"` // active | paused
	Schedule string `json:"schedule"`
	Status   string `json:"status"`
	NextRun  string `json:"next_run"`
	ID       string `json:"_id"`               // hidden; row-action target (the agent name)
	Paused   bool   `json:"_paused"`           // hidden; gates Pause vs Resume per row
	Broken   bool   `json:"_broken,omitempty"` // hidden; broken (target agent gone) → needs relink
}

// handleConsoleAgents lists the owner's standing (scheduled) agents, each
// joined with the status of its most recent run.
func (T *OrchestrateApp) handleConsoleAgents(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Scope to the agent this pane is for — see standingAgentOnRailOf — so a
	// pane shows the schedules that are this agent's business and not everyone
	// else's.
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []consoleAgentRow{}
	for _, sa := range ListStandingAgents(RootDB, user) {
		// Both the agent that runs it and the one that manages it — see
		// standingAgentOnRailOf. Either alone leaves a blind spot.
		if !standingAgentOnRailOf(sa, agentID) {
			continue
		}
		state := "active"
		if sa.Paused {
			state = "paused"
		}
		row := consoleAgentRow{Name: sa.Name, Mission: sa.Mission, State: state, Schedule: StandingScheduleLabel(sa), ID: sa.Name, Paused: sa.Paused}
		if sa.Broken {
			row.Broken = true
			row.State = brokenStateLabel(sa.BrokenReason)
		}
		if !sa.NextRun.IsZero() {
			row.NextRun = sa.NextRun.UTC().Format(time.RFC3339)
		}
		// By identity, with the display name as the legacy fallback for runs
		// recorded before subjects existed (see RunFilter.Subject).
		if latest := ListRuns(RootDB, user, RunFilter{Limit: 1}.AboutStanding(sa.Name)); len(latest) > 0 {
			row.Status = string(latest[0].Status)
		}
		// Warn only while a subject-less run could actually be crossing the two.
		// New runs carry a subject and cannot; saying otherwise would send
		// someone renaming a pair that is already fine, and the warning would
		// never go away.
		if clash, ok := standingNameCollision(udb, user, sa); ok && hasLegacyRunsNamed(user, sa.Name) {
			row.State = "⚠ shares a name with the agent " + chFirst(clash.Name, clash.ID) +
				", and runs recorded before this release cannot tell them apart — rename one, or wait for those runs to age out"
		}
		rows = append(rows, row)
	}
	writeJSON(w, rows)
}

// standingNameCollision reports the agent whose Name is the same as this
// standing agent's, if there is one.
//
// It matters because the run ledger's Agent field is a shared, free-text
// namespace — "standing-agent / job name", per its own comment — and different
// writers put different things in it: a machine run and a guardrail run record
// an agent's NAME, agent CRUD records an agent's ID, and a standing run records
// the schedule's name. So looking up a standing agent's history with
// RunFilter{Agent: sa.Name} matches any run filed under that same text,
// including every run of an agent that happens to be called that.
//
// The result is a status and a last-run that belong to something else, shown
// without a hint that anything is wrong — a wrong answer being strictly worse
// than no answer, since nothing about the row invites doubt.
//
// Detection lives HERE, at the read, rather than as a guard at creation:
// renaming is the user's call, an agent can be renamed into a collision long
// after the schedule was made, and a check that only ran at creation would
// never see it. Matching is case-insensitive and trims, because the ledger
// lookup is exact — a pair that differs only in case does NOT actually cross,
// and flagging it would be a false alarm. Those are excluded below.
// hasLegacyRunsNamed reports whether any run filed under this display name
// predates RunRecord.Subject — the only runs a name collision can still cross.
// Every run written since carries an identity, so the ambiguity is bounded and
// drains as pruneRuns ages those rows out.
func hasLegacyRunsNamed(owner, name string) bool {
	for _, r := range ListRuns(RootDB, owner, RunFilter{Agent: name}) {
		if r.Subject == "" {
			return true
		}
	}
	return false
}

func standingNameCollision(udb Database, owner string, sa StandingAgent) (AgentRecord, bool) {
	name := strings.TrimSpace(sa.Name)
	if udb == nil || name == "" {
		return AgentRecord{}, false
	}
	for _, a := range listAgents(udb, owner) {
		// Exact, because that is what ListRuns compares.
		if strings.TrimSpace(a.Name) == name {
			return a, true
		}
	}
	return AgentRecord{}, false
}

// handleConsoleAgentDelete removes a standing agent (and cancels its schedule)
// from the Enabled-agents pane's row Delete button. id = the agent's name.
func (T *OrchestrateApp) handleConsoleAgentDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if name == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	DeleteStandingAgent(RootDB, user, name)
	w.WriteHeader(http.StatusNoContent)
}

// setConsoleAgentPaused pauses or resumes a standing agent — the same logic as
// the set_standing_paused tool, behind the Enabled-agents pane's Pause/Resume
// row buttons. Idempotent, so a Pause click on an already-paused agent (or vice
// versa) is harmless. id = the agent's name.
func (T *OrchestrateApp) setConsoleAgentPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	sa, found := GetStandingAgent(RootDB, user, name)
	if !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	sa.Paused = paused
	if paused {
		if sa.SchedulerID != "" {
			UnscheduleTask(sa.SchedulerID)
			sa.SchedulerID = ""
			sa.NextRun = time.Time{}
		}
		SaveStandingAgent(RootDB, sa)
	} else {
		// Resume from broken is gated: only proceed once the target agent is back,
		// clearing the broken flag on success (see the monitor path for rationale).
		if sa.Broken {
			if reason := standingAgentDependencyError(sa); reason != "" {
				http.Error(w, "can't resume — "+reason+"; relink it to a live agent or delete it", http.StatusConflict)
				return
			}
			sa.Broken = false
			sa.BrokenReason = ""
		}
		SaveStandingAgent(RootDB, sa)
		_ = ScheduleStandingAgent(RootDB, sa)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleAgentPause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleAgentPaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleAgentResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleAgentPaused(w, r, false)
}

// handleConsoleAgentRun fires a standing agent's run immediately from the
// Enabled-agents pane's "Run now" button — a one-off manual test that does NOT
// disturb the recurring schedule (RunStandingAgentNow stamps trigger "manual").
// The run executes off-request in a goroutine (an agent run can take a while and
// must outlive the response), with a background context so it isn't cancelled
// when the HTTP handler returns; the outcome reports back through the standing
// reporter, and the run ledger updates the row's status on reload. id = the
// agent's name. Returns 202 once the run is launched.
func (T *OrchestrateApp) handleConsoleAgentRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if _, found := GetStandingAgent(RootDB, user, name); !found {
		http.Error(w, "no such standing agent", http.StatusNotFound)
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				Log("[orchestrate/console] run-now panicked for standing agent %s/%s: %v", user, name, rec)
			}
		}()
		RunStandingAgentNow(context.Background(), RootDB, user, name)
	}()
	w.WriteHeader(http.StatusAccepted)
}

type consoleMonitorRow struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
	Script  string `json:"format_script"` // the watch format_script, if any (so you can SEE it)
	Checked string `json:"last_checked"`  // when the poll last ran (liveness)
	Seen    string `json:"last_seen"`     // last response/value it hashed/observed
	Last    string `json:"last_fired"`
	ID      string `json:"_id"`     // hidden; row-action target (the monitor name)
	Paused  bool   `json:"_paused"` // hidden; gates Pause vs Resume per row
	// Schedulable gates the "Test" row action: only poll / http_poll / watch
	// monitors have a check to run on demand — a webhook is push-only.
	Schedulable bool `json:"_schedulable"`
	Broken      bool `json:"_broken,omitempty"` // hidden; dependency gone → needs relink
}

// handleConsoleMonitors lists the owner's event monitors (webhook / poll /
// http_poll) for the Event-monitors nav pane.
func (T *OrchestrateApp) handleConsoleMonitors(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Scope to the agent this pane is for — a monitor belongs to the agent it
	// wakes (WakeAgent, set on create), so without this every agent's pane shows
	// every agent's monitors.
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	rows := []consoleMonitorRow{}
	for _, m := range ListEventMonitors(RootDB, user) {
		// A monitor belongs to the agent it wakes. An empty WakeAgent means the
		// default Chat agent (seed-chat) — map it so those monitors show on
		// Chat's pane instead of vanishing from every pane.
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		state := "active"
		if m.Paused {
			state = "paused"
		}
		if m.Broken {
			state = brokenStateLabel(m.BrokenReason)
		}
		detail := ""
		switch m.Kind {
		case EventKindPoll:
			detail = fmt.Sprintf("every %ds via %s", m.IntervalSeconds, m.CheckAgent)
		case EventKindHTTP:
			detail = fmt.Sprintf("every %ds: %s %s %s", m.IntervalSeconds, m.URL, m.CompareOp, m.Threshold)
		case EventKindWatch:
			detail = fmt.Sprintf("every %ds: watch %s", m.IntervalSeconds, m.ToolName)
			if len(m.ToolArgs) > 0 {
				if b, err := json.Marshal(m.ToolArgs); err == nil {
					detail += " " + string(b)
				}
			}
			detail += " for changes"
			if strings.TrimSpace(m.FormatScript) != "" {
				// A format_script only suppresses now when it emits the explicit
				// SKIP sentinel; empty output fails open to the built-in diff. Flag
				// its presence so a custom-formatted watcher is still visible here.
				detail += " [format_script]"
			}
		case EventKindWebhook:
			detail = "webhook (POST .../event/" + m.Token + ")"
		}
		if detail != "" {
			var dests []string
			hasWake := false
			for _, mode := range strings.Split(m.Notify, ",") {
				switch strings.TrimSpace(mode) {
				case EventNotifyText:
					dests = append(dests, "texts you")
				case EventNotifyDirect:
					if m.DeliverChatID != "" {
						dests = append(dests, "posts to its chat")
					} else {
						dests = append(dests, "posts here")
					}
				case EventNotifyChannel:
					dests = append(dests, "wakes here")
					hasWake = true
				}
			}
			if len(dests) == 0 {
				dests = append(dests, "wakes here")
				hasWake = true
			}
			detail += " → " + strings.Join(dests, " + ")
			if !hasWake {
				detail += " (no LLM)"
			}
			// Show where the monitor surfaces (Surface) so the user sees + can change
			// it via the Move-to action (its card/badge/wake all follow).
			detail += surfaceSuffix(m.Surface)
		}
		last := ""
		if !m.LastFired.IsZero() {
			last = m.LastFired.Local().Format("Jan 2 3:04 PM")
		}
		checked := ""
		if !m.LastChecked.IsZero() {
			checked = m.LastChecked.Local().Format("Jan 2 3:04 PM")
		}
		seen := strings.ReplaceAll(m.LastResult, "\n", " ")
		if r := []rune(seen); len(r) > 80 {
			seen = string(r[:80]) + "…"
		}
		// Full script — the UI table renders long/multi-line cells with a
		// click-to-expand toggle, so send it whole rather than truncating here.
		script := strings.TrimSpace(m.FormatScript)
		rows = append(rows, consoleMonitorRow{Name: m.Name, Kind: m.Kind, State: state, Detail: detail, Script: script, Checked: checked, Seen: seen, Last: last, ID: m.Name, Paused: m.Paused, Schedulable: IsScheduledEventKind(m.Kind), Broken: m.Broken})
	}
	writeJSON(w, rows)
}

func (T *OrchestrateApp) handleConsoleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if name == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	DeleteEventMonitor(RootDB, user, name)
	w.WriteHeader(http.StatusNoContent)
}

// setConsoleMonitorPaused pauses/resumes an event monitor. Pausing a scheduled
// monitor (poll/http_poll) cancels its next check; resuming reschedules it. For
// a webhook monitor the Paused flag alone gates the public endpoint.
func (T *OrchestrateApp) setConsoleMonitorPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	m.Paused = paused
	if paused {
		if m.SchedulerID != "" {
			UnscheduleTask(m.SchedulerID)
			m.SchedulerID = ""
			m.NextCheck = time.Time{}
		}
		SaveEventMonitor(RootDB, m)
	} else {
		// Resume — recovery from a broken monitor is a GATED, explicit action:
		// only allow it once the missing dependency is actually back, otherwise
		// we'd resume into a monitor that just re-breaks on its next tick. Re-check
		// here and refuse while it's still missing; clear the broken flag on
		// success so the monitor comes back healthy.
		if m.Broken {
			if reason := eventMonitorDependencyError(m); reason != "" {
				http.Error(w, "can't resume — "+reason+"; relink it to a live agent or delete it", http.StatusConflict)
				return
			}
			m.Broken = false
			m.BrokenReason = ""
		}
		SaveEventMonitor(RootDB, m)
		_ = ScheduleEventMonitor(RootDB, m)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleMonitorPause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleMonitorPaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleMonitorResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleMonitorPaused(w, r, false)
}

// handleConsoleMonitorRun runs a monitor's check immediately from the
// Event-monitors pane's "Test" button — a one-off manual poll that fires the
// wake/notify if the condition matches, without touching the monitor's cadence
// (RunEventMonitorCheck does not re-arm). Rejected for webhook (push-only)
// monitors, which have no check to run. The check runs off-request in a
// goroutine with a background context (a poll can call an external agent/URL and
// must outlive the response); the row's last-checked timestamp updates on
// reload. id = the monitor's name. Returns 202 once the check is launched.
func (T *OrchestrateApp) handleConsoleMonitorRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	m, found := GetEventMonitor(RootDB, user, name)
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	if !IsScheduledEventKind(m.Kind) {
		http.Error(w, "this monitor is push-triggered — it has no check to run", http.StatusBadRequest)
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				Log("[orchestrate/console] test-check panicked for monitor %s/%s: %v", user, name, rec)
			}
		}()
		if err := RunEventMonitorCheck(context.Background(), RootDB, user, name); err != nil {
			Log("[orchestrate/console] test-check failed for monitor %s/%s: %v", user, name, err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// handleConsoleRuns returns the run-ledger feed (owner-scoped, status-level).
func (T *OrchestrateApp) handleConsoleRuns(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	runs := ListRuns(RootDB, user, RunFilter{Limit: 100})
	if runs == nil {
		runs = []RunRecord{}
	}
	writeJSON(w, runs)
}

// consoleActivityRow is one row of the live "Active now" pane. Cards layout:
// Agent renders bold as the title, the rest as detail lines. _id / _running
// are hidden plumbing for the Cancel row action.
type consoleActivityRow struct {
	Agent    string `json:"agent"`
	Activity string `json:"activity"`
	Brief    string `json:"brief,omitempty"`
	Surface  string `json:"surface"`
	ID       string `json:"_id"`
	Running  bool   `json:"_running,omitempty"`
}

// handleConsoleActivity serves the live agent-activity view: every run the
// in-memory registry knows about for this user — interactive chat turns,
// scheduled fires, and standing-agent fires — running ones first, then the
// recently completed (the registry's reconnect-retention window doubles as
// the history horizon). This is the "what is the AI doing right now" pane
// the per-session live card can't provide: it only ever shows the session
// you're looking at, while the fleet works in the background.
func (T *OrchestrateApp) handleConsoleActivity(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := []consoleActivityRow{}
	now := time.Now()
	// ?run=<id> narrows to one run and everything it started — the destination
	// the live pill points a background entry at. An id that matches nothing
	// returns nothing rather than falling back to the whole list, so a stale
	// link says "that work is over" instead of quietly showing the deployment.
	snaps := descendantsOf(T.runsRegistry().Activity(user), strings.TrimSpace(r.URL.Query().Get("run")))
	background := markBackground(snaps)
	for _, s := range snaps {
		name := s.AgentName
		if name == "" {
			name = s.AgentID
		}
		var activity string
		switch s.Status {
		case RunStatusRunning:
			activity = "● running — " + shortElapsed(now.Sub(s.StartedAt))
			if s.Round > 0 {
				activity += fmt.Sprintf(", round %d", s.Round)
			}
			if s.LastTool != "" {
				activity += ", last tool: " + s.LastTool
			}
		default:
			activity = s.Status + " " + shortElapsed(now.Sub(s.EndedAt)) + " ago — took " + shortElapsed(s.EndedAt.Sub(s.StartedAt))
			if s.Round > 0 {
				activity += fmt.Sprintf(" (%d rounds)", s.Round)
			}
		}
		surface := s.Kind
		if background[s.ID] && s.Status == RunStatusRunning {
			// Said on the row, not just in a color: this table is also read by
			// people who arrived from a link rather than from the pill.
			surface += " · background"
		}
		rows = append(rows, consoleActivityRow{
			Agent:    runIndentPrefix(s.Depth) + name,
			Activity: activity,
			Brief:    s.Label,
			Surface:  surface,
			ID:       s.ID,
			Running:  s.Status == RunStatusRunning,
		})
	}
	writeJSON(w, rows)
}

// handleConsoleActivityCancel cancels one in-flight run from the Active-now
// pane — the UI kill switch for a runaway cycle. Owner-checked against the
// run's recorded user; canceling an already-finished run is a no-op.
func (T *OrchestrateApp) handleConsoleActivityCancel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	run := T.runsRegistry().Get(r.URL.Query().Get("id"))
	if run == nil || run.UserID != user {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	run.Cancel()
	w.WriteHeader(http.StatusNoContent)
}

// shortElapsed renders a duration for the activity pane: 42s, 3m10s, 1h04m.
func shortElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// handleConsoleRunDetail returns one run's full record (encrypted raw fetched
// on demand).
func (T *OrchestrateApp) handleConsoleRunDetail(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rec, found := GetRun(RootDB, user, r.URL.Query().Get("id"))
	if !found {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, rec)
}

// handleConsoleApprovals lists pending delegations awaiting the user's
// approval (the authorizations queue).
func (T *OrchestrateApp) handleConsoleApprovals(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type apprRow struct {
		Agent     string `json:"agent"`
		Action    string `json:"action"`
		Requested string `json:"requested"`
		ID        string `json:"_id"`
	}
	out := []apprRow{}
	for _, a := range ListAuthorizations(RootDB, user) {
		agent, action := approvalDisplay(udb, user, a)
		out = append(out, apprRow{
			Agent:     agent,
			Action:    action,
			Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
			ID:        a.ID,
		})
	}
	writeJSON(w, out)
}

// approvalDisplay maps a pending Authorization to its (who, detail) display
// pair, applying the same per-action relabeling the approvals queue shows.
// Shared by the approvals list and the combined Permissions view.
func approvalDisplay(udb Database, user string, a Authorization) (who, detail string) {
	who, detail = a.Agent, a.Brief
	switch a.Action {
	case "send_message":
		who = operatorApprovalRecipient(user, a)
		detail = "text: " + a.Text
	case "converse_contact":
		who = operatorApprovalRecipient(user, a)
		detail = "converse toward: " + a.Brief
	case "activate_sub_agent":
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		detail = "activate sub-agent: " + a.Brief
	case buildAgentAction:
		// The REQUESTER is who's asking; the build itself is the detail.
		if a.FromAgent != "" {
			if rec, found := loadAgent(udb, a.FromAgent); found {
				who = rec.Name
			} else {
				who = a.FromAgent
			}
		}
		detail = "build a sub-agent: " + a.Brief
	case "bind_thread":
		who = operatorApprovalRecipient(user, a)
		detail = "bind 1:1 thread so the agent can read replies"
		if a.Brief != "" {
			detail = "bind 1:1 thread (" + a.Brief + ")"
		}
	case "autonomous_tool":
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		detail = "run \"" + a.Brief + "\" unattended (refused on a scheduled fire; approving pre-authorizes it)"
	case "scope_tool":
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		// Says plainly that nothing is waiting on this. The old copy ("keep X in
		// this agent's own kit") read as a permission being withheld, when the
		// agent has been calling the tool all along — that IS the evidence.
		detail = "Suggestion: \"" + a.Brief + "\" looks like part of this agent's kit — scope it so it's always in the catalog instead of loaded on demand. It keeps working either way."
	case orphanMemoryRefAction:
		if rec, found := loadAgent(udb, a.Agent); found {
			who = rec.Name
		}
		// Says what is actually wrong — the agent's memory is now false — and
		// that nothing is blocked. It runs fine; it just runs believing it can
		// call something it cannot.
		detail = "Suggestion: this agent's memory still refers to \"" + a.Brief + "\", whose last agent was deleted, so nothing can call it. Approving re-homes the tool here so the memory is true again. Or edit the memory from the agent's Memory pane — nothing is blocked either way."
	}
	return who, detail
}

// handleConsolePermissions is the combined "Permissions" page: pending approval
// requests AND the standing grants you've already given, on one page. Pending
// rows come first (they need action) and carry _pending; granted rows carry
// _granted, so the table's conditional row actions show Approve/Always/Deny on
// the former and Revoke on the latter. The pinned rail badge counts _pending.
func (T *OrchestrateApp) handleConsolePermissions(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Field order matters: the card layout uses the FIRST non-underscore field
	// as the title (Who), then Detail (mono box), Status (pill), Requested.
	type permRow struct {
		Who       string `json:"Who"`
		Detail    string `json:"Detail"`
		Status    string `json:"Status,omitempty"`
		Requested string `json:"Requested,omitempty"`
		ID        string `json:"_id"`
		Pending   bool   `json:"_pending,omitempty"`
		Managed   bool   `json:"_managed,omitempty"`    // a standing policy row (segmented control + Remove)
		Policy    string `json:"_policy,omitempty"`     // allow | ask | block (the segmented state)
		OneShot   bool   `json:"_oneshot,omitempty"`    // one-time decision (Approve/Deny only) — no "Always" grant makes sense (e.g. activating a drafted sub-agent, which the approval consumes)
		Suggested bool   `json:"_suggestion,omitempty"` // an OFFER, not a request: nothing is blocked on it (see approvalIsSuggestion)
	}
	out := []permRow{}
	// Zone 1 — live pending requests (a decision is blocked on the user), then
	// Zone 1b — suggestions. Both come out of the Authorizations store, but only
	// the first kind is holding anything up, so they are collected separately:
	// requests stay on top and own the rail badge, offers sit below them and are
	// counted nowhere. Mixing them made routine curation look like a permission.
	var suggested []permRow
	for _, a := range ListAuthorizations(RootDB, user) {
		who, detail := approvalDisplay(udb, user, a)
		if approvalIsSuggestion(a.Action) {
			suggested = append(suggested, permRow{
				Who: who, Detail: detail, Status: "Suggested",
				Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
				ID:        a.ID, Suggested: true,
			})
			continue
		}
		out = append(out, permRow{
			Who: who, Detail: detail, Status: "Pending",
			Requested: a.Requested.Local().Format("Jan 2 3:04 PM"),
			ID:        a.ID, Pending: true,
			// Activating a drafted sub-agent is a ONE-TIME decision — approving it
			// makes the agent live and the authorization disappears, so "Always"
			// has no meaning. Present just Approve / Deny for it.
			OneShot: a.Action == "activate_sub_agent",
		})
	}
	out = append(out, suggested...)
	// Zone 2 — standing permission policy per subject (Always allow / Needs
	// approval / Blocked, + Remove).
	for _, e := range ListDelegationPolicies(RootDB, user) {
		name := e.Target
		if rec, found := loadAgent(udb, e.Target); found && rec.Name != "" {
			name = rec.Name
		}
		out = append(out, permRow{Who: name, Detail: "Agent delegation", ID: "agent:" + e.Target, Managed: true, Policy: e.Policy})
	}
	for _, e := range ListContactPolicies(RootDB, user) {
		out = append(out, permRow{Who: e.Target, Detail: "Contact messaging", ID: "contact:" + e.Target, Managed: true, Policy: e.Policy})
	}
	// Zone 3 — autonomous-run tool grants: the tools you "Always allowed" a
	// scheduled/standing agent to run unattended (AutoApproveTools). Surfaced so
	// the grant isn't invisible after approval — Remove (or "Needs approval")
	// revokes it, and the tool re-queues on its next unattended fire.
	for _, ag := range listAgents(udb, user) {
		for _, tool := range ag.AutoApproveTools {
			out = append(out, permRow{
				Who:     firstNonEmptyStr(ag.Name, ag.ID),
				Detail:  "Autonomous tool: " + tool,
				ID:      "autotool:" + ag.ID + ":" + tool,
				Managed: true, Policy: "allow",
			})
		}
	}
	writeJSON(w, out)
}

// removeAutoApproveTool revokes a standing autonomous-tool grant from an agent.
func removeAutoApproveTool(udb Database, agentID, tool string) {
	rec, ok := loadAgent(udb, agentID)
	if !ok {
		return
	}
	var kept []string
	changed := false
	for _, t := range rec.AutoApproveTools {
		if t == tool {
			changed = true
			continue
		}
		kept = append(kept, t)
	}
	if changed {
		rec.AutoApproveTools = kept
		if _, err := saveAgent(udb, rec); err != nil {
			Log("[console.perm] revoke autonomous tool %s/%s failed: %v", agentID, tool, err)
		}
	}
}

// handleConsolePrivileges is the write side of the inline privileges card: it
// applies the same edits the Permissions pane and the agent editor make, to the
// same AgentRecord, for an agent the CALLER owns.
//
// The card is a shortcut, never an authority. The model can emit a card; only a
// request carrying the owner's session can change anything, and the record is
// loaded from that user's own store — an id belonging to someone else simply
// isn't there. POST body:
//
//	{"agent_id":"…","tools":{"send_message":"allow","call_x":"ask"},
//	 "flags":{"fleet":true,"exposed":false}}
//
// Tool policy is binary: "allow" pre-authorizes the tool for unattended runs
// (AutoApproveTools), anything else revokes so it queues again on its next
// fire. Flags map to the capability toggles by their form field names.
func (T *OrchestrateApp) handleConsolePrivileges(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AgentID string            `json:"agent_id"`
		Tools   map[string]string `json:"tools"`
		Flags   map[string]bool   `json:"flags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	udb := UserDB(T.DB, user)
	rec, found := loadAgent(udb, strings.TrimSpace(body.AgentID))
	if !found {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	// A sub-agent's posture is the framework's to decide (it carries its
	// parent's authority and is never published on its own), so the card shows
	// those rows locked and the server refuses them too — a locked control that
	// only the UI enforces is not a control.
	if strings.TrimSpace(rec.OwnedBy) != "" && len(body.Flags) > 0 {
		http.Error(w, "a sub-agent's capabilities follow its parent", http.StatusConflict)
		return
	}
	for tool, policy := range body.Tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(policy), "allow") {
			rec.AutoApproveTools = appendUnique(rec.AutoApproveTools, tool)
			continue
		}
		var kept []string
		for _, t := range rec.AutoApproveTools {
			if t != tool {
				kept = append(kept, t)
			}
		}
		rec.AutoApproveTools = kept
	}
	for field, on := range body.Flags {
		switch strings.TrimSpace(field) {
		case "fleet":
			rec.Fleet = on
		case "author":
			rec.Author = on
		case "exposed":
			rec.Exposed = on
		case "mcp_exposed":
			rec.MCPExposed = on
		case "allow_builder_dispatch":
			rec.AllowBuilderDispatch = on
		default:
			http.Error(w, "unknown capability "+field, http.StatusBadRequest)
			return
		}
	}
	if _, err := saveAgent(udb, rec); err != nil {
		Log("[console.privileges] save %s failed: %v", rec.ID, err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appendUnique adds s to list unless it's already there.
func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// handleConsolePermissionPolicy sets a subject's standing policy.
// POST ?id=<agent:…|contact:…>&value=<allow|ask|block>.
func (T *OrchestrateApp) handleConsolePermissionPolicy(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, target, found := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), ":")
	value := strings.TrimSpace(r.URL.Query().Get("value"))
	if !found || target == "" {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch kind {
	case "agent":
		SetDelegationPolicy(RootDB, user, target, value)
	case "contact":
		SetContactPolicy(RootDB, user, target, value)
	case "autotool":
		// A tool grant is binary (granted or not) — any state other than "allow"
		// revokes it; the tool re-queues for approval on its next unattended fire.
		if aid, tool, ok := strings.Cut(target, ":"); ok && tool != "" && value != "allow" {
			removeAutoApproveTool(UserDB(T.DB, user), aid, tool)
		}
	default:
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleConsolePermissionRemove forgets a subject's policy entirely (back to
// the ask default, dropped from the list). POST ?id=<agent:…|contact:…>.
func (T *OrchestrateApp) handleConsolePermissionRemove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, target, found := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), ":")
	if !found || target == "" {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	switch kind {
	case "agent":
		RemoveDelegationPolicy(RootDB, user, target)
	case "contact":
		RemoveContactPolicy(RootDB, user, target)
	case "autotool":
		if aid, tool, ok := strings.Cut(target, ":"); ok && tool != "" {
			removeAutoApproveTool(UserDB(T.DB, user), aid, tool)
		}
	default:
		http.Error(w, "unknown subject", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// operatorApprovalRecipient renders a human-readable recipient for a phantom
// messaging approval. When the message was addressed by chat_id (e.g. a group,
// which has no single handle), it resolves the chat to its display name/handle
// via the bridge so the approval row names WHO it's going to — otherwise the
// row would show a bare chat id (or nothing) and the user couldn't tell.
func operatorApprovalRecipient(owner string, a Authorization) string {
	if strings.TrimSpace(a.Handle) != "" {
		return a.Handle
	}
	if strings.TrimSpace(a.ChatID) != "" {
		if link, ok := ActiveMessagingLink(); ok {
			if s, ok := link.DescribeChat(owner, a.ChatID); ok {
				switch {
				case s.DisplayName != "" && s.Handle != "":
					return s.DisplayName + " (" + s.Handle + ")"
				case s.DisplayName != "":
					return s.DisplayName
				case s.Handle != "":
					return s.Handle
				}
			}
		}
		return a.ChatID
	}
	return "(unknown recipient)"
}

// resolveApproval approves a queued delegation: optionally pre-authorizes the
// agent (always-allow), drops the pending entry, and runs the delegation async
// (the result lands in Activity / the run-ledger).
func (T *OrchestrateApp) resolveApproval(w http.ResponseWriter, r *http.Request, always bool) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	a, found := GetAuthorization(RootDB, user, r.URL.Query().Get("id"))
	if !found {
		http.Error(w, "no such authorization", http.StatusNotFound)
		return
	}
	DeleteAuthorization(RootDB, user, a.ID)
	// activate_sub_agent: a dispatched Builder drafted this sub-agent and held it
	// for approval. Approval just clears the PendingApproval hold so it goes
	// live — no delegation runs. ("Always allow" has no meaning here.)
	if a.Action == "activate_sub_agent" {
		if rec, ok := loadAgent(udb, a.Agent); ok {
			rec.PendingApproval = false
			if _, err := saveAgent(udb, rec); err != nil {
				Log("[operator.approval] activate_sub_agent %s save failed: %v", a.Agent, err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// build_agent: a non-Fleet agent asked (via request_build) to have a sub-agent
	// built. The user approving THIS is the human-in-the-loop the Fleet gate
	// protects — so now run Builder on the requester's behalf. a.FromAgent rides
	// as the dispatch origin, which runAgentSyncConfirm turns into the sub-session
	// DispatchParentAgentID, so Builder's create_agent stamps OwnedBy=<requester>
	// exactly as a live Fleet dispatch would. Builder's own creation then queues
	// its usual activate_sub_agent approval, so the built agent still lands
	// held-for-activation — this approval authorizes the BUILD, the next the
	// switch-on. Async, like the delegate approval below.
	if a.Action == buildAgentAction {
		go RunDelegation(context.Background(), RootDB, a.Owner, "builder", a.Brief, a.FromAgent)
		Log("[operator.approval] build_agent approved — dispatching Builder for owner=%s requester=%s", a.Owner, a.FromAgent)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// autonomous_tool: an unattended (standing/scheduled) fire wanted a
	// NeedsConfirm tool the agent isn't pre-authorized for. Approving adds the tool
	// to the PARENT's AutoApproveTools when the requester is a sub-agent — the grant
	// is inherited down the ownership chain, so the owner authorizes ONCE at the
	// parent and every sub-agent inherits it (autonomousApprovedSet), rather than
	// re-approving the same tool per sub-agent. Top-level agents grant on themselves.
	if a.Action == "autonomous_tool" {
		grantTo := a.Agent
		if rec, ok := loadAgent(udb, a.Agent); ok && rec.OwnedBy != "" {
			grantTo = rec.OwnedBy
		}
		if rec, ok := loadAgent(udb, grantTo); ok {
			has := false
			for _, t := range rec.AutoApproveTools {
				if t == a.Brief {
					has = true
					break
				}
			}
			if !has && a.Brief != "" {
				rec.AutoApproveTools = append(rec.AutoApproveTools, a.Brief)
				if _, err := saveAgent(udb, rec); err != nil {
					Log("[operator.approval] autonomous_tool %s/%s save failed: %v", grantTo, a.Brief, err)
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// orphan_memory_ref: an agent's memory names a tool whose last carrier was
	// deleted. Approving RE-HOMES the orphan onto this agent, which makes the
	// remembered capability real again — the repair the memory implies. The
	// other repair (editing the memory) stays manual in the Memory pane,
	// because the audit never deletes on the owner's behalf.
	if a.Action == orphanMemoryRefAction {
		if a.Brief != "" {
			if err := rehomeOrphanTool(RootDB, user, a.Brief, a.Agent); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			Log("[orchestrate.memaudit] approved: re-homed orphaned %q onto agent=%s", a.Brief, a.Agent)
		}
		DeleteAuthorization(RootDB, user, a.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// scope_tool: the Tier-2 elevation suggestion — this agent load_tool'd the
	// same shared tool across enough sessions that it's evidently kit.
	// Approving ADDS the agent to the tool's ScopeAgents: first-class in its
	// catalog from then on, and (when the tool was shared) withdrawn from the
	// general pool — the approval text says so.
	if a.Action == "scope_tool" {
		if a.Brief != "" {
			p, ok := UserToolByName(udb, user, a.Brief)
			if !ok {
				http.Error(w, "tool no longer exists", http.StatusNotFound)
				return
			}
			scope := p.ScopeAgents
			has := false
			for _, id := range scope {
				if id == a.Agent {
					has = true
					break
				}
			}
			if !has {
				scope = append(scope, a.Agent)
				if !SetUserToolScopeAgents(udb, user, a.Brief, scope) {
					http.Error(w, "scope update failed", http.StatusInternalServerError)
					return
				}
				Log("[orchestrate.elevate] approved: scoped %q to agent=%s", a.Brief, a.Agent)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// send_message: deliver the queued text to the contact via the phantom
	// bridge. ("Always allow" doesn't pre-authorize a contact — each outbound
	// to a real person stays a deliberate approval.)
	if a.Action == "send_message" {
		recip := operatorRecipientKey(a.ChatID, a.Handle)
		// "Always allow" pre-authorizes THIS recipient, so future texts (and
		// autonomous conversations) to the same chat/handle send without
		// re-queuing.
		if always {
			SetContactPreAuthorized(RootDB, a.Owner, recip, true)
		}
		if _, err := operatorDeliverMessage(a.Owner, a.Agent, a.ChatID, a.Handle, a.Text, a.Images); err != nil {
			Log("[operator.approval] send_message to %s failed: %v", recip, err)
		}
		// Approved post: if the target is a bound channel, make its agent see it
		// (channel session + cortex) so it can field follow-ups.
		recordChannelPost(UserDB(T.DB, a.Owner), a.Owner, a.ChatID, a.Handle, a.Text)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// bind_thread: the agent asked to bind a person's 1:1 thread so it can read
	// their replies. Approval creates a persistent, agent-bound channel scoped to
	// that handle, with a gatekeeper rule that keeps it to replies-to-its-own
	// messages. Idempotent; "Always allow" has no separate meaning here.
	if a.Action == "bind_thread" {
		addr := a.ChatID
		if addr == "" {
			addr = a.Handle
		}
		if addr != "" {
			exists := false
			for _, ch := range ListChannelsForAgent(RootDB, a.Owner, a.Agent) {
				if ch.Service == "imessage" && (ch.Address == addr || ch.Address == a.Handle || ch.Address == a.ChatID) {
					exists = true
					break
				}
			}
			if !exists {
				name := a.Brief
				if name == "" {
					name = addr
				}
				wake := a.Text != "nowake"
				gk := threadBindingGatekeeperRule
				if !wake {
					gk = "" // no-wake = record-only; the gatekeeper is skipped anyway
				}
				SaveChannel(RootDB, Channel{
					ID:         UUIDv4(),
					Owner:      a.Owner,
					AgentID:    a.Agent,
					Name:       "DM: " + name,
					Service:    "imessage",
					Address:    addr,
					AutoReply:  wake,
					Gatekeeper: gk,
					AgentBound: true,
				})
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// converse_contact: hand the goal to phantom, which runs the autonomous
	// multi-turn conversation and wakes the Operator back here when done.
	// (Like send_message, "Always allow" does NOT pre-authorize a contact —
	// each conversation stays a deliberate approval.)
	if a.Action == "converse_contact" {
		// Goal-conversations are retired; an approval for a stale queued one just
		// clears (nothing to start). Guard kept so it doesn't fall through to the
		// delegation path below.
		Log("[operator.approval] converse_contact retired; clearing stale approval")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if always {
		SetDelegationPreAuthorized(RootDB, user, a.Agent, true)
	}
	// a.FromAgent (captured when the delegation was queued) keeps an approved
	// delegation's channel reach identical to a pre-authorized one's. Empty on
	// legacy records — that run just keeps its own scope.
	go RunDelegation(context.Background(), RootDB, a.Owner, a.Agent, a.Brief, a.FromAgent)
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleApprovalApprove(w http.ResponseWriter, r *http.Request) {
	T.resolveApproval(w, r, false)
}

func (T *OrchestrateApp) handleApprovalAlways(w http.ResponseWriter, r *http.Request) {
	T.resolveApproval(w, r, true)
}

// handleCredentialUpdateApply applies a user-APPROVED config change to the
// caller's OWN api credential (the credential_update card's Approve button).
// Config-only: base_url / param_name / description. It loads the existing
// record, overlays only the provided fields, and saves with an EMPTY secret —
// which Secure().Save treats as "keep the existing secret" and also preserves
// the Disabled/Secured state. So approving an update can never wipe the secret
// or silently enable/disable a working credential. Scoped to the user's
// namespace (LoadUser + Owner=user), so it can't touch a global/admin cred.
func (T *OrchestrateApp) handleCredentialUpdateApply(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		Name        string `json:"name"`
		BaseURL     string `json:"base_url"`
		ParamName   string `json:"param_name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	c, found := Secure().LoadUser(user, name)
	if !found {
		http.Error(w, "no credential named "+name+" in your API credentials", http.StatusNotFound)
		return
	}
	// Overlay only the provided config fields; leave the rest as-is.
	if v := strings.TrimSpace(body.BaseURL); v != "" {
		c.BaseURL = v
	}
	if v := strings.TrimSpace(body.ParamName); v != "" {
		c.ParamName = v
	}
	if v := strings.TrimSpace(body.Description); v != "" {
		c.Description = v
	}
	c.Owner = user
	// Empty secret => preserve the existing secret + enabled/secured state.
	if err := Secure().Save(c, ""); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleApprovalDeny(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	// Denying a sub-agent activation rejects the draft outright — delete the
	// held agent so it doesn't linger dormant (PendingApproval) forever.
	if a, found := GetAuthorization(RootDB, user, id); found && a.Action == "activate_sub_agent" {
		deleteAgent(udb, user, a.Agent)
	}
	DeleteAuthorization(RootDB, user, id)
	w.WriteHeader(http.StatusNoContent)
}
