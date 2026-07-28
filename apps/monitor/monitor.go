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
	return "Apps: Central live monitor — what every agent and app is doing right now."
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

func (T *MonitorApp) handlePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// Agents — the orchestrate runs ledger, tree-ordered (sub-agents nested
	// under the turn that called them) with recently-finished runs lingering.
	// Absolute path: the endpoint lives under the orchestrate app's mux, and it
	// self-gates (admin), so a non-admin viewer just sees an empty table.
	agents := ui.Table{
		Source:        "/orchestrate/api/console/activity",
		RowKey:        "_id",
		AutoRefreshMS: 3000,
		EmptyText:     "No agent activity right now.",
		Columns: []ui.Col{
			{Field: "agent", Label: "Agent", Flex: 2},
			{Field: "surface", Label: "Via", Flex: 1, Mute: true},
			{Field: "activity", Label: "Status", Flex: 3, Mute: true},
			{Field: "brief", Label: "Doing", Flex: 3, Mute: true},
		},
	}
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
	page := ui.Page{
		Title:     "Monitor",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "1100px",
		Nav:       HubNav("/monitor"),
		Sections: []ui.Section{
			{
				Title:    "Agents — live & recent",
				Subtitle: "Every agent turn: chat, scheduled, standing, channel, dispatch, and the OpenAI endpoint. Sub-agents nest (↳) under the turn that called them; recently finished runs linger briefly.",
				Body:     agents,
			},
			{
				Title:    "Everything running now",
				Subtitle: "The instantaneous snapshot behind the live pill — apps and pipelines alongside active agents.",
				Body:     live,
			},
		},
	}
	page.ServeHTTP(w, r)
}
