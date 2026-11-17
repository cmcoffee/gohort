package customapps

// Reporting a problem with an app someone shared with you. The owner and the
// app's maintainers change it with Builder; everyone else it is shared with
// had no way to say it was broken. They get "Report a problem" in the app's
// header: a message, and a "Send diagnostic information" box. With the box
// ticked the app is loaded again as them, in a page check's scratch store
// seeded with their records and their saved view (so the check sees what they
// see and changes nothing of theirs), and an assistant reads what it found
// (errors, failed requests, what the page showed, a screenshot, their failed
// runs) and writes the owner a short diagnosis. The report waits on the
// owner's Reports page, beside Recent runs, with a bell notice naming who sent
// it, and goes from there to Builder as a Fix with the problem filled in.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/tools/appscript"
)

const (
	reportsTable = "app_reports"
	shotsTable   = "app_report_shots"
	// reportMessageMax is the longest message a report keeps, in characters.
	reportMessageMax = 4000
	// reportsPerDay is how many reports one person may send about one app in
	// a day: enough to report a second thing, not enough to flood the owner.
	reportsPerDay = 3
	// reportsKept is how many reports one app keeps; the oldest go first.
	reportsKept = 50
	// reportClip bounds each piece of evidence a report keeps and the
	// assistant reads.
	reportClip = 6000
	// reportNavAction is the client action the header link runs.
	reportNavAction = "app_report_problem"
)

// appReport is one problem a person reported with an app.
type appReport struct {
	ID       string     `json:"id"`
	Slug     string     `json:"slug"`
	Reporter string     `json:"reporter"`
	At       time.Time  `json:"at"`
	Message  string     `json:"message"`
	Closed   bool       `json:"closed,omitempty"`
	Diag     reportDiag `json:"diag"`
}

// reportDiag is the diagnostic information a report carries, when its
// sender ticked the box.
type reportDiag struct {
	// State is "" (not asked for), "running", "done" or "failed".
	State          string    `json:"state,omitempty"`
	Started        time.Time `json:"started,omitempty"`
	Why            string    `json:"why,omitempty"` // why it failed
	PageErrors     []string  `json:"page_errors,omitempty"`
	ConsoleErrors  []string  `json:"console_errors,omitempty"`
	FailedRequests []string  `json:"failed_requests,omitempty"`
	Pending        []string  `json:"pending,omitempty"`
	Shown          string    `json:"shown,omitempty"`        // what the page showed them
	View           string    `json:"view,omitempty"`         // where they were in it
	RunFailures    []string  `json:"run_failures,omitempty"` // their recent failed runs
	Analysis       string    `json:"analysis,omitempty"`
	AnalysisWhy    string    `json:"analysis_why,omitempty"` // why there is none
	HasShot        bool      `json:"has_shot,omitempty"`
}

// reportsMu orders the read-modify-write of an app's reports: a diagnosis
// finishing while the owner closes the report must not lose either.
var reportsMu sync.Mutex

// reportStore is where an owner's app reports live.
func (T *CustomApps) reportStore(owner string) Database {
	return UserDB(T.DB, owner)
}

// loadReports is an app's reports, newest first.
func (T *CustomApps) loadReports(owner, slug string) []appReport {
	var out []appReport
	T.reportStore(owner).Get(reportsTable, slug, &out)
	return out
}

func (T *CustomApps) saveReports(owner, slug string, reports []appReport) {
	db := T.reportStore(owner)
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].At.After(reports[j].At) })
	for len(reports) > reportsKept {
		db.Unset(shotsTable, reports[len(reports)-1].ID)
		reports = reports[:len(reports)-1]
	}
	if len(reports) == 0 {
		db.Unset(reportsTable, slug)
		return
	}
	db.Set(reportsTable, slug, reports)
}

// updateReport changes one report under the lock; false when it is gone.
func (T *CustomApps) updateReport(owner, slug, id string, fn func(*appReport)) bool {
	reportsMu.Lock()
	defer reportsMu.Unlock()
	reports := T.loadReports(owner, slug)
	for i := range reports {
		if reports[i].ID == id {
			fn(&reports[i])
			T.saveReports(owner, slug, reports)
			return true
		}
	}
	return false
}

