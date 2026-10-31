package admin

// System Hooks: the jobs gohort hands to tools on this machine, and how
// each is done here. A job (core/flows) is one thing gohort needs done, as a
// contract: reading an iPhone photo, the text of a PDF, writing a guide as
// Word. Each has a native flow, its ideal tool (or Go, or the app); where
// that tool is missing, or the admin wants another, they adapt the job with a
// flow of their own, saved as a connector of kind flows.ConnectorKind, so it
// is approved, exported and imported like every other connector. They can
// also add a format gohort does not support at all. The tools gohort needs
// that are not jobs yet (yt-dlp, ffmpeg, git, the sandbox) are listed too,
// with what to install.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterConnectorKind(flows.ConnectorKind, adaptedFlowHandler{}) }

// adaptedFlowHandler is the connector kind for an adapted flow: approving
// one makes it a way gohort does its job.
type adaptedFlowHandler struct{}

func (adaptedFlowHandler) adapted(c Connector) (flows.Adapted, error) {
	var a flows.Adapted
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &a); err != nil {
			return a, fmt.Errorf("bad flow spec: %w", err)
		}
	}
	return a, nil
}

func (h adaptedFlowHandler) Validate(c Connector) error {
	a, err := h.adapted(c)
	if err != nil {
		return err
	}
	return a.Validate()
}

func (h adaptedFlowHandler) Materialize(c Connector) error {
	a, err := h.adapted(c)
	if err != nil {
		return err
	}
	return flows.SetAdapted(c.Name, a)
}

func (h adaptedFlowHandler) Teardown(c Connector) error {
	flows.RemoveAdapted(c.Name)
	return nil
}

func (h adaptedFlowHandler) Summary(c Connector) string {
	a, err := h.adapted(c)
	if err != nil {
		return err.Error()
	}
	j, _ := a.Contract()
	return j.Title + ", with " + strings.Join(stepPrograms(a.Flow()), " then ")
}

func stepPrograms(steps []string) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = flows.Program(s)
	}
	return out
}

// depRow is one row of the table.
type depRow struct {
	ID       string `json:"id"`   // a job's id, an adapted flow's name, or tool:<program>
	Name     string `json:"name"` // the adapted flow's name
	Group    string `json:"group"`
	Enables  string `json:"enables"`
	By       string `json:"by"`
	State    string `json:"state"` // ready | installed | missing | stale | nosandbox | on | off
	Detail   string `json:"detail,omitempty"`
	Job      bool   `json:"job"`    // a job: it can be adapted
	Fills    bool   `json:"fills"`  // an adapted flow that fills a job
	Custom   bool   `json:"custom"` // an adapted flow that adds a format
	Adapted  bool   `json:"adapted"`
	CanOn    bool   `json:"can_on"`
	CanOff   bool   `json:"can_off"`
	Testable bool   `json:"testable"`
	Output   bool   `json:"output"`
	// Link rows take a link rather than a file: Test asks for one.
	Link bool `json:"link"`
	// A row that cannot run here, or whose last test failed, has a ✨ Fix in
	// place of its plain Adapt or Edit (markFix): the same form, with the
	// assist beside it.
	FixJob     bool `json:"fix_job"`
	FixFill    bool `json:"fix_fill"`
	FixCustom  bool `json:"fix_custom"`
	Adapt      bool `json:"adapt"`
	EditFill   bool `json:"edit_fill"`
	EditCustom bool `json:"edit_custom"`
}

var groupOrder = []string{flows.GroupReading, flows.GroupWriting, flows.GroupMedia, flows.GroupCode}

// toolGroups places the tools that are not jobs yet.
var toolGroups = map[string]string{
	"yt-dlp": flows.GroupMedia, "ffmpeg": flows.GroupMedia, "ffprobe": flows.GroupMedia,
}

// testParams are the values a test of job passes: each one's default, and
// the link the admin gave for one that takes a link.
func testParams(j flows.Job, link string) map[string]string {
	out := map[string]string{}
	for _, p := range j.Params {
		out[p.Name] = p.Default
	}
	if takesLink(j) {
		out["url"] = strings.TrimSpace(link)
	}
	return out
}

// takesLink reports whether a job is called with a link rather than a file.
func takesLink(j flows.Job) bool {
	if j.TakesFile() || j.IsExport() {
		return false
	}
	for _, p := range j.Params {
		if p.Name == "url" {
			return true
		}
	}
	return false
}

var versionRE = regexp.MustCompile(`\d+(\.\d+)+`)

// shortVersion is a tool's version as the page shows it: "22.02.0" from
// "pdftotext version 22.02.0".
func shortVersion(v string) string {
	if m := versionRE.FindString(v); m != "" {
		return m
	}
	return ""
}

