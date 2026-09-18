// Package monitor is a central live-activity dashboard: one page that shows
// what every agent and app on the deployment is doing right now, refreshed on
// an interval. It owns no data of its own — it composes the framework's live
// endpoints (the orchestrate runs ledger + the global live feed) into a couple
// of auto-refreshing ui.Table surfaces, so it's a thin read-only view.
//
// It appears as a "Monitor" tab in the hub nav (WebAppHubTab).
package monitor

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterApp(new(MonitorApp))
	// Monitor IS the central live view, so it registers itself as the target the
	// core/ui live-activity pill links to. Declaring it here (not in core/ui)
	// keeps the framework's shared UI free of any app's route.
	ui.DefaultLiveURL = "/monitor"
}

// MonitorApp is the app entry point; AppCore wires the shared framework state.
type MonitorApp struct {
	AppCore
}

// --- core.Agent interface ---
func (T MonitorApp) Name() string         { return "monitor" }
func (T MonitorApp) SystemPrompt() string { return "" }
func (T MonitorApp) Desc() string {
	return "Apps: Central live monitor, what every agent and app is doing right now."
}
func (T *MonitorApp) Init() error { return T.Flags.Parse() }
func (T *MonitorApp) Main() error {
	Log("monitor is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

// --- core.WebApp ---
func (T *MonitorApp) WebPath() string { return "/monitor" }
func (T *MonitorApp) WebName() string { return "Monitor" }
func (T *MonitorApp) WebDesc() string { return "Live view of everything happening on gohort." }

// WebHidden: Monitor is NOT a discoverable app — no dashboard tile, no hub tab.
// It's the expanded view you reach by clicking the live area (the floating live
// pill or the dashboard's "Live Sessions" panel). The route still mounts.
func (T *MonitorApp) WebHidden() bool { return true }

func (T *MonitorApp) Routes() {
	T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handlePage(w, r)
	})
}

// monitorAgentsTable is the live agent-activity table. Split out of the page so
// its row action can be asserted on: the Cancel button's gate is a contract
// with the activity endpoint, and getting it wrong is invisible (a button that
// appears, posts, reports success, and stops nothing).
func monitorAgentsTable(source string) ui.Table {
	return ui.Table{
		Source:        source,
		RowKey:        "_id",
		AutoRefreshMS: 3000,
		EmptyText:     "No agent activity right now.",
		Columns: []ui.Col{
			{Field: "agent", Label: "Agent", Flex: 2},
			{Field: "surface", Label: "Via", Flex: 1, Mute: true},
			{Field: "activity", Label: "Status", Flex: 3, Mute: true},
			{Field: "brief", Label: "Doing", Flex: 3, Mute: true},
		},
		// The kill switch for a runaway run, on the only rows that HAVE one:
		// _cancellable marks a turn that is both in flight and running under a
		// context the endpoint can reach. It used to read _running, which is not
		// the same thing — most running rows had no cancel func behind them, so
		// the button appeared, the POST answered success, and the work carried
		// on. It lives here because
		// this is where a person watching work happen already is. It used to
		// live in orchestrate's Manage menu, on a pane that read this same
		// endpoint at this same interval — a second copy of this table, whose
		// one distinction was the button. The copy is gone; the button stayed.
		RowActions: []ui.RowAction{
			{Type: "button", Label: "Cancel", OnlyIf: "_cancellable", Compact: true,
				PostTo:  "/orchestrate/api/console/activity/cancel?id={_id}",
				Confirm: "Cancel this in-flight run? The agent stops mid-turn; anything it already did stays done."},
		},
	}
}

func (T *MonitorApp) handlePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// Agents — the orchestrate runs ledger, tree-ordered (sub-agents nested
	// under the turn that called them) with recently-finished runs lingering.
	// Absolute path: the endpoint lives under the orchestrate app's mux, and it
	// self-gates (admin), so a non-admin viewer just sees an empty table.
	// ?run=<id> arrives from the live pill: somebody clicked a background task
	// to see what it is doing. The page narrows to that run and the sub-agents
	// it started, rather than sending them to a table of the whole deployment
	// and leaving them to find the row again.
	focus := strings.TrimSpace(r.URL.Query().Get("run"))
	agentSource := "/orchestrate/api/console/activity"
	if focus != "" {
		agentSource += "?run=" + url.QueryEscape(focus)
	}
	agents := monitorAgentsTable(agentSource)
	// Everything running now — the same instantaneous feed the live pill shows:
	// apps and pipelines alongside active agent turns.
	live := ui.Table{
		Source: "/api/live",
		RowKey: "id",
		// /api/live resolves each row's way back into the app that owns the
		// work and blanks it for viewers who can't reach that app, so rows
		// link exactly when there's somewhere to go. Agent runs and queued
		// tasks have no owning page and stay inert — which is right, since
		// this page IS where they'd otherwise send you.
		RowLink:       "href",
		AutoRefreshMS: 3000,
		EmptyText:     "Nothing running.",
		Columns: []ui.Col{
			{Field: "app", Label: "Source", Flex: 1},
			{Field: "topic", Label: "Task", Flex: 3},
			{Field: "status", Label: "Status", Flex: 2, Mute: true},
		},
	}
	title, backURL := "Monitor", "/"
	agentsTitle := "Agents: live & recent"
	agentsSubtitle := "Every agent turn: chat, scheduled, standing, channel, dispatch, and the OpenAI endpoint. Sub-agents nest (↳) under the turn that called them; recently finished runs linger briefly."
	sections := []ui.Section{
		{Title: agentsTitle, Subtitle: agentsSubtitle, Body: agents},
		{
			Title:    "Everything running now",
			Subtitle: "The instantaneous snapshot behind the live pill: apps and pipelines alongside active agents.",
			Body:     live,
		},
	}
	if focus != "" {
		// A focused view drops the global feed. Someone who followed a link to
		// one task is asking about that task; a second table of everything
		// happening on the deployment is the thing they just navigated away
		// from, and it would be the larger of the two.
		title, backURL = "Task", "/monitor"
		agents.EmptyText = "That task has finished: it is no longer running, and finished runs are kept only briefly."
		sections = []ui.Section{{
			Title:    "This task",
			Subtitle: "The run you followed, and any sub-agents it started. Refreshes every 3 seconds; it disappears shortly after it finishes.",
			Body:     agents,
		}}
	}
	page := ui.Page{
		Title:     title,
		ShowTitle: true,
		BackURL:   backURL,
		MaxWidth:  "1100px",
		Nav:       HubNav("/monitor"),
		Sections:  sections,
	}
	page.ServeHTTP(w, r)
}