// canReport is whether user reports problems with the app rather than
// changing it: anyone it is shared with but its owner and maintainers.
func canReport(spec AppSpec, owner, user string) bool {
	return user != "" && user != owner && !spec.MaintainedBy(user)
}

// withReportLink adds "Report a problem" to an app page's header nav. It runs
// the dialog in reportDialogJS rather than going anywhere.
func withReportLink(page json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(page, &m) != nil || m == nil {
		return page
	}
	nav, _ := m["nav"].([]any)
	m["nav"] = append(nav, map[string]any{"label": "Report a problem", "url": "#", "action": reportNavAction})
	out, err := json.Marshal(m)
	if err != nil {
		return page
	}
	return out
}

// withOpenReportsLink tells the owner, in the header, that people have
// reported problems not yet closed.
func withOpenReportsLink(page json.RawMessage, base string, reports []appReport) json.RawMessage {
	open := 0
	for _, r := range reports {
		if !r.Closed {
			open++
		}
	}
	switch open {
	case 0:
		return page
	case 1:
		return withNavLink(page, "⚠ A problem was reported: see it", base+"/_reports")
	default:
		return withNavLink(page, fmt.Sprintf("⚠ %d problems reported: see them", open), base+"/_reports")
	}
}

// reportIn is what the dialog sends.
type reportIn struct {
	Message     string `json:"message"`
	Diagnostics bool   `json:"diagnostics"`
}

// startReportDiagnosis runs a report's diagnosis in the background; tests
// run it in place.
var startReportDiagnosis = func(fn func()) { go fn() }

// reportProblem files a report from someone the app is shared with, tells
// the owner, and starts the diagnosis when it was asked for.
func (T *CustomApps) reportProblem(a *appCall, in reportIn) (any, error) {
	if !canReport(a.spec, a.owner, a.User) {
		return nil, jsonapi.Forbidden("this app is yours to change: use Change with Builder")
	}
	if _, isCheck, _ := appCheckScratch(a.R, a.spec, a.owner); isCheck {
		return nil, errNotInCheck
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		return nil, jsonapi.BadRequest("say what went wrong")
	}
	if r := []rune(msg); len(r) > reportMessageMax {
		msg = string(r[:reportMessageMax]) + "..."
	}
	now := time.Now()
	rep := appReport{ID: newID(), Slug: a.spec.Slug, Reporter: a.User, At: now, Message: msg}
	if in.Diagnostics {
		rep.Diag = reportDiag{State: "running", Started: now}
	}

	reportsMu.Lock()
	reports := T.loadReports(a.owner, a.spec.Slug)
	sent := 0
	for _, r := range reports {
		if r.Reporter == a.User && now.Sub(r.At) < 24*time.Hour {
			sent++
		}
	}
	if sent >= reportsPerDay {
		reportsMu.Unlock()
		return nil, jsonapi.Status(http.StatusTooManyRequests,
			fmt.Sprintf("you have reported %d problems with this app today, and its owner has them all; send another tomorrow", sent))
	}
	T.saveReports(a.owner, a.spec.Slug, append([]appReport{rep}, reports...))
	reportsMu.Unlock()

	T.noticeReport(a.owner, a.spec, rep)
	Log("[app-report] %s reported a problem with %s's app %s (diagnostics: %v)", a.User, a.owner, a.spec.Slug, in.Diagnostics)
	if in.Diagnostics {
		spec, owner, reporter, id := a.spec, a.owner, a.User, rep.ID
		startReportDiagnosis(func() { T.diagnoseReport(spec, owner, reporter, id) })
	}
	said := "Sent to the app's owner."
	if in.Diagnostics {
		said = "Sent to the app's owner. The app is being looked over as you see it, and what that finds goes with your report."
	}
	return map[string]any{"id": rep.ID, "said": said}, nil
}