// depRows are the table, by group: each job, the adapted flows that fill
// it, then the tools that are not jobs. With custom, they are the Custom
// area's instead: the flows that add a format gohort does not support.
func depRows(custom bool) []depRow {
	// Once: it runs each tool's version command.
	checked := CheckDependencies()
	tools := map[string]DependencyStatus{}
	for _, d := range checked {
		tools[d.Name] = d
	}
	refusing := sandbox.GetSandboxStatus().Refusing
	var adapted []struct {
		c Connector
		a flows.Adapted
	}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == flows.ConnectorKind {
			a, _ := adaptedFlowHandler{}.adapted(c)
			adapted = append(adapted, struct {
				c Connector
				a flows.Adapted
			}{c, a})
		}
	}
	covered := map[string]bool{}
	byGroup := map[string][]depRow{}
	for _, j := range flows.Jobs() {
		if strings.HasPrefix(j.ID, "test.") {
			continue
		}
		f, ready := j.Ready()
		row := depRow{ID: j.ID, Group: j.Group, Enables: j.Title, Detail: "Used for " + j.UsedFor + ".", Job: true,
			Testable: !j.AppRendered(), Output: j.IsExport(), Link: takesLink(j)}
		for _, p := range j.Programs() {
			covered[p] = true
		}
		progs := f.Programs()
		if j.Builtin {
			progs = j.Programs()
		}
		switch {
		case !ready:
			row.By, row.State = j.By(), "missing"
			if hint := j.InstallHint(); strings.HasPrefix(hint, "set up") {
				row.Detail = "To use it, " + hint + "."
			} else {
				row.Detail = "Install " + hint + ", or adapt it to a tool you have."
			}
		case len(progs) == 0:
			row.By, row.State = j.By(), "ready"
		default:
			var by []string
			for _, p := range progs {
				v := shortVersion(tools[p].Version)
				by = append(by, strings.TrimSpace(p+" "+v))
				if tools[p].Stale {
					row.State, row.Detail = "stale", tools[p].Note
				}
			}
			sep := " then "
			if j.Builtin {
				sep = " and "
			}
			row.By = strings.Join(by, sep)
			if row.State == "" {
				row.State = "ready"
			}
		}
		if refusing && !j.Builtin {
			row.State = "nosandbox"
		}
		row.markFix()
		byGroup[j.Group] = append(byGroup[j.Group], row)
		for _, x := range adapted {
			if x.a.Job == j.ID {
				byGroup[j.Group] = append(byGroup[j.Group], adaptedRow(x.c, x.a, refusing))
			}
		}
	}
	if custom {
		rows := []depRow{}
		for _, x := range adapted {
			if x.a.Job == "" {
				rows = append(rows, adaptedRow(x.c, x.a, refusing))
			}
		}
		return rows
	}
	for _, d := range checked {
		if covered[d.Name] {
			continue
		}
		g := toolGroups[d.Name]
		if g == "" {
			g = flows.GroupCode
		}
		row := depRow{ID: "tool:" + d.Name, Group: g, Enables: d.Enables, By: strings.TrimSpace(d.Name + " " + shortVersion(d.Version))}
		switch {
		case !d.Present:
			row.State, row.Detail = "missing", "Install: "+d.Install
		case d.Stale:
			row.State, row.Detail = "stale", d.Note
		default:
			row.State = "installed"
		}
		byGroup[g] = append(byGroup[g], row)
	}
	var rows []depRow
	for _, g := range groupOrder {
		rows = append(rows, byGroup[g]...)
	}
	return rows
}

// adaptedRow is an adapted flow's row: under the job it fills, or in its
// group as a format of its own.
func adaptedRow(c Connector, a flows.Adapted, refusing bool) depRow {
	j, _ := a.Contract()
	row := depRow{ID: c.Name, Name: c.Name, Group: j.Group, Enables: j.Title, By: strings.Join(a.Flow(), "  then  "),
		Adapted: true, Fills: a.Job != "", Custom: a.Job == "", CanOn: !c.Approved, CanOff: c.Approved,
		Testable: true, Output: j.IsExport(), Link: takesLink(j)}
	if row.Fills {
		row.Enables = "↳ " + c.Name
		row.Detail = "Adapted, for " + j.Title + "."
	} else if j.IsExport() {
		row.Enables, row.Detail = "Writes "+j.Title, "Exports documents as "+j.Title+"."
	} else if j.HandsOff() {
		row.Enables, row.Detail = "Reads "+j.Title, "Hands it on as "+flows.FormatLabel(j.Then)+", "+readerOf(j.Then)+"."
	} else {
		row.Enables, row.Detail = "Reads "+j.Title, "Becomes "+j.Becomes.Label()+"."
	}
	if a.Prefer {
		row.Detail += " Used before the native flow."
	}
	if c.LastError != "" {
		row.Detail += " Last error: " + c.LastError
	}
	installed := true
	for _, p := range stepPrograms(a.Flow()) {
		installed = installed && flows.Installed(p)
	}
	switch {
	case refusing:
		row.State = "nosandbox"
	case !installed:
		row.State = "missing"
	case c.Approved:
		row.State = "on"
	default:
		row.State = "off"
	}
	row.markFix()
	return row
}

