package orchestrate

// ✨ Fix in a Scheduler item's View: the shared assist (core/assist,
// ui.AssistPanel), the schedule's own words on the left and on the right the
// worker, already told what the schedule is for, why its card says it is in
// trouble (parked, failing again and again, stopped by a guardrail), and what
// its last runs said. Offered on every item, not only a flagged one: a run
// can end OK and fall short the same way every time (out of rounds, a step it
// never reaches), which no flag counts and the owner sees run after run.
//
// It proposes, the owner saves. What it suggests fills the form; the form's
// Save changes the schedule. Its one tool reads a run of this schedule. So it
// either rewrites what the schedule does, or says what only the owner can
// fix and where: a deleted agent (Relink on the row), a credential that
// lapsed, a site refused by outbound control, a guardrail rule, the timing or
// the goal (Edit on the row).
//
// The form holds only what a schedule DOES (a recurring task's name and
// directive, a scheduled agent's mission, a monitor's check and brief) and
// saves through schedFixFormSave, which sends those fields, with the
// schedule's STORED timing and goal, to the row's own update. The whole
// record its edit endpoint reads back is not safe to post back: a recurring
// task is refused (its empty active window), a monitor refuses fields its
// kind does not own, and a scheduled agent's timing is rewritten. A recurring
// task is found by its lasting identity (its uid), since every save
// reschedules it under a new task id.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// schedFixAsk is what the window opens on: why it keeps failing when its card
// says it does, else what keeps going wrong in its runs.
const (
	schedFixAsk       = "Why does this keep failing, and what would fix it?"
	schedFixAskRepeat = "What keeps going wrong in its runs, and what would fix it?"
)

// schedFixRuns is how many recent runs the assistant is shown.
const schedFixRuns = 5

// schedFixRunChars bounds one run's output in what the assistant is shown;
// schedule_run reads one whole.
const schedFixRunChars = 1500

// schedFixTarget is one Scheduler item being fixed, found fresh on every
// request.
type schedFixTarget struct {
	kind string
	key  string // what the fix's URLs name it by: a recurring task's uid, else its name
	id   string // what its row and edit endpoints name it by now
	row  map[string]any

	recurring orchUpdatePayload
	standing  StandingAgent
	monitor   EventMonitor
}

// findSchedFix finds user's item kind/key. A recurring task is found by its
// task id or its uid.
func findSchedFix(user string, udb Database, kind, key string) (schedFixTarget, bool) {
	key = strings.TrimSpace(key)
	t := schedFixTarget{kind: kind, key: key, id: key}
	if user == "" || key == "" {
		return t, false
	}
	var row any
	switch kind {
	case schedKindRecurring:
		found := false
		for _, rt := range listAgentRecurringTasks(user, "") {
			if rt.TaskID == key || recurringTaskUID(rt.Payload) == key {
				t.recurring, t.id, t.key, found = rt.Payload, rt.TaskID, recurringTaskUID(rt.Payload), true
				break
			}
		}
		if !found {
			return t, false
		}
		for _, r := range consoleRecurringRows(user, "") {
			if r.ID == t.id {
				row = r
			}
		}
	case schedKindStanding:
		sa, ok := GetStandingAgent(RootDB, user, key)
		if !ok {
			return t, false
		}
		t.standing = sa
		for _, r := range consoleAgentRows(user, udb, "", newStandingRunIndex(user)) {
			if r.ID == sa.Name {
				row = r
			}
		}
	case schedKindMonitor:
		m, ok := GetEventMonitor(RootDB, user, key)
		if !ok {
			return t, false
		}
		t.monitor = m
		for _, r := range consoleMonitorRows(user, "") {
			if r.ID == m.Name {
				row = r
			}
		}
	default:
		return t, false
	}
	if row != nil {
		t.row = schedulerRow(row, "", kind)
	}
	return t, true
}

// flagged reports whether the item's card says it needs attention.
func (t schedFixTarget) flagged() bool { return schedStr(t.row, "_warn_why") != "" }

// trouble is why the item needs attention, as its card says it, for the
// assistant.
func (t schedFixTarget) trouble() string {
	if why := schedStr(t.row, "_warn_why"); why != "" {
		return why
	}
	return "its card says nothing is wrong, so look in its last runs for a problem that repeats"
}