// reporterName is how the owner knows the person: their display name and
// account, or the account alone when they have no display name.
func reporterName(user string) string {
	if AuthDB != nil {
		if db := AuthDB(); db != nil {
			if u, ok := AuthGetUser(db, user); ok && strings.TrimSpace(u.DisplayName) != "" {
				return strings.TrimSpace(u.DisplayName) + " (" + user + ")"
			}
		}
	}
	return user
}

// noticeReport puts the report on the owner's bell, naming who sent it.
func (T *CustomApps) noticeReport(owner string, spec AppSpec, rep appReport) {
	if RootDB == nil {
		return
	}
	name := spec.Name
	if strings.TrimSpace(name) == "" {
		name = spec.Slug
	}
	base := strings.TrimRight(DashboardURL(), "/")
	title := fmt.Sprintf("%s reported a problem with %s", reporterName(rep.Reporter), name)
	body := reportLine(rep.Message, 400) + "\n\nSee the report: " + base + T.WebPath() + "/" + spec.Slug + "/_reports"
	// The diagnosis is still running, so this link carries what they wrote
	// only; the Reports page's Fix with Builder adds what the check found.
	said := rep
	said.Message, said.Diag = reportLine(rep.Message, 400), reportDiag{}
	if fix := reportBuilderURL(spec.Slug, said); fix != "" {
		body += "\nFix with Builder: " + base + fix
	}
	if _, first := notices.Record(RootDB, notices.Notice{
		Owner: owner, About: "app:" + spec.Slug, Kind: notices.KindReport, Title: title, Body: body,
	}); first && NoticeForwarder != nil {
		go NoticeForwarder(owner, name, title, body)
	}
}

// diagnoseReport loads the app as the reporter, in a scratch store seeded
// with their records and saved view, keeps what the load found, and has the
// worker model read it. It runs on a context of its own: it outlives the
// request that started it.
func (T *CustomApps) diagnoseReport(spec AppSpec, owner, reporter, id string) {
	var d reportDiag
	d.State = "done"
	reporterDB := T.recordBase(spec, reporter)
	if BrowserCheckPage == nil {
		d.State, d.Why = "failed", "this server has no headless browser to load the app with"
	} else {
		token, end := appscript.BeginCheckScratch(owner, spec.Slug, spec.RecordKey, appscript.ReadRecords(reporterDB, spec.Slug))
		if sc, ok := appscript.CheckScratchFor(token); ok {
			if view := loadViewState(reporterDB, spec.Slug); len(view) > 0 {
				sc.DB.Set(viewStateTable, spec.Slug, view)
			}
		}
		rep, err := CheckPageAsUser(RootDB, reporter, T.WebPath()+"/"+spec.Slug+"/", "",
			PageCheckCookie{Name: appscript.CheckScratchCookie, Value: token, HTTPOnly: true})
		end()
		switch {
		case err != nil:
			d.State, d.Why = "failed", "the app could not be loaded: "+reportLine(err.Error(), 300)
		case rep == nil:
			d.State, d.Why = "failed", "the app could not be loaded"
		default:
			d.PageErrors = clipList(rep.PageErrors)
			d.ConsoleErrors = clipList(rep.ConsoleErrors)
			d.FailedRequests = clipList(rep.FailedRequests)
			// Only the app's own requests: the runtime's live-update
			// connection is always still open when the page settles.
			own := T.WebPath() + "/" + spec.Slug + "/"
			var pending []string
			for _, u := range rep.PendingRequests {
				if strings.Contains(u, own) {
					pending = append(pending, u)
				}
			}
			d.Pending = clipList(pending)
			shown := strings.TrimSpace(strings.Join(append([]string{rep.BodyText}, rep.FrameTexts...), "\n\n"))
			d.Shown = clipText(shown, reportClip)
			if len(rep.Screenshot) > 0 {
				T.reportStore(owner).Set(shotsTable, id, rep.Screenshot)
				d.HasShot = true
			}
		}
	}
	if view := loadViewState(reporterDB, spec.Slug); len(view) > 0 {
		b, _ := json.Marshal(view)
		d.View = clipText(string(b), 1500)
	}
	for _, r := range appscript.RecentRuns(owner, spec.Slug) {
		at, err := time.Parse(time.RFC3339, r.At)
		if !r.OK && r.Other && err == nil && time.Since(at) < 24*time.Hour {
			d.RunFailures = append(d.RunFailures, fmt.Sprintf("%s, %s: %s", runScriptLabel(r), at.Format("Jan 2 15:04 UTC"), reportLine(r.Error, 300)))
		}
	}
	var msg string
	var shot []byte
	if !T.updateReport(owner, spec.Slug, id, func(r *appReport) { msg = r.Message }) {
		return // deleted meanwhile
	}
	if d.HasShot {
		T.reportStore(owner).Get(shotsTable, id, &shot)
	}
	d.Analysis, d.AnalysisWhy = T.analyseReport(spec, msg, d, shot)
	T.updateReport(owner, spec.Slug, id, func(r *appReport) {
		d.Started = r.Diag.Started
		r.Diag = d
	})
	Log("[app-report] diagnosis of %s's report on %s/%s: %s", reporter, owner, spec.Slug, d.State)
}