// flowForm is an adapted flow as the forms carry it. Steps are its
// commands as a list of rows (the form's "+ Add step"), Reads folders
// separated by commas or lines. A flow for one of
// gohort's jobs takes its contract from the job (the save address names
// it, ?job=); one that adds a format says its own: an input that Takes
// formats and Becomes a kind, or an output that Gives a format From the
// document's Markdown or HTML.
type flowForm struct {
	Name        string   `json:"name"`
	Direction   string   `json:"direction,omitempty"`
	Takes       string   `json:"takes,omitempty"`
	Becomes     string   `json:"becomes,omitempty"`
	Gives       string   `json:"gives,omitempty"`
	From        string   `json:"from,omitempty"`
	Steps       stepList `json:"steps"`
	Reads       string   `json:"reads,omitempty"`
	TimeoutSecs int      `json:"timeout_secs"`
	Prefer      bool     `json:"prefer"`
}

// step is one row of the Steps list.
type step struct {
	Command string `json:"command"`
}

// stepList is a flow's steps as the form edits them: rows, each a command.
// It also reads a string of one command per line (anything posted before
// the list was), so either shape saves.
type stepList []step

func (sl *stepList) UnmarshalJSON(b []byte) error {
	var text string
	if json.Unmarshal(b, &text) == nil {
		*sl = stepsOf(splitLines(text, false))
		return nil
	}
	var rows []step
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	*sl = rows
	return nil
}

// commands are the non-empty commands, in order.
func (sl stepList) commands() []string {
	var out []string
	for _, st := range sl {
		if c := strings.TrimSpace(st.Command); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func stepsOf(commands []string) stepList {
	out := make(stepList, len(commands))
	for i, c := range commands {
		out[i] = step{Command: c}
	}
	return out
}

func splitLines(s string, commas bool) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || (commas && r == ',') }) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// adapted reads the form, for job when it fills one.
func (ff flowForm) adapted(job string) flows.Adapted {
	a := flows.Adapted{Job: job, Steps: ff.Steps.commands(), Reads: splitLines(ff.Reads, true),
		TimeoutSecs: ff.TimeoutSecs, Prefer: ff.Prefer}
	if job != "" {
		return a
	}
	if strings.TrimSpace(ff.Direction) == "output" {
		a.Gives = flows.ParseOutFormat(ff.Gives)
		if a.Gives == "" {
			a.Gives = strings.TrimSpace(ff.Gives) // refused by Validate, with what was written
		}
		a.Takes = []string{"text/markdown"}
		if strings.TrimSpace(ff.From) == "html" {
			a.Takes = []string{"text/html"}
		}
		return a
	}
	if then, ok := strings.CutPrefix(strings.TrimSpace(ff.Becomes), handOffPrefix); ok {
		a.Then = flows.ParseFormat(then)
	} else {
		a.Becomes = flows.Kind(strings.TrimSpace(ff.Becomes))
	}
	for _, tok := range strings.FieldsFunc(ff.Takes, func(r rune) bool { return r == ',' || r == ' ' }) {
		a.Takes = append(a.Takes, flows.ParseFormat(tok))
	}
	return a
}

// formOf is a for its edit form.
func formOf(name string, a flows.Adapted) flowForm {
	ff := flowForm{Name: name, Steps: stepsOf(a.Flow()), Reads: strings.Join(a.Reads, ", "), TimeoutSecs: a.TimeoutSecs, Prefer: a.Prefer}
	switch {
	case a.Job != "":
	case a.Gives != "":
		ff.Direction, ff.Gives, ff.From = "output", a.Gives, "markdown"
		if len(a.Takes) == 1 && a.Takes[0] == "text/html" {
			ff.From = "html"
		}
	default:
		short := make([]string, len(a.Takes))
		for i, t := range a.Takes {
			short[i] = flows.ShortName(t)
		}
		ff.Direction, ff.Takes, ff.Becomes = "input", strings.Join(short, ", "), string(a.Becomes)
		if a.Then != "" {
			ff.Becomes = handOffPrefix + a.Then
		}
	}
	return ff
}

// saveAdapted saves an adapted flow called name, off: a new one, or (edit) a
// change to one, which turns it off first if it was on, so a change never
// runs unapproved. Returns whether it turned one off.
func saveAdapted(owner, name string, a flows.Adapted, edit bool, desc string) (turnedOff bool, err error) {
	if err := a.Validate(); err != nil {
		return false, err
	}
	prev, existed := GetConnector(RootDB, name)
	switch {
	case existed && !edit:
		return false, fmt.Errorf("there is already a connector called %s: pick another name", name)
	case existed && prev.Kind != flows.ConnectorKind:
		return false, fmt.Errorf("%s is a connector of another kind: pick another name", name)
	case existed && prev.Approved:
		if err := UnapproveConnector(RootDB, name); err != nil {
			return false, err
		}
		turnedOff = true
	}
	spec, _ := json.Marshal(a)
	// A changed flow is another flow: its last test said nothing of this one.
	forgetTest(name)
	c := Connector{Name: name, Kind: flows.ConnectorKind, Owner: owner, Spec: spec, Desc: desc}
	if existed {
		c.Owner, c.Desc = prev.Owner, prev.Desc
	}
	return turnedOff, SaveConnector(RootDB, c)
}