// subtitle is the window's line under its title.
func (t schedFixTarget) subtitle() string {
	if t.flagged() {
		return schedStr(t.row, "_warn_why")
	}
	return "Its last runs, read for what keeps going wrong."
}

// ask is what the window opens on.
func (t schedFixTarget) ask() string {
	if t.flagged() {
		return schedFixAsk
	}
	return schedFixAskRepeat
}

// name is the item's title as the Scheduler shows it.
func (t schedFixTarget) name() string {
	switch t.kind {
	case schedKindRecurring:
		return recurringName(t.recurring)
	case schedKindStanding:
		return t.standing.Name
	}
	return t.monitor.Name
}

// agentID is the agent the schedule runs as or wakes.
func (t schedFixTarget) agentID() string {
	switch t.kind {
	case schedKindRecurring:
		return t.recurring.AgentID
	case schedKindStanding:
		return firstNonEmptyStr(t.standing.AgentID, t.standing.ReportAgentID)
	}
	return t.monitor.WakeAgent
}

// monitorFields are the words a monitor's form holds: its check when an agent
// judges it, its brief when it wakes one. A monitor refuses an edit to a
// field its kind does not own, so only these are sent.
func monitorFields(m EventMonitor) (check, brief bool) {
	return strings.TrimSpace(m.CheckAgent) != "", monitorWakesAgent(m)
}

// formRecord is what the Fix form edits, read fresh.
func (t schedFixTarget) formRecord() map[string]any {
	switch t.kind {
	case schedKindRecurring:
		return map[string]any{"name": strings.TrimSpace(t.recurring.Name), "prompt": t.recurring.Prompt}
	case schedKindStanding:
		return map[string]any{"mission": t.standing.Mission}
	}
	out := map[string]any{}
	check, brief := monitorFields(t.monitor)
	if check {
		out["check"] = t.monitor.Check
	}
	if brief {
		out["wake_brief"] = t.monitor.WakeBrief
	}
	return out
}

// formFields are the Fix form's fields.
func (t schedFixTarget) formFields() []ui.FormField {
	switch t.kind {
	case schedKindRecurring:
		return []ui.FormField{
			{Field: "name", Type: "text", Label: "Name", Help: "The card's title. Left empty, the directive's first line."},
			{Field: "prompt", Type: "textarea", Label: "What it does each time", Help: "The directive the agent is given on every run."},
		}
	case schedKindStanding:
		return []ui.FormField{
			{Field: "mission", Type: "textarea", Label: "What it does each time", Help: "The mission the agent is given on every run."},
		}
	}
	var out []ui.FormField
	check, brief := monitorFields(t.monitor)
	if check {
		out = append(out, ui.FormField{Field: "check", Type: "textarea", Label: "What it checks", Help: "The question its checker agent answers each time."})
	}
	if brief {
		out = append(out, ui.FormField{Field: "wake_brief", Type: "textarea", Label: "What the woken agent is told", Help: "The guidance handed to the agent it wakes."})
	}
	if len(out) == 0 {
		out = append(out, ui.FormField{Type: "header", Label: "Nothing here is written in words to change: the assistant says what to change, and where."})
	}
	return out
}

// updatePath is the row's own update endpoint, for its current id.
func (t schedFixTarget) updatePath() string {
	switch t.kind {
	case schedKindRecurring:
		return "/api/console/recurring/update"
	case schedKindStanding:
		return "/api/console/agents/update"
	}
	return "/api/console/monitors/update"
}

// saveBody is the body the row's update takes for the edited fields: those
// fields from what was posted, and the timing and goal as stored, so a save
// here changes what the schedule does and nothing about when.
func (t schedFixTarget) saveBody(posted map[string]any) map[string]any {
	str := func(k string) (string, bool) {
		v, ok := posted[k].(string)
		return v, ok
	}
	out := map[string]any{}
	switch t.kind {
	case schedKindRecurring:
		if v, ok := str("name"); ok {
			out["name"] = v
		}
		if v, ok := str("prompt"); ok {
			out["prompt"] = v
		}
		out["timing"] = recurringTimingOut(t.recurring)
		out["until"] = recurringUntilOut(t.recurring)
	case schedKindStanding:
		if v, ok := str("mission"); ok {
			out["mission"] = v
		}
		out["timing"] = standingTimingOut(t.standing)
		out["until"] = standingUntilOut(t.standing, true)
	case schedKindMonitor:
		check, brief := monitorFields(t.monitor)
		if v, ok := str("check"); ok && check {
			out["check"] = v
		}
		if v, ok := str("wake_brief"); ok && brief {
			out["wake_brief"] = v
		}
	}
	return out
}