// reportAnalysisPrompt is the assistant's job on a report.
const reportAnalysisPrompt = `You look over a problem report for an app. A person using the app said what went wrong, and the app was then loaded again as them: below is what that load found (errors the page raised, requests that failed, what the page showed), where they were in the app, recent failed runs of the app's scripts started by people it is shared with, and a screenshot when one could be taken.

Write a short diagnosis for the app's owner, in plain words:
1. What the person most likely ran into.
2. The evidence for it, quoted briefly.
3. Where in the app to look: a data source, an action, a field, the page's layout.

If the evidence does not explain the report, say so, and say what the report alone suggests. Never name an error the evidence does not show. At most 150 words, no headings.`

// analyseReport has the worker model read a report's evidence: the
// diagnosis, or "" and why there is none.
func (T *CustomApps) analyseReport(spec AppSpec, msg string, d reportDiag, shot []byte) (string, string) {
	if T.LLM == nil {
		return "", "no model is set up here to read it"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The app: %s\n\n", spec.Name)
	b.WriteString(textutil.UntrustedData("report", msg) + "\n\n")
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			fmt.Fprintf(&b, "%s: none\n\n", title)
			return
		}
		fmt.Fprintf(&b, "%s:\n- %s\n\n", title, strings.Join(lines, "\n- "))
	}
	if d.State == "failed" {
		fmt.Fprintf(&b, "The app could not be loaded again to check: %s\n\n", d.Why)
	} else {
		section("Errors the page raised", d.PageErrors)
		section("Console errors", d.ConsoleErrors)
		section("Requests that failed", d.FailedRequests)
		section("Requests still waiting when the page settled", d.Pending)
		b.WriteString(textutil.UntrustedData("page text", d.Shown) + "\n\n")
	}
	if d.View != "" {
		b.WriteString(textutil.UntrustedData("saved view", d.View) + "\n\n")
	}
	section("Failed script runs in the last day", d.RunFailures)
	m := Message{Role: "user", Content: b.String()}
	if len(shot) > 0 {
		m.Images = [][]byte{shot}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(AppContext()), 3*time.Minute)
	defer cancel()
	resp, err := T.WorkerChat(ctx, []Message{m}, WithSystemPrompt(reportAnalysisPrompt), WithMaxTokens(1200))
	if err != nil {
		return "", "the assistant could not read it: " + reportLine(err.Error(), 200)
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", "the assistant had nothing to say"
	}
	return out, ""
}

// reportLine is s on one line, at most max characters.
func reportLine(s string, max int) string {
	return textutil.Clip(strings.Join(strings.Fields(s), " "), max)
}