// runRow runs the row id names (a job, or an adapted flow, on or off) on in:
// a file for a job that reads one, gohort's sample document for an export.
func runRow(r *http.Request, id string, in []byte) (flows.Result, string, bool, error) {
	link := r.FormValue("url")
	if j, ok := flows.JobByID(id); ok {
		if j.IsExport() {
			doc := flows.SampleDocument()
			if j.Builtin {
				return flows.Result{}, "", true, fmt.Errorf("%s is written by the app itself: export a guide from Scribe to see it", j.Title)
			}
			res, err := flows.RunNative(r.Context(), j, flows.Input{Doc: &doc})
			return res, "", true, err
		}
		from := flows.Sniff(in)
		res, err := flows.RunNative(r.Context(), j, flows.Input{Data: in, From: from, Params: testParams(j, link)})
		return res, from, false, err
	}
	c, ok := GetConnector(RootDB, id)
	if !ok || c.Kind != flows.ConnectorKind {
		return flows.Result{}, "", false, fmt.Errorf("there is nothing called %q", id)
	}
	a, err := adaptedFlowHandler{}.adapted(c)
	if err != nil {
		return flows.Result{}, "", false, err
	}
	return runFlowOn(r.Context(), c.Name, a, in, link)
}

// runFlowOn runs an adapted flow, saved or not, as gohort would: an output
// on gohort's sample document, anything else on in (or the link).
func runFlowOn(ctx context.Context, name string, a flows.Adapted, in []byte, link string) (flows.Result, string, bool, error) {
	if a.IsOutput() {
		doc := flows.SampleDocument()
		res, err := flows.RunAdapted(ctx, name, a, flows.Input{Doc: &doc})
		return res, "", true, err
	}
	from := flows.Sniff(in)
	j, _ := a.Contract()
	res, err := flows.RunAdapted(ctx, name, a, flows.Input{Data: in, From: from, Params: testParams(j, link)})
	return res, from, false, err
}

// isLinkRow reports whether a row takes a link (tests on one the admin
// gives, no file needed).
func isLinkRow(id string) bool {
	if j, ok := flows.JobByID(id); ok {
		return takesLink(j)
	}
	if c, ok := GetConnector(RootDB, id); ok && c.Kind == flows.ConnectorKind {
		a, _ := adaptedFlowHandler{}.adapted(c)
		j, _ := a.Contract()
		return takesLink(j)
	}
	return false
}

// isOutputRow reports whether a row writes a document (tests on the sample,
// no file needed).
func isOutputRow(id string) bool {
	if j, ok := flows.JobByID(id); ok {
		return j.IsExport()
	}
	if c, ok := GetConnector(RootDB, id); ok && c.Kind == flows.ConnectorKind {
		a, _ := adaptedFlowHandler{}.adapted(c)
		return a.IsOutput()
	}
	return false
}

// handOffPrefix marks a Becomes that hands the result on as a format.
const handOffPrefix = "then:"

// readerOf says what reads format here: "which gohort reads (built in)",
// or the converter that does.
func readerOf(format string) string {
	for _, r := range flows.Readers() {
		if r.Format == format {
			if r.Builtin {
				return "which gohort reads (built in: " + r.By + ")"
			}
			return "which your " + r.By + " reads"
		}
	}
	return "which nothing here reads now"
}

// checkCustomInput refuses a custom input for a format gohort already reads
// (that is its row's Adapt), and a hand-off to a format nothing here reads.
func checkCustomInput(a flows.Adapted) error {
	for _, t := range a.Takes {
		if by := flows.BuiltinReader(t); by != "" {
			return fmt.Errorf("gohort already reads %s (%s, under Reading): to use another tool for it, adapt that row instead", flows.FormatLabel(t), by)
		}
	}
	if a.Then != "" && readerOf(a.Then) == "which nothing here reads now" {
		return fmt.Errorf("nothing here reads %s, so there is no one to hand it on to: pick a format from the list", flows.FormatLabel(a.Then))
	}
	return nil
}

// becomesOptions are Becomes's choices: the kinds a file becomes, then the
// formats it can be handed on as, each with what reads it.
func becomesOptions() []map[string]string {
	var out []map[string]string
	for _, k := range flows.Kinds {
		out = append(out, map[string]string{"value": string(k), "label": k.Label(), "group": "It becomes"})
	}
	for _, r := range flows.Readers() {
		label := flows.FormatLabel(r.Format) + " (built in)"
		if !r.Builtin {
			label = flows.FormatLabel(r.Format) + " (your " + r.By + ")"
		}
		out = append(out, map[string]string{"value": handOffPrefix + r.Format, "label": label, "group": "Or hand it on as"})
	}
	return out
}

// flowName is a name for a new flow adapting job id: read.heic -> read-heic-adapted.
func flowName(id string) string { return strings.ReplaceAll(id, ".", "-") + "-adapted" }

