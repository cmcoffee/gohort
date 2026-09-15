package orchestrate

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// consoleRunRow is one card in the Runs pane: the ledger's metadata for a run
// (never Raw / Steps — those are GetRun-only and travel in the Details modal).
// Field order is display order: the first field is the card title, Status the
// pill, the rest muted detail. _id is the Details row action's target.
//
// The agent leads. This pane draws from every agent the owner has, and the
// first question about any row in a list like that is whose it is; the
// schedule that fired it answers the second, on its own line.
type consoleRunRow struct {
	Agent   string `json:"agent"`
	Task    string `json:"task,omitempty"`
	Status  string `json:"Status"`
	When    string `json:"when"`
	Trigger string `json:"trigger,omitempty"`
	Brief   string `json:"brief,omitempty"`
	Summary string `json:"summary,omitempty"`
	ID      string `json:"_id"`
}

// consoleRunAgent / consoleRunTask split a run's identity in two, for the
// fleet feed where the agent leads and the schedule that fired it follows.
// Together they carry what consoleRunTitle packs into one string.
func consoleRunAgent(rec RunRecord) string { return rec.Agent }

func consoleRunTask(rec RunRecord) string {
	if task := strings.TrimSpace(rec.Task); task != "" && task != rec.Agent {
		return task
	}
	return ""
}

// handleConsoleRuns returns the run-ledger feed (owner-scoped, status-level)
// shaped for the Runs cards pane, newest first.
func (T *OrchestrateApp) handleConsoleRuns(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	loc := UserLocation(user)
	// Narrowing arrives from a summary figure that linked here — a count of
	// failures handing over the scope it counted. The pane's own button sends
	// neither, so opening it directly still lists everything.
	//
	// An agent's runs are gathered through agentRuns rather than a ledger
	// filter: a run is filed under the thing that FIRED it, so asking for the
	// agent alone misses everything its schedules, monitors and recurring tasks
	// did on its behalf — which is most of what a failure count is counting.
	status := RunStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	runs := []RunRecord{}
	if agentID := strings.TrimSpace(r.URL.Query().Get("agent")); agentID != "" {
		runs = agentRuns(user, udb, agentID, agentDisplayName(udb, user, agentID))
	} else {
		runs = ListRuns(RootDB, user, RunFilter{Limit: 100})
	}
	rows := []consoleRunRow{}
	for _, rec := range runs {
		if status != "" && rec.Status != status {
			continue
		}
		rows = append(rows, consoleRunRow{
			Agent:   consoleRunAgent(rec),
			Task:    consoleRunTask(rec),
			Status:  string(rec.Status),
			When:    consoleRunWhen(rec, loc),
			Trigger: rec.Trigger,
			Brief:   truncateObs(rec.Brief, 120),
			Summary: truncateObs(firstNonEmpty(rec.Summary, rec.Err), 160),
			ID:      rec.ID,
		})
	}
	writeJSON(w, rows)
}

// consoleRunTitle names a run the way its owner thinks of it: the schedule
// that fired, when one did, else the agent / standing name the ledger holds.
func consoleRunTitle(rec RunRecord) string {
	if task := strings.TrimSpace(rec.Task); task != "" && task != rec.Agent {
		return task + " → " + rec.Agent
	}
	return rec.Agent
}

