// Package monitor is a central live-activity dashboard: one page that shows
// what every agent and app on the deployment is doing right now, and what
// finished recently, refreshed on an interval. It merges the orchestrate runs
// ledger and the global live feed into one table (activity.go), keeping
// finished app work for a while itself, since the live feed forgets it.
//
// It appears as a "Monitor" tab in the hub nav (WebAppHubTab).
package monitor

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
	T.watch()
	T.HandleFunc("/api/activity", jsonapi.Get(T.DB, T.handleActivity))
	T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handlePage(w, r)
	})
}

// monitorTable is the Monitor's one table: running work first, then what
// finished recently. Split out of the page so its row actions can be asserted
// on: the Cancel button's gate is a contract with the activity endpoint, and
// getting it wrong is invisible (a button that appears, posts, reports
// success, and stops nothing).
func monitorTable(source string) ui.Table {
	return ui.Table{
		Source:        source,
		RowKey:        "_id",
		AutoRefreshMS: 3000,
		EmptyText:     "Nothing running, and nothing finished recently.",
		// An app's row opens the app, at that work, when the viewer can reach
		// it (href, gated server-side like the live pill's links). An agent
		// run's row has its Trace instead.
		RowLink: "href",
		Columns: []ui.Col{
			{Field: "name", Label: "Agent or app", Flex: 2},
			{Field: "via", Label: "Via", Flex: 1, Mute: true},
			{Field: "status", Label: "Status", Flex: 3, Mute: true},
			{Field: "doing", Label: "Doing", Flex: 3, Mute: true},
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
			monitorTraceAction(),
			{Type: "button", Label: "Cancel", OnlyIf: "_cancellable", Compact: true,
				PostTo:  "/orchestrate/api/console/activity/cancel?id={_id}",
				Confirm: "Cancel this in-flight run? The agent stops mid-turn; anything it already did stays done."},
		},
	}
}

// monitorTraceAction opens a run's trace: what it has done so far, step by
// step, its sub-agents' steps among them, refreshing while it goes. The row
// says what a run is doing now; this is how it got there, which is what tells
// a run that is working from one that is stuck. Every one of the viewer's
// agent runs has it (_run), a finished one too: its trace is kept for a while
// after it ends. An app's row opens the app instead.
func monitorTraceAction() ui.RowAction {
	a := ui.ModalAction("Trace", ui.Table{
		Source:        "/orchestrate/api/console/trace?id={_id}",
		RowKey:        "_id",
		AutoRefreshMS: 2000,
		EmptyText:     "Nothing to show: this run's trace has been cleared, as a finished run's is a while after it ends. Its run record keeps the full list: Agents, Fleet menu, Runs.",
		Columns: []ui.Col{
			{Field: "step", Label: "Step", Flex: 3},
			{Field: "state", Label: "State", Type: "badge", Badges: []ui.BadgeMapping{
				{Value: "running", Label: "running", Color: "accent", Spin: true},
				{Value: "done", Label: "done", Color: "success"},
				{Value: "completed", Label: "completed", Color: "success"},
				{Value: "failed", Label: "failed", Color: "danger"},
				{Value: "canceled", Label: "canceled", Color: "warning"},
				{Value: "said", Label: "said", Color: "mute"},
				{Value: "note", Label: "progress", Color: "mute"},
			}},
			{Field: "time", Label: "Time", Mute: true},
			{Field: "detail", Label: "Detail", Line: 2, Mute: true},
			{Field: "result", Label: "Result", Line: 2},
		},
	})
	a.Width = "820px"
	a.OnlyIf = "_run"
	return a
}

func (T *MonitorApp) handlePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// ?run=<id> arrives from the live pill: somebody clicked a background task
	// to see what it is doing. The page narrows to that run and the sub-agents
	// it started, rather than sending them to a table of the whole deployment
	// and leaving them to find the row again.
	focus := strings.TrimSpace(r.URL.Query().Get("run"))
	// Absolute: a relative source resolves against the page's own URL, which
	// is /monitor without the slash when it is reached from the pill.
	source := "/monitor/api/activity"
	if focus != "" {
		source += "?run=" + url.QueryEscape(focus)
	}
	table := monitorTable(source)
	title, backURL := "Monitor", "/"
	sections := []ui.Section{{
		Title:    "Running & recent",
		Subtitle: "What is running now, then what finished recently, newest first. Open an agent's Trace to see each step it took; click an app's row to open it.",
		Detail: "Running comes first: agent turns (chat, scheduled, standing, channel, dispatch, and the OpenAI endpoint) with their sub-agents nested (↳) under the turn that called them, then apps, pipelines and queued work. Cancel appears on the runs that can be stopped. " +
			"A finished agent run says how it ended (completed, failed, canceled). An app only reports that it ended, so its row says \"ended\" and the app's own page has the result. " +
			"Other people's work shows without its details, and without Trace or Cancel. Finished work drops off after about half an hour (for agent runs, Run buffer retention under Tuning).",
		Body: table,
	}}
	if focus != "" {
		// A focused view drops the global feed. Someone who followed a link to
		// one task is asking about that task; a table of everything happening
		// on the deployment is the thing they just navigated away from.
		title, backURL = "Task", "/monitor"
		table.EmptyText = "That task has finished: it is no longer running, and finished runs are kept only briefly."
		sections = []ui.Section{{
			Title:    "This task",
			Subtitle: "The run you followed, and any sub-agents it started.",
			Detail:   "It refreshes every 3 seconds, and disappears shortly after it finishes.",
			Body:     table,
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