func (a *AdminApp) registerSystemDepsRoutes(sub *http.ServeMux) {
	// The table: GET lists it. POST saves an adapted flow, off: one that
	// fills a job (?job=<id>), one that adds a format, or with ?edit=<name>
	// a change to that one.
	sub.HandleFunc("/api/system-deps", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleSystemDeps, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleSystemDepsSave, a.gate(), jsonapi.MaxBody(1<<16)),
	}.Serve)

	// Becomes's choices for a custom input, as the form renders.
	sub.HandleFunc("/api/system-deps/becomes", jsonapi.Get(a.db, func(c *jsonapi.Call) (any, error) {
		return becomesOptions(), nil
	}, a.gate()))

	// One adapted flow as its edit form fills from: GET ?name=.
	sub.HandleFunc("/api/system-deps/one", jsonapi.Get(a.db, a.handleSystemDepsOne, a.gate()))

	// The Adapt form of a job's row: GET ?id=<job>. An export the app
	// renders itself is adapted to be used first.
	sub.HandleFunc("/api/system-deps/adapt", jsonapi.Get(a.db, a.handleSystemDepsAdapt, a.gate()))

	// Test a row: POST ?id=, multipart "file" for one that reads a file; an
	// export writes gohort's sample document. Runs exactly as gohort would,
	// under the request, so closing the dialog (or Stop) ends it.
	sub.HandleFunc("/api/system-deps/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		var in []byte
		if !isOutputRow(id) && !isLinkRow(id) {
			r.Body = http.MaxBytesReader(w, r.Body, 257<<20)
			file, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "Pick a sample file to test it on.", http.StatusBadRequest)
				return
			}
			defer file.Close()
			if in, err = io.ReadAll(file); err != nil {
				http.Error(w, "The file could not be read.", http.StatusBadRequest)
				return
			}
		}
		start := time.Now()
		res, from, output, err := runRow(r, id, in)
		out := map[string]any{"took_ms": time.Since(start).Milliseconds(), "log": res.Log}
		if err != nil {
			// A failure marks the row for fixing, with why; Stop is not one.
			if r.Context().Err() == nil {
				recordTest(id, testOutcome{Message: err.Error(), Log: res.Log, At: time.Now()})
			}
			out["ok"], out["message"] = false, err.Error()
			jsonapi.Write(w, out)
			return
		}
		recordTest(id, testOutcome{OK: true, At: time.Now()})
		out["ok"] = true
		if output {
			ext := outputExt(id)
			out["message"] = fmt.Sprintf("It wrote the sample document as %s (%s), by %s.", flows.OutLabel(ext), HumanSize(int64(len(res.Out))), res.By)
			if len(res.Out) <= 4<<20 {
				out["download"] = "data:" + flows.OutMIME(ext) + ";base64," + base64.StdEncoding.EncodeToString(res.Out)
				out["filename"] = "sample." + ext
			}
			jsonapi.Write(w, out)
			return
		}
		if len(res.Files) > 0 {
			out["message"] = fmt.Sprintf("It gave %d results from %s (%s), by %s.", len(res.Files), flows.FormatLabel(from), HumanSize(int64(len(in))), res.By)
			res.Out = res.Files[0] // the first one shown
		} else if isLinkRow(id) {
			out["message"] = fmt.Sprintf("It fetched %s (%s) from the link, by %s.", flows.FormatLabel(flows.Sniff(res.Out)), HumanSize(int64(len(res.Out))), res.By)
		} else {
			out["message"] = fmt.Sprintf("It turned %s (%s) into %s (%s), by %s.", flows.FormatLabel(from), HumanSize(int64(len(in))), describeResult(res.Out), HumanSize(int64(len(res.Out))), res.By)
		}
		if ct := http.DetectContentType(res.Out); strings.HasPrefix(ct, "image/") && len(res.Out) <= 4<<20 {
			out["preview"] = "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(res.Out)
		} else if strings.HasPrefix(ct, "text/") {
			text := string(res.Out)
			text = textutil.Clip(text, 2000)
			out["text"] = text
		}
		jsonapi.Write(w, out)
	})
}

// handleSystemDeps is the table, or with ?fix= the jobs that cannot run here
// (for the walk), or with ?custom= the formats the deployment added.
func (a *AdminApp) handleSystemDeps(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	if q.Get("fix") != "" {
		fix := []depRow{}
		for _, row := range depRows(false) {
			if row.FixJob {
				fix = append(fix, row)
			}
		}
		return fix, nil
	}
	return depRows(q.Get("custom") != ""), nil
}