func clipList(in []string) []string {
	var out []string
	total := 0
	for _, s := range in {
		s = reportLine(s, 500)
		if total += len(s); total > reportClip {
			out = append(out, fmt.Sprintf("(%d more not kept)", len(in)-len(out)))
			break
		}
		out = append(out, s)
	}
	return out
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// reportBuilderURL opens Builder on a Fix of the app with the report as the
// problem. "" when no Builder runs here.
func reportBuilderURL(slug string, r appReport) string {
	orch := agenthost.Get()
	if orch == nil {
		return ""
	}
	problem := fmt.Sprintf("%s reported: %s", reporterName(r.Reporter), r.Message)
	if r.Diag.Analysis != "" {
		problem += "\n\nWhat the check of the app as they see it found: " + r.Diag.Analysis
	}
	if errs := append(append([]string{}, r.Diag.PageErrors...), r.Diag.FailedRequests...); len(errs) > 0 {
		problem += "\n\nErrors: " + strings.Join(errs, "; ")
	}
	problem = clipText(problem, 1800)
	return orch.WebPath() + "/?agent=seed-builder&intake.action=Fix&intake.kind=App&intake.target=" +
		url.QueryEscape(slug) + "&intake.problem=" + url.QueryEscape(problem)
}

// reportsList is the owner's Reports table.
func (T *CustomApps) reportsList(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.NotFound("no such page")
	}
	base := T.WebPath() + "/" + a.spec.Slug
	rows := []map[string]any{}
	for _, r := range T.loadReports(a.owner, a.spec.Slug) {
		row := map[string]any{
			"id":          r.ID,
			"reporter":    reporterName(r.Reporter),
			"at":          r.At,
			"message":     r.Message,
			"closed":      r.Closed,
			"status":      "open",
			"diag":        "none",
			"analysis":    r.Diag.Analysis,
			"builder_url": reportBuilderURL(a.spec.Slug, r),
		}
		if r.Closed {
			row["status"] = "closed"
		}
		switch r.Diag.State {
		case "running":
			row["diag"] = "running"
			row["diag_note"] = fmt.Sprintf("Loading the app as %s and reading what it shows, %ds so far", r.Reporter, int(time.Since(r.Diag.Started).Seconds()))
		case "failed":
			row["diag"] = "failed"
			row["diag_note"] = "Not checked: " + r.Diag.Why
		case "done":
			row["diag"] = "done"
		}
		if r.Diag.AnalysisWhy != "" && r.Diag.State != "running" {
			row["analysis"] = "No diagnosis: " + r.Diag.AnalysisWhy
		}
		join := func(field string, lines []string) {
			if len(lines) > 0 {
				row[field] = strings.Join(lines, "\n")
			}
		}
		join("page_errors", append(append([]string{}, r.Diag.PageErrors...), r.Diag.ConsoleErrors...))
		join("failed_requests", append(append([]string{}, r.Diag.FailedRequests...), r.Diag.Pending...))
		join("run_failures", r.Diag.RunFailures)
		if r.Diag.Shown != "" {
			row["shown"] = r.Diag.Shown
		}
		if r.Diag.View != "" {
			row["view"] = r.Diag.View
		}
		if r.Diag.HasShot {
			row["has_shot"] = "1"
			row["shot_url"] = base + "/_reports/" + r.ID + "/screenshot"
		}
		if row["builder_url"] != "" {
			row["has_builder"] = "1"
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// reportClose is the Closed toggle.
type reportClose struct {
	Closed bool `json:"closed"`
}

func (T *CustomApps) closeReport(a *appCall, in reportClose) (any, error) {
	if !a.mine() {
		return nil, jsonapi.NotFound("no such report")
	}
	if !T.updateReport(a.owner, a.spec.Slug, a.R.PathValue("id"), func(r *appReport) { r.Closed = in.Closed }) {
		return nil, jsonapi.NotFound("no such report")
	}
	return map[string]any{"closed": in.Closed}, nil
}

func (T *CustomApps) deleteReport(a *appCall, _ struct{}) (any, error) {
	if !a.mine() {
		return nil, jsonapi.NotFound("no such report")
	}
	id := a.R.PathValue("id")
	reportsMu.Lock()
	defer reportsMu.Unlock()
	reports := T.loadReports(a.owner, a.spec.Slug)
	kept := reports[:0]
	for _, r := range reports {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(reports) {
		return nil, jsonapi.NotFound("no such report")
	}
	T.reportStore(a.owner).Unset(shotsTable, id)
	T.saveReports(a.owner, a.spec.Slug, kept)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleReportShot serves a report's screenshot, to the app's owner only.
func (T *CustomApps) handleReportShot(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_, owner, err := T.openApp(r, user, r.PathValue("slug"))
	if err != nil {
		jsonapi.Fail(w, r, err)
		return
	}
	var shot []byte
	if user != owner || !T.reportStore(owner).Get(shotsTable, r.PathValue("id"), &shot) || len(shot) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(shot)
}

// forgetReports drops a deleted app's reports.
func (T *CustomApps) forgetReports(owner, slug string) {
	if T == nil || T.DB == nil {
		return
	}
	reportsMu.Lock()
	defer reportsMu.Unlock()
	for _, r := range T.loadReports(owner, slug) {
		T.reportStore(owner).Unset(shotsTable, r.ID)
	}
	T.reportStore(owner).Unset(reportsTable, slug)
}

// handleReportsPage is the app's Reports page, its owner's only.
func (T *CustomApps) handleReportsPage(w http.ResponseWriter, r *http.Request, spec AppSpec, owner bool) {
	if !owner {
		http.NotFound(w, r)
		return
	}
	base := T.WebPath() + "/" + spec.Slug
	ui.Page{
		Title:         spec.Name + " reports",
		ShowTitle:     true,
		BackURL:       base + "/",
		MaxWidth:      "900px",
		ExtraHeadHTML: reportsPageJS,
		Sections: []ui.Section{{
			Title: "Reported problems",
			Subtitle: "What people you shared this app with said went wrong, newest first. " +
				"Open one for what a check of the app as they see it found, then fix it with Builder.",
			Detail: "A person reports a problem from the app's header. When they send diagnostic information, the app is loaded again as them, " +
				"with a copy of their records so nothing of theirs changes, and an assistant reads what it showed: errors, failed requests, " +
				"the page's text and a screenshot. Each person may send three reports a day about this app; the last 50 are kept.",
			Body: ui.Table{
				Source:        base + "/_reports/list",
				RowKey:        "id",
				AutoRefreshMS: 3000,
				Columns: []ui.Col{
					{Field: "reporter", Flex: 0},
					{Field: "message", Flex: 3},
					{Field: "status", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "open", Label: "Open", Color: "warning"},
						{Value: "closed", Label: "Closed", Color: "mute"},
					}},
					{Field: "diag", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "running", Label: "Checking", Color: "info"},
						{Value: "done", Label: "Checked", Color: "success"},
						{Value: "failed", Label: "Not checked", Color: "danger"},
						{Value: "none", Label: "No diagnostics", Color: "mute"},
					}},
					{Field: "at", Format: "reltime", Mute: true, Line: 2},
					{Field: "diag_note", Mute: true, Line: 2},
				},
				RowActions: []ui.RowAction{
					ui.ExpandIf("Details", "id", "", ui.RecordView{Pairs: []ui.DisplayPair{
						{Label: "They said", Field: "message", Block: true},
						{Label: "Diagnosis", Field: "analysis", Block: true, HideEmpty: true},
						{Label: "Errors", Field: "page_errors", Mono: true, Block: true, HideEmpty: true},
						{Label: "Failed requests", Field: "failed_requests", Mono: true, Block: true, HideEmpty: true},
						{Label: "Their failed runs", Field: "run_failures", Mono: true, Block: true, HideEmpty: true},
						{Label: "What the page showed", Field: "shown", Block: true, HideEmpty: true},
						{Label: "Where they were", Field: "view", Mono: true, Block: true, HideEmpty: true},
					}}),
					{Type: "button", Label: "Screenshot", Method: "client", PostTo: "app_report_screenshot", OnlyIf: "has_shot"},
					{Type: "button", Label: "Fix with Builder", Method: "client", PostTo: "app_report_builder", OnlyIf: "has_builder"},
					{Type: "toggle", Field: "closed", Label: "Closed", PostTo: base + "/_reports/{id}/close"},
					{Type: "button", Label: "Delete", Method: "DELETE", PostTo: base + "/_reports/{id}", Confirm: "Delete this report?", Variant: "danger"},
				},
				EmptyText: "Nobody has reported a problem with this app.",
			},
		}},
	}.ServeHTTP(w, r)
}

// reportsPageJS opens a report's screenshot and its Builder fix: both are
// places to go that each row knows.
//
// Head HTML runs before the runtime at the end of the body, so both scripts
// register once the document is parsed (the runtime has run by then).
const reportsPageJS = `<script>(function(){
  function register(){
  if (!window.uiRegisterClientAction) return;
  window.uiRegisterClientAction('app_report_screenshot', function(ctx){
    var u = ctx && ctx.record && ctx.record.shot_url;
    if (u) window.open(u, '_blank', 'noopener');
  });
  window.uiRegisterClientAction('app_report_builder', function(ctx){
    var u = ctx && ctx.record && ctx.record.builder_url;
    if (u) location.href = u;
  });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', register); else register();
})();</script>`

// reportDialogJS is the "Report a problem" dialog: a message, the
// diagnostics box, and Send in the footer.
const reportDialogJS = `<script>(function(){
  function register(){
  if (!window.uiRegisterClientAction || !window.uiOpenModal) return;
  window.uiRegisterClientAction('` + reportNavAction + `', function(){
    var box = document.createElement('textarea');
    box.className = 'ui-input';
    box.rows = 5;
    box.style.width = '100%';
    box.style.boxSizing = 'border-box';
    box.setAttribute('aria-label', 'What went wrong');
    box.placeholder = 'What went wrong, and what you were doing when it happened';
    var tick = document.createElement('input');
    tick.type = 'checkbox';
    tick.checked = true;
    tick.style.marginTop = '3px';
    var label = document.createElement('label');
    label.style.cssText = 'display:flex;gap:8px;align-items:flex-start;margin-top:12px;cursor:pointer';
    var words = document.createElement('div');
    var head = document.createElement('div');
    head.textContent = 'Send diagnostic information';
    var help = document.createElement('div');
    help.className = 'ui-muted';
    help.style.fontSize = '0.9em';
    help.textContent = 'Loads the app again as you, without changing anything of yours, and sends the owner what it shows you, any errors, and an assistant\'s read of what went wrong.';
    words.appendChild(head);
    words.appendChild(help);
    label.appendChild(tick);
    label.appendChild(words);
    var err = document.createElement('div');
    err.style.cssText = 'color:var(--danger);margin-top:10px';
    err.setAttribute('role', 'alert');
    window.uiOpenModal({
      title: 'Report a problem',
      subtitle: 'Goes to the person who shared this app with you.',
      width: '480px',
      closeLabel: 'Cancel',
      mount: function(body, api){
        body.appendChild(box);
        body.appendChild(label);
        body.appendChild(err);
        // Cancel is the dialog's own Close; Send joins it in the footer as
        // the main button.
        var cancel = api.primaryButton;
        var btn = document.createElement('button');
        btn.className = 'ui-row-btn primary';
        btn.textContent = 'Send';
        api.addAction(btn);
        btn.addEventListener('click', function(){
          var msg = (box.value || '').trim();
          if (!msg) { err.textContent = 'Say what went wrong first.'; box.focus(); return; }
          err.textContent = '';
          btn.disabled = true;
          btn.textContent = 'Sending...';
          fetch('_report', {method: 'POST', credentials: 'same-origin',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({message: msg, diagnostics: !!tick.checked})})
            .then(function(r){
              if (!r.ok) return r.text().then(function(t){ throw new Error((t || '').trim() || ('HTTP ' + r.status)); });
              return r.json();
            })
            .then(function(d){
              body.textContent = '';
              var done = document.createElement('p');
              done.style.margin = '0';
              done.textContent = (d && d.said) || 'Sent to the app\'s owner.';
              body.appendChild(done);
              btn.remove();
              if (cancel) { cancel.textContent = 'Close'; cancel.className = 'ui-row-btn primary'; }
            })
            .catch(function(e){
              err.textContent = 'Not sent: ' + e.message;
              btn.disabled = false;
              btn.textContent = 'Send';
            });
        });
      },
    });
  });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', register); else register();
})();</script>`