// fixQuery is the query every Fix URL carries.
func (t schedFixTarget) fixQuery() string {
	return "kind=" + url.QueryEscape(t.kind) + "&id=" + url.QueryEscape(t.key)
}

// schedFixAbout is what the assistant is told before anything else.
func schedFixAbout(user string, t schedFixTarget, agentName string) string {
	var b strings.Builder
	b.WriteString("The Scheduler lists the work the owner's agents do on their own: recurring tasks (a directive an agent runs on a cadence), scheduled agents (an agent woken on a schedule with a mission) and monitors (a check run on an interval that wakes an agent when it fires). A schedule that keeps failing is parked after enough failures in a row, and one stopped again and again by a guardrail is flagged; either one needs the owner.\n\n")
	fmt.Fprintf(&b, "The owner is fixing this %s: %q.\n", map[string]string{schedKindRecurring: "recurring task", schedKindStanding: "scheduled agent", schedKindMonitor: "monitor"}[t.kind], t.name())
	if agentName != "" {
		fmt.Fprintf(&b, "It runs as, or wakes, the agent %q.\n", agentName)
	} else if t.agentID() != "" {
		fmt.Fprintf(&b, "The agent it names (%s) no longer exists.\n", t.agentID())
	}
	switch t.kind {
	case schedKindRecurring:
		fmt.Fprintf(&b, "Cadence: %s.\nWhat it does each time:\n%s\n", recurringDetail(t.recurring), strings.TrimSpace(t.recurring.Prompt))
		if lbl := objectiveStateLabel(t.recurring.objective()); lbl != "" {
			b.WriteString("Its goal: " + strings.TrimSpace(t.recurring.objective().Until) + " (" + lbl + ")\n")
		}
	case schedKindStanding:
		fmt.Fprintf(&b, "Schedule: %s.\nWhat it does each time:\n%s\n", StandingScheduleLabel(t.standing), strings.TrimSpace(t.standing.Mission))
		if lbl := objectiveStateLabel(standingObjective(t.standing)); lbl != "" {
			b.WriteString("Its goal: " + lbl + "\n")
		}
	case schedKindMonitor:
		fmt.Fprintf(&b, "Kind: %s.\n", t.monitor.Kind)
		if c := strings.TrimSpace(t.monitor.Check); c != "" {
			b.WriteString("What it checks: " + c + "\n")
		}
		if w := strings.TrimSpace(t.monitor.WakeBrief); w != "" {
			b.WriteString("What the woken agent is told: " + w + "\n")
		}
	}
	b.WriteString("Why it needs attention: " + t.trouble() + ".\n")

	if view, ok := schedulerViewFor(user, t.kind, t.id); ok {
		if runs, _ := view["runs"].([]schedulerViewRun); len(runs) > 0 {
			if len(runs) > schedFixRuns {
				runs = runs[:schedFixRuns]
			}
			b.WriteString("\nIts last runs, newest first (schedule_run reads one whole, with each tool it called and what came back):\n")
			for _, r := range runs {
				fmt.Fprintf(&b, "- %s, %s", r.When, r.Status)
				if r.ID != "" {
					fmt.Fprintf(&b, " (run %s)", r.ID)
				}
				b.WriteString(":\n  " + strings.ReplaceAll(truncateObs(r.Summary, schedFixRunChars), "\n", "\n  ") + "\n")
			}
		}
		if checks, _ := view["checks"].([]schedulerViewCheck); len(checks) > 0 {
			b.WriteString("\nIts last checks:\n")
			for i, c := range checks {
				if i >= schedFixRuns {
					break
				}
				fmt.Fprintf(&b, "- %s %s: %s\n", c.When, c.Verdict, truncateObs(c.Note, 300))
			}
		}
	}

	b.WriteString("\nThe form beside this conversation holds what the schedule does, in its own words. You fill it; the owner saves it, and only then does the schedule change. Saving changes nothing about when it runs. " + t.afterSave() + "\n\n" +
		"How to work: find why it fails from its runs (read one whole with schedule_run when the summary does not say). A run can end OK and still fall short the same way every time (it runs out of rounds, skips a step, says it could not finish): a shortfall that repeats is what to fix, as much as an error. Then do one of two things. " +
		"When its words are the cause (a step it cannot do, a tool it no longer has, an instruction it keeps misreading, a goal it cannot meet as written), propose new words in the form and say what you changed and why. " +
		"When the cause is outside its words, say exactly what the owner has to do and where, and change nothing in the form: an agent that was deleted (Relink on the row); a credential that lapsed or lacks a permission (the owner's credentials, under Extensions); a site the network rules refused (the agent's Security page, Network); a guardrail rule that keeps stopping it (that rule, on the agent's Guardrails); when it runs, or the goal it stops at (Edit on the row). " +
		"Never propose words that hide a failure, such as telling it to ignore errors or to report success.")
	return b.String()
}