// consoleRunWhen renders start time (in the owner's zone) plus duration, or
// "running" for a row the ledger has not closed.
func consoleRunWhen(rec RunRecord, loc *time.Location) string {
	when := rec.Started.In(loc).Format("Jan 2 15:04")
	if rec.Ended.IsZero() {
		return when + ", running"
	}
	d := rec.Ended.Sub(rec.Started).Round(time.Second)
	if d < time.Second {
		return when
	}
	return when + ", " + d.String()
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// consoleRunDetail is the Details modal's view of one run — the full record
// GetRun rehydrates (steps, output, prompt), with field order chosen for a
// reader: what ran and how it ended first, the trace next, the bulk last.
type consoleRunDetail struct {
	Run       string            `json:"Run"`
	Status    string            `json:"Status"`
	Trigger   string            `json:"Trigger,omitempty"`
	Started   string            `json:"Started"`
	Ended     string            `json:"Ended,omitempty"`
	Duration  string            `json:"Duration,omitempty"`
	Brief     string            `json:"Brief,omitempty"`
	Summary   string            `json:"Summary,omitempty"`
	Error     string            `json:"Error,omitempty"`
	Steps     []consoleRunStep  `json:"Steps,omitempty"`
	Artifacts []RunArtifact     `json:"Artifacts,omitempty"`
	Output    string            `json:"Output,omitempty"`
	Prompt    *consoleRunPrompt `json:"Prompt,omitempty"`
}

type consoleRunStep struct {
	Step   string `json:"step"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// consoleRunPrompt is the prompt digest a reader can act on, plus the exact
// text when the agent's CapturePrompt flag kept it.
type consoleRunPrompt struct {
	SystemTokens  int    `json:"system_tokens,omitempty"`
	ToolCount     int    `json:"tools,omitempty"`
	ToolTokens    int    `json:"tool_tokens,omitempty"`
	Messages      int    `json:"messages,omitempty"`
	HistoryTokens int    `json:"history_tokens,omitempty"`
	Window        int    `json:"window,omitempty"`
	Budget        int    `json:"history_budget,omitempty"`
	Headroom      int    `json:"headroom,omitempty"`
	Tight         bool   `json:"tight,omitempty"`
	Text          string `json:"as_sent,omitempty"`
}

// handleConsoleRunDetail serves one run's full record for the Details modal.
// An unknown or foreign id answers an empty object, which the modal reads as
// "gone", rather than confirming the id exists.
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
	writeJSON(w, consoleRunDetailOf(rec, UserLocation(user)))
}

func consoleRunDetailOf(rec RunRecord, loc *time.Location) consoleRunDetail {
	d := consoleRunDetail{
		Run:       consoleRunTitle(rec),
		Status:    string(rec.Status),
		Trigger:   rec.Trigger,
		Started:   rec.Started.In(loc).Format("Jan 2, 2006 15:04:05"),
		Brief:     rec.Brief,
		Summary:   rec.Summary,
		Error:     rec.Err,
		Artifacts: rec.Artifacts,
		Output:    rec.Raw,
	}
	if !rec.Ended.IsZero() {
		d.Ended = rec.Ended.In(loc).Format("Jan 2, 2006 15:04:05")
		d.Duration = rec.Ended.Sub(rec.Started).Round(time.Second).String()
	}
	// The summary usually IS the output for a run that produced one thing;
	// showing it twice makes the modal longer without saying more.
	if strings.TrimSpace(d.Output) == strings.TrimSpace(d.Summary) {
		d.Output = ""
	}
	for _, st := range rec.Steps {
		d.Steps = append(d.Steps, consoleRunStep{Step: st.Name, Args: st.Args, Result: st.Result, Error: st.Err})
	}
	if p := rec.Prompt; p.SystemTokens > 0 || p.Messages > 0 || strings.TrimSpace(p.Text) != "" {
		d.Prompt = &consoleRunPrompt{
			SystemTokens: p.SystemTokens, ToolCount: p.ToolCount, ToolTokens: p.ToolTokens,
			Messages: p.Messages, HistoryTokens: p.HistoryTokens, Window: p.Window,
			Budget: p.Budget, Headroom: p.Headroom, Tight: p.Tight, Text: p.Text,
		}
	}
	return d
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
	// Cancellable gates the Cancel action. Distinct from Running, because they
	// were silently different: every running row offered the button and most of
	// them had nothing behind it.
	Cancellable bool `json:"_cancellable,omitempty"`
}

// runOwnerDestination is the conversation a run's owner rejoins from the live
// pill, or "" when the run has no conversation to rejoin.
//
// Only a ROOT run qualifies: a dispatched sub-agent's session id names a
// sub-session, not a thread the chat page can open, and the thread it belongs
// to is its parent's, which is already listed. (The sub-agent's diagnostics
// are mirrored into that parent thread's trail, tagged with its name — see
// session_diag.go, diagParentKey — so nothing is lost by not linking here.) A root run with a session id
// — a chat turn, a scheduled or standing fire waking a thread, a channel
// turn — opens that thread, where the panel's resume probe finds the run.
func runOwnerDestination(prefix string, s RunSnapshot) string {
	if s.Depth != 0 || strings.TrimSpace(s.SessionID) == "" || strings.TrimSpace(s.AgentID) == "" {
		return ""
	}
	return strings.TrimSuffix(prefix, "/") + "/?agent=" + url.QueryEscape(s.AgentID) + "&session=" + url.QueryEscape(s.SessionID)
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
			Agent:       runIndentPrefix(s.Depth) + name,
			Activity:    activity,
			Brief:       s.Label,
			Surface:     surface,
			ID:          s.ID,
			Running:     s.Status == RunStatusRunning,
			Cancellable: s.Status == RunStatusRunning && s.Cancellable,
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
	// A run with no cancel func behind it cannot be stopped, and saying so is
	// the point: this answered 204 either way, so the button reported success
	// on work that carried right on. Rows now offer Cancel only where it does
	// something (_cancellable), and this is the backstop for a stale row.
	if !run.Cancel() {
		http.Error(w, "this run cannot be cancelled — it is not running under a stoppable context", http.StatusConflict)
		return
	}
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