// handleSystemDepsSave saves an adapted flow, off.
func (a *AdminApp) handleSystemDepsSave(c *jsonapi.Call, ff flowForm) (any, error) {
	q := c.R.URL.Query()
	name, job := strings.TrimSpace(ff.Name), strings.TrimSpace(q.Get("job"))
	edit := strings.TrimSpace(q.Get("edit"))
	if edit != "" {
		name = edit
		if conn, ok := GetConnector(RootDB, edit); ok && conn.Kind == flows.ConnectorKind {
			prev, _ := adaptedFlowHandler{}.adapted(conn)
			job = prev.Job
		}
	}
	if job != "" {
		if _, ok := flows.JobByID(job); !ok {
			return nil, jsonapi.BadRequest("there is no job " + job)
		}
	}
	ad := ff.adapted(job)
	if job == "" && !ad.IsOutput() {
		if err := checkCustomInput(ad); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	if _, err := saveAdapted(c.User, name, ad, edit != "", "Adapted flow"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true, "message": "Saved, off. Test it, then turn it on."}, nil
}

// handleSystemDepsOne is the ?name= adapted flow as its edit form fills from.
func (a *AdminApp) handleSystemDepsOne(c *jsonapi.Call) (any, error) {
	conn, ok := GetConnector(RootDB, strings.TrimSpace(c.R.URL.Query().Get("name")))
	if !ok || conn.Kind != flows.ConnectorKind {
		return nil, jsonapi.NotFound("no such flow")
	}
	ad, _ := adaptedFlowHandler{}.adapted(conn)
	return formOf(conn.Name, ad), nil
}

// handleSystemDepsAdapt is the ?id= job's Adapt form, under a name not taken.
func (a *AdminApp) handleSystemDepsAdapt(c *jsonapi.Call) (any, error) {
	j, ok := flows.JobByID(c.R.URL.Query().Get("id"))
	if !ok {
		return nil, jsonapi.NotFound("no such job")
	}
	name := flowName(j.ID)
	for n := 2; ; n++ {
		if _, taken := GetConnector(RootDB, name); !taken {
			break
		}
		name = fmt.Sprintf("%s-%d", flowName(j.ID), n)
	}
	return flowForm{Name: name, Prefer: j.Builtin}, nil
}

// outputExt is the format an export row writes.
func outputExt(id string) string {
	if j, ok := flows.JobByID(id); ok {
		return j.Gives
	}
	if c, ok := GetConnector(RootDB, id); ok {
		a, _ := adaptedFlowHandler{}.adapted(c)
		j, _ := a.Contract()
		return j.Gives
	}
	return ""
}

// describeResult names a result: "an image the model can read", audio, or
// "text".
func describeResult(out []byte) string {
	for _, k := range []flows.Kind{flows.KindImage, flows.KindAudio} {
		if flows.Readable(out, k) {
			return k.Label()
		}
	}
	return flows.KindText.Label()
}

var depsSources = []string{"api/system-deps", "api/system-deps?custom=1"}

// flowFormWidth is the dialog a flow's form opens in: wide enough that a
// step's command line reads on one line, a program and its arguments.
const flowFormWidth = "min(760px, 96vw)"

// flowFields are the forms' fields. withName adds a name; custom adds the
// contract of a format gohort does not support (direction, what it takes
// and becomes, or the format it gives).
func flowFields(withName, custom bool) []ui.FormField {
	var out []ui.FormField
	if withName {
		out = append(out, ui.FormField{Field: "name", Label: "Name", Type: "text", Required: true,
			Help: "Lowercase letters, numbers, - and _."})
	}
	if custom {
		becomes := []ui.SelectOption{}
		for _, k := range flows.Kinds {
			becomes = append(becomes, ui.SelectOption{Value: string(k), Label: k.Label()})
		}
		out = append(out,
			ui.FormField{Field: "direction", Label: "It", Type: "select", Default: "input", Options: []ui.SelectOption{
				{Value: "input", Label: "reads a format gohort does not"},
				{Value: "output", Label: "writes documents as a format gohort does not"},
			}},
			ui.FormField{Field: "takes", Label: "Takes", Type: "text", ShowWhen: "direction:input",
				Help:   "The formats it reads, separated by commas: pages, or a MIME type. Not one gohort already reads (PDF, Word, HEIC and the rest under Reading): adapt that one's row instead.",
				Detail: "Use * for any format nothing else reads, for a converter that reads everything."},
			ui.FormField{Field: "becomes", Label: "Becomes", Type: "select", Options: becomes, OptionsSource: "api/system-deps/becomes", ShowWhen: "direction:input",
				Help:   "What it turns the file into: an image or text the model reads, or a format gohort already reads, handed on to that reader.",
				Detail: "Handed on, its result has to be that format, and the reader listed with it finishes the job: a converter from Pages to PDF, say, then gohort's own PDF reader. Built in means gohort reads it; your converter means one added here."},
			ui.FormField{Field: "gives", Label: "Format", Type: "text", ShowWhen: "direction:output",
				Help: "The format it writes, by its extension: epub, rtf, or any other."},
			ui.FormField{Field: "from", Label: "Reads the document as", Type: "select", Default: "markdown", ShowWhen: "direction:output", Options: []ui.SelectOption{
				{Value: "markdown", Label: "Markdown"},
				{Value: "html", Label: "HTML"},
			}})
	}
	return append(out, []ui.FormField{
		{Field: "steps", Label: "Steps", Type: "rows", AddLabel: "+ Add step",
			// A two-line box that grows with it: a command line with a few
			// arguments does not fit a one-line input.
			Columns: []ui.FormField{{Field: "command", Label: "Command", Type: "textarea", Rows: 2,
				Placeholder: "heif-convert {in} {out}"}},
			Help:   "The commands, run in order: add a step for each. {in} is the file it reads (in a later step, the step before's result) and {out} the file it writes.",
			Detail: "For example: heif-convert {in} {out}. Leave {out} out and what a step prints is its result, as with antiword -w 0 {in}. Write {out.wav} to name a step's result for the next program. A job that gives several files writes them into {outdir}, and one called with values (a count, a link) has them as {count} or {url}. Each line runs as written, without a shell, so ; | and $( ) are just characters."},
		{Field: "reads", Label: "Can also read", Type: "text",
			Help: "Folders it needs to read, read-only, separated by commas: a model file a transcriber uses, say. Nothing else on this machine is visible to it."},
		{Field: "timeout_secs", Label: "Time limit (seconds)", Type: "number",
			Help: "Stopped after this long, all steps together. 60 when left empty, 1800 at most."},
		{Field: "prefer", Label: "Use before the native flow", Type: "toggle",
			Help: "Off, it is used only where the native flow cannot do it."},
	}...)
}

func depsRowActions() []ui.RowAction {
	// A row that cannot run, or failed its test, has ✨ Fix: the same form
	// as its Adapt or Edit, with the assist beside it.
	adapt := ui.ModalAction("Adapt…", adaptForm())
	adapt.OnlyIf, adapt.Width = "adapt", flowFormWidth
	fixJob := ui.AssistAction("Fix", fixPanel(adaptForm()))
	fixJob.OnlyIf, fixJob.Variant = "fix_job", "primary"
	editFill := ui.ModalAction("Edit", editForm(false))
	editFill.OnlyIf, editFill.Width = "edit_fill", flowFormWidth
	fixFill := ui.AssistAction("Fix", fixPanel(editForm(false)))
	fixFill.OnlyIf, fixFill.Variant = "fix_fill", "primary"
	editCustom := ui.ModalAction("Edit", editForm(true))
	editCustom.OnlyIf, editCustom.Width = "edit_custom", flowFormWidth
	fixCustom := ui.AssistAction("Fix", fixPanel(editForm(true)))
	fixCustom.OnlyIf, fixCustom.Variant = "fix_custom", "primary"
	return []ui.RowAction{
		{Type: "button", Label: "Test", Method: "client", PostTo: "system_deps_test", OnlyIf: "testable"},
		fixJob, fixFill, fixCustom,
		adapt,
		{Type: "button", Label: "Turn on", PostTo: "api/connectors?action=approve&name={name}",
			Method: "POST", OnlyIf: "can_on", Variant: "primary"},
		{Type: "button", Label: "Turn off", PostTo: "api/connectors?action=unapprove&name={name}",
			Method: "POST", OnlyIf: "can_off"},
		editFill,
		editCustom,
		{Type: "button", Label: "Delete", PostTo: "api/connectors?name={name}", Method: "DELETE",
			OnlyIf: "adapted", Confirm: "Delete this flow? gohort stops using it.", Variant: "danger"},
	}
}

// depsColumns are both tables' columns.
func depsColumns() []ui.Col {
	return []ui.Col{
		{Field: "enables", Label: "Enables", Flex: 3},
		{Field: "by", Label: "Done by", Flex: 3},
		{Field: "state", Type: "badge", Badges: []ui.BadgeMapping{
			{Value: "ready", Label: "Ready", Color: "success"},
			{Value: "installed", Label: "Installed", Color: "success"},
			{Value: "on", Label: "On", Color: "success"},
			{Value: "off", Label: "Off", Color: "warning"},
			{Value: "stale", Label: "Stale, update", Color: "warning"},
			{Value: "missing", Label: "Not installed", Color: "danger"},
			{Value: "nosandbox", Label: "No sandbox here", Color: "danger"},
		}},
		{Field: "detail", Label: "", Mute: true, Flex: 6, Line: 2},
	}
}

// systemDepsSection is the System Hooks section on the Capabilities
// tab: Fix what cannot run here, the table of jobs and tools, then the
// Custom area, where Add sets up a format gohort does not support.
func systemDepsSection() ui.Section {
	return ui.Section{
		Title:    "System Hooks",
		Subtitle: "The jobs gohort hands to tools on this machine (reading what arrives, writing documents, media), how each is done here, and where to plug in another tool when one is missing.",
		Detail: "Each job gohort needs done, reading what arrives or writing documents, has a native flow: Go itself, the app, or the best tool for it when installed. Where that tool is missing, or you want another, adapt the job: give a flow of commands you have, test it, and turn it on. A row that cannot run here, or whose test failed, has ✨ Fix: its form, with an assistant beside it that knows the row, reads a program's help, tries steps on a file you attach, and fills the form for you to save. Fix what cannot run here goes through every such job in turn. Under Custom, add a format gohort does not support at all. Agents already reach every job here through gohort's own tools (video, export, attachments), and every custom format that is on, with the convert_file tool, on files in their workspace. The tools listed without Adapt are not jobs yet: install them.\n\n" +
			"A flow's commands run as written, with no shell reading them. They run in the sandbox, which sees the system folders read-only and nothing else on this machine except the folders a flow says it reads, with no network unless the job needs it, and they are stopped at the time limit. What a flow gives is checked before gohort uses it: an image has to be one the model can read, text has to be text, a Word file has to be a Word file, or it is thrown away. On a machine whose sandbox cannot confine commands, none of them run.",
		Body: ui.Stack{Children: []ui.Component{
			fixWalkButton(),
			ui.Table{
				Source:     depsSources[0],
				RowKey:     "id",
				GroupBy:    "group",
				Columns:    depsColumns(),
				RowActions: depsRowActions(),
				EmptyText:  "No dependency information.",
			},
			ui.Subsection{
				Title:    "Custom",
				Subtitle: "Formats gohort does not support itself, read or written by a tool you have.",
				Body: ui.Stack{Children: []ui.Component{
					ui.Table{
						Source:     depsSources[1],
						RowKey:     "id",
						Columns:    depsColumns(),
						RowActions: depsRowActions(),
						EmptyText:  "Nothing custom yet. Add one to read a format gohort does not, or to write documents as one.",
					},
					ui.ModalButton{
						Label: "Add…", Variant: "primary", Align: "left", Width: flowFormWidth,
						Title:    "Add a custom input or output",
						Subtitle: "Saved off: test it from its row, then turn it on.",
						Body:     addForm(),
					},
				}},
			},
		}},
	}
}

// systemDepsTestAction (a row's Test) picks a sample file and runs the row
// on it (an export runs on gohort's sample document instead), showing that
// it is alive (a spinner and the seconds) with a Stop, then what came of
// it: the image, the start of the text or the file to download, or the
// error and the end of what the commands printed.
const systemDepsTestAction = `function(ctx){
  var r = (ctx && ctx.record) || {};
  if(!r.id || !window.uiOpenModal){ return; }
  var el = window.uiEl;
  // An export is tested on gohort's sample document: nothing to pick.
  if (r.output) { run(null); return; }
  // A job that takes a link asks for one.
  if (r.link) {
    window.uiPrompt('A link to test it on (a public http or https address):', '', {placeholder: 'https://'}).then(function(v){
      v = (v || '').trim();
      if (v) run(null, v);
    });
    return;
  }
  var input = document.createElement('input');
  input.type = 'file'; input.style.display = 'none';
  document.body.appendChild(input);
  input.addEventListener('change', function(){
    var f = input.files && input.files[0];
    input.remove();
    if(f){ run(f); }
  });
  input.click();
  function run(f, link){
    var what = f ? f.name : (link || 'the sample document');
    var ctl = new AbortController(), timer = null, done = false, stop = null;
    // One footer button: Stop while it runs, Close once it has finished.
    // Closing the dialog any other way (Escape) stops it too.
    window.uiOpenModal({title: 'Test: ' + (r.enables || r.id), width: '600px',
      actions: [{label: 'Stop', onClick: function(api){ if (done) { api.close(); } else { ctl.abort(); } }}],
      onClose: function(){ ctl.abort(); if (timer) clearInterval(timer); },
      mount: function(body, api){
      var frames = ['⠋','⠙','⠹','⠸','⠼','⠴','⠦','⠧','⠇','⠏'], i = 0, t0 = Date.now();
      var status = el('div', {style: 'margin:0 0 0.6rem;line-height:1.45'});
      var out = el('div', {});
      stop = api.dialog.querySelector('.ui-modal-footer button');
      body.appendChild(status); body.appendChild(out);
      function tick(){
        var s = Math.round((Date.now() - t0) / 1000);
        status.textContent = frames[i++ % frames.length] + ' Running it on ' + what + (s >= 3 ? ' - ' + s + 's' : '');
      }
      tick(); timer = setInterval(tick, 250);
      function finish(text, ok){
        done = true; clearInterval(timer); timer = null;
        // A failed test marks the row for fixing, a passing one clears it.
        if (window.uiInvalidate) window.uiInvalidate(['api/system-deps', 'api/system-deps?custom=1']);
        status.textContent = text;
        status.style.color = ok ? '' : 'var(--danger, #e5484d)';
        if (stop) stop.textContent = 'Close';
      }
      var fd = new FormData(); if (f) fd.append('file', f); if (link) fd.append('url', link);
      fetch('api/system-deps/test?id=' + encodeURIComponent(r.id), {method: 'POST', credentials: 'same-origin', body: fd, signal: ctl.signal})
        .then(function(res){ return res.text().then(function(t){ if(!res.ok) throw new Error(t || ('HTTP ' + res.status)); return JSON.parse(t); }); })
        .then(function(d){
          var secs = ((d.took_ms || 0) / 1000).toFixed(1) + 's';
          finish((d.ok ? 'Worked in ' : 'Failed after ') + secs + ': ' + (d.message || ''), d.ok);
          if (d.preview) out.appendChild(el('img', {src: d.preview, style: 'max-width:100%;max-height:320px;display:block;margin:0 0 0.6rem;border-radius:6px'}));
          if (d.text) out.appendChild(el('pre', {style: 'white-space:pre-wrap;font-size:var(--fs-sm, 0.8rem);max-height:220px;overflow:auto;margin:0 0 0.6rem', text: d.text}));
          if (d.download) out.appendChild(el('p', {style: 'margin:0 0 0.6rem'}, [el('a', {href: d.download, download: d.filename || 'sample', text: 'Download ' + (d.filename || 'the result')})]));
          if (d.log) out.appendChild(el('pre', {style: 'white-space:pre-wrap;font-size:var(--fs-sm, 0.8rem);color:var(--text-mute);max-height:160px;overflow:auto;margin:0', text: d.log}));
        })
        .catch(function(e){
          if (e && e.name === 'AbortError') { finish('Stopped.', false); return; }
          finish('Failed: ' + ((e && e.message) || e), false);
        });
    }});
  }
}`