// afterSave says what a save does to a parked item, which differs by kind: the
// recurring update reschedules the task, so it runs again; a scheduled agent
// or a monitor stays parked until Resume (or Relink) on its row.
func (t schedFixTarget) afterSave() string {
	if t.kind == schedKindRecurring {
		return "Saving a parked recurring task also puts it back on its schedule; one whose agent is gone parks again on its next run."
	}
	return "A parked one stays parked after a save: Resume on its row starts it again, or Relink when its agent was deleted."
}

// schedFixTools are the assistant's tools: one, reading a run of this
// schedule. Nothing it has saves.
func schedFixTools(user string, t schedFixTarget) []AgentToolDef {
	return []AgentToolDef{{
		Tool: Tool{
			Name:        "schedule_run",
			Description: "One run of the schedule being fixed, whole: its status, its error, each tool it called with the arguments and what came back, and its full output. Give a run id from the list of its last runs.",
			Parameters: map[string]ToolParam{
				"run": {Type: "string", Description: "The run's id, from the list of its last runs."},
			},
			Required: []string{"run"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["run"].(string)
			if !schedFixOwnsRun(user, t, strings.TrimSpace(id)) {
				return "", fmt.Errorf("run %q is not one of this schedule's recent runs", id)
			}
			rec, ok := GetRun(RootDB, user, strings.TrimSpace(id))
			if !ok {
				return "", fmt.Errorf("run %q was not found", id)
			}
			b, err := json.MarshalIndent(consoleRunDetailOf(rec, UserLocation(user)), "", " ")
			if err != nil {
				return "", err
			}
			return SpillOutput(string(b), 14000, "read_output"), nil
		},
	}}
}

// schedFixOwnsRun reports whether run id is one of the schedule's recent
// runs, so the tool reads this schedule and nothing else of the owner's.
func schedFixOwnsRun(user string, t schedFixTarget, id string) bool {
	if id == "" {
		return false
	}
	view, ok := schedulerViewFor(user, t.kind, t.id)
	if !ok {
		return false
	}
	runs, _ := view["runs"].([]schedulerViewRun)
	for _, r := range runs {
		if r.ID == id {
			return true
		}
	}
	return false
}

// registerSchedFixAssist puts the assist on its route. It speaks the assist
// protocol (GET asks whether a worker is there to help, POST is a turn), so
// it is registered here rather than among the console's JSON routes, whose
// test holds every route to one method.
func (T *OrchestrateApp) registerSchedFixAssist() {
	T.HandleFunc("/api/console/scheduler/fix", T.handleSchedFix)
}

// handleSchedFix is the assist: POST /api/console/scheduler/fix?kind=&id=
// (GET: whether help is available). Signed out is refused before anything.
func (T *OrchestrateApp) handleSchedFix(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	udb := UserDB(T.DB, user)
	q := r.URL.Query()
	assist.Handler(assist.Spec{
		Route: "app.orchestrate.scheduler_fix", Label: "Scheduler: fix a schedule", App: "/orchestrate",
		Core: &T.AppCore, Think: assist.Off, Timeout: 3 * time.Minute,
		Allow: func(w http.ResponseWriter, r *http.Request) bool {
			if _, ok := findSchedFix(user, udb, q.Get("kind"), q.Get("id")); !ok {
				http.Error(w, "schedule not found", http.StatusNotFound)
				return false
			}
			return true
		},
		About: func(*http.Request) (string, error) {
			t, ok := findSchedFix(user, udb, q.Get("kind"), q.Get("id"))
			if !ok {
				return "", assist.Refuse(http.StatusNotFound, "schedule not found")
			}
			name := ""
			if rec, found := loadAgent(udb, t.agentID()); found {
				name = chFirst(rec.Name, rec.ID)
			}
			return schedFixAbout(user, t, name), nil
		},
		Tools: func(*http.Request) []AgentToolDef {
			t, ok := findSchedFix(user, udb, q.Get("kind"), q.Get("id"))
			if !ok {
				return nil
			}
			return schedFixTools(user, t)
		},
	})(w, r)
}

// schedFixPanel is GET /api/console/scheduler/fix-panel?kind=&id=: the
// window a row's ✨ Fix opens, as a component.
func (T *OrchestrateApp) handleSchedFixPanel(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	t, ok := findSchedFix(c.User, c.UDB, q.Get("kind"), q.Get("id"))
	if !ok {
		return nil, jsonapi.NotFound("schedule not found")
	}
	form := "api/console/scheduler/fix-form?" + t.fixQuery()
	return map[string]any{
		"title":    "Fix: " + t.name(),
		"subtitle": t.subtitle(),
		"width":    ui.AssistWidth,
		"panel": ui.AssistPanel{
			Work: ui.FormPanel{Source: form, PostURL: form, Method: "POST", SubmitLabel: "Save", Fields: t.formFields()},
			URL:  "api/console/scheduler/fix?" + t.fixQuery(),
			Ask:  t.ask(), Web: true,
			Intro:       "It knows this schedule, why it is in trouble and what its last runs said. It either rewrites what the schedule does, in the form, or says what only you can fix and where. Nothing changes until you save.",
			Placeholder: "Ask about it, or say what to try...",
		},
	}, nil
}

// handleSchedFixFormGet is GET /api/console/scheduler/fix-form: what the form
// edits, read fresh.
func (T *OrchestrateApp) handleSchedFixFormGet(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	t, ok := findSchedFix(c.User, c.UDB, q.Get("kind"), q.Get("id"))
	if !ok {
		return nil, jsonapi.NotFound("schedule not found")
	}
	return t.formRecord(), nil
}

// handleSchedFixFormSave is POST /api/console/scheduler/fix-form: the edited
// fields, with the stored timing and goal, sent on to the row's own update
// for the item's current id, as the caller. Its answer is the update's.
func (T *OrchestrateApp) handleSchedFixFormSave(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	t, ok := findSchedFix(user, UserDB(T.DB, user), q.Get("kind"), q.Get("id"))
	if !ok {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	var posted map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&posted); err != nil {
		http.Error(w, "the form's fields, as JSON", http.StatusBadRequest)
		return
	}
	body, err := json.Marshal(t.saveBody(posted))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fwd := r.Clone(r.Context())
	u := *r.URL
	u.Path = strings.TrimSuffix(r.URL.Path, "/api/console/scheduler/fix-form") + t.updatePath()
	u.RawQuery = "id=" + url.QueryEscape(t.id)
	fwd.URL, fwd.RequestURI = &u, ""
	fwd.Body, fwd.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	fwd.Header.Set("Content-Type", "application/json")
	switch t.kind {
	case schedKindRecurring:
		jsonapi.Post(T.DB, T.handleConsoleRecurringUpdate)(w, fwd)
	case schedKindStanding:
		jsonapi.Post(T.DB, T.handleConsoleAgentUpdate)(w, fwd)
	default:
		jsonapi.Post(T.DB, T.handleConsoleMonitorUpdate)(w, fwd)
	}
}
