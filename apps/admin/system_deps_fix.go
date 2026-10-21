package admin

// Fixing a hook: a row of System Hooks that cannot run here (its tool is not
// installed, out of date, the sandbox is missing) or whose last test failed
// has a ✨ Fix, and "Fix what cannot run here" walks every such job in turn.
// Each opens the assist (core/assist, ui.AssistPanel): the row's own form
// on the left, the Adapt form for a job or the flow's form for an adapted
// one, and on the right the worker, already told which row it is, what the
// job has to do and why it fails.
//
// WHY IT NEEDS NO AUTHORITY. It proposes; the admin saves. What it suggests
// fills the form, and the form's Save saves the flow off, as an admin's own
// Adapt does; only Turn on, on the row, makes gohort use it. Its tools read
// a program's help and try steps, in the same sandbox a flow runs in, on
// files the admin attached in the window, never on anything of this
// machine's. A try reads no folder the admin has not saved into the flow
// (Can also read), so a page it read on the web cannot steer it into this
// machine's files. It installs nothing: it says what to install.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/ui"
)

// hooksScope is what the assistant is told System Hooks is.
const hooksScope = "System Hooks, in Admin > Capabilities, are the jobs gohort has outside tools do: reading what arrives into an image the model can read or into text, writing documents as formats, and media (frames, sound, transcription, downloads). Each job has a native flow, its ideal tool. Where that tool is missing, or the admin wants another, the job is adapted: a flow of commands installed on this machine that keeps the job's contract. Each step runs as written, with no shell: {in} is the file it reads (in a later step, the step before's result), {out} the file it writes ({out.wav} names it for the next program), with no {out} what it prints is its result, and a job that gives several files writes them into {outdir}. A flow is saved off; only the admin turns it on."

// lastTests keeps each row's last test, in memory: a failed one marks the
// row for fixing and says why, until a test passes or the flow changes.
var lastTests = struct {
	sync.Mutex
	m map[string]testOutcome
}{m: map[string]testOutcome{}}

type testOutcome struct {
	OK      bool
	Message string
	Log     string
	At      time.Time
}

func recordTest(id string, o testOutcome) {
	lastTests.Lock()
	defer lastTests.Unlock()
	lastTests.m[id] = o
}

func forgetTest(id string) {
	lastTests.Lock()
	defer lastTests.Unlock()
	delete(lastTests.m, id)
}

// failedTest is a row's last test when it failed.
func failedTest(id string) (testOutcome, bool) {
	lastTests.Lock()
	defer lastTests.Unlock()
	o, ok := lastTests.m[id]
	return o, ok && !o.OK
}

// broken reports whether a row needs fixing: it cannot run here, or its
// last test failed.
func (row depRow) broken() bool {
	switch row.State {
	case "missing", "stale", "nosandbox":
		return true
	}
	_, failed := failedTest(row.ID)
	return failed
}

// markFix sets a row's fix flags and its plain Adapt / Edit, which the Fix
// takes the place of on a broken row.
func (row *depRow) markFix() {
	b := row.broken()
	row.FixJob, row.Adapt = row.Job && b, row.Job && !b
	row.FixFill, row.EditFill = row.Fills && b, row.Fills && !b
	row.FixCustom, row.EditCustom = row.Custom && b, row.Custom && !b
	if o, failed := failedTest(row.ID); failed {
		row.Detail = strings.TrimSpace(row.Detail + " Last test failed: " + o.Message)
	}
}

// rowLine is one row as the assistant reads it.
func rowLine(row depRow) string {
	states := map[string]string{"ready": "ready", "installed": "installed", "missing": "not installed", "stale": "stale, update it",
		"nosandbox": "this machine has no sandbox, so it cannot run", "on": "on", "off": "off"}
	var s string
	switch {
	case row.Job:
		j, _ := flows.JobByID(row.ID)
		contract := flows.FormatsLabel(j.Takes) + " into " + j.Becomes.Label()
		if j.IsExport() {
			contract = "the document as " + map[bool]string{true: "HTML", false: "Markdown"}[len(j.Takes) == 1 && j.Takes[0] == "text/html"] + " into " + flows.OutLabel(j.Gives)
		}
		s = fmt.Sprintf("job %s: %s (%s; placeholders %s), by %s; %s", row.ID, row.Enables, contract, strings.Join(j.Placeholders(), " "), row.By, states[row.State])
	case row.Adapted:
		s = fmt.Sprintf("adapted %s: %s, steps %q; %s", row.Name, strings.TrimPrefix(row.Enables, "↳ "), row.By, states[row.State])
	default:
		s = fmt.Sprintf("tool %s: %s, %s; %s (not a job yet: install it)", strings.TrimPrefix(row.ID, "tool:"), row.Enables, row.By, states[row.State])
	}
	if row.Detail != "" {
		s += "; " + row.Detail
	}
	return s
}

// depsText is the whole table as the assistant reads it.
func depsText() string {
	var b strings.Builder
	group := ""
	for _, row := range append(depRows(false), depRows(true)...) {
		if row.Group != group {
			group = row.Group
			b.WriteString("\n" + group + ":\n")
		}
		b.WriteString("  " + rowLine(row) + "\n")
	}
	return b.String()
}

// fixRow finds the row id names: a job, or an adapted flow.
func fixRow(id string) (depRow, bool) {
	for _, row := range append(depRows(false), depRows(true)...) {
		if row.ID == id && (row.Job || row.Adapted) {
			return row, true
		}
	}
	return depRow{}, false
}

// fixAbout is what the assistant is told before anything else: which row,
// what it has to do, why it fails, and what the form beside it is.
func fixAbout(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		// Add: a format gohort does not support, not a row yet.
		return hooksScope + "\n\nThe admin is adding a custom format: an input that reads a format gohort does not (takes formats, becomes an image the model can read or text, or hands its result on as a format something here already reads, for that reader to finish: Pages to PDF, then gohort's PDF reader), or an output that writes documents as a format gohort does not (gives a format, from the document's Markdown or HTML). The form beside this conversation is the new format's; saving it saves it off, under Custom.\n\n" +
			"How to work: ask what format they want if the form does not say, find a program installed here that converts it, and read its help (dependency_check_program). Try your steps with dependency_try, giving the direction and what it takes and becomes (or gives), before you put them in the form. If nothing installed can do it, say which package to install: you cannot install software.\n\n" +
			"What a try runs on: an input, on a file the admin attaches in this window (ask for one when none is listed); an output, on gohort's sample document.", nil
	}
	row, ok := fixRow(id)
	if !ok {
		return "", fmt.Errorf("there is no job or flow called %q", id)
	}
	var b strings.Builder
	b.WriteString(hooksScope + "\n\nThe admin is fixing this row:\n  " + rowLine(row) + "\n")
	if row.Job {
		for _, other := range depRows(false) {
			if !other.Fills {
				continue
			}
			if c, ok := GetConnector(RootDB, other.ID); ok {
				if a, _ := (adaptedFlowHandler{}).adapted(c); a.Job == row.ID {
					b.WriteString("It already has an adapted flow, which may only need testing and turning on:\n  " + rowLine(other) + "\n")
				}
			}
		}
	}
	if o, failed := failedTest(row.ID); failed {
		fmt.Fprintf(&b, "Its last test failed: %s\n", o.Message)
		if o.Log != "" {
			b.WriteString("What it printed (the end):\n" + o.Log + "\n")
		}
	}
	if row.Job {
		b.WriteString("\nThe form beside this conversation is a new flow for this job. Saving it saves the flow off, beside the job.")
	} else {
		b.WriteString("\nThe form beside this conversation is this flow's own. Saving a change to one that is on turns it off until the admin turns it on again.")
	}
	b.WriteString(" You fill it; the admin saves it, tests it from its row, and turns it on.\n\n" +
		"How to work: find a program installed here that can do the job, and read its help (dependency_check_program). Try your steps with dependency_try before you put them in the form, and say what came of it. If nothing installed can do it, say which package to install on this machine's system: you cannot install software. If the sandbox is missing, no flow can run until it is installed (bubblewrap on Linux).\n\n" +
		"What a try runs on: a job that reads a file, on a file the admin attaches in this window (ask for one of the kind it reads when none is listed); a job that writes documents, on gohort's sample document; a download, on a link.")
	return b.String(), nil
}

// fixTools are the assistant's tools: read the table, read a program's
// help, try steps, run what is set up. None of them saves or turns on.
func fixTools(r *http.Request) []AgentToolDef {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "dependency_list",
				Description: "The whole System Hooks table: each job (its id, what it enables, what it takes and gives, its placeholders), what does it here and whether it is ready, not installed, stale or off, the adapted flows, and the tools that are not jobs yet.",
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return depsText(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "dependency_check_program",
				Description: "Whether a program is installed here, and what it prints for a help or version flag, run in the same sandbox a flow runs in (no network, an empty folder). Use it to learn a program's command line before writing steps: how it names its input and output, and how it picks the output format.",
				Parameters: map[string]ToolParam{
					"program": {Type: "string", Description: "The program's name (heif-convert) or full path."},
					"flag":    {Type: "string", Description: "One of --help, -h, -help, help, --version, -version, -V, --usage, or empty for none."},
				},
				Required: []string{"program"},
				Caps:     []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				flag, ok := args["flag"].(string)
				if !ok {
					flag = "--help"
				}
				prog, _ := args["program"].(string)
				return flows.Probe(ctx, prog, strings.TrimSpace(flag))
			},
		},
		{
			Tool: Tool{
				Name:        "dependency_try",
				Description: "Try steps for this row without saving anything: they run exactly as the flow would, keeping this job's contract, and you are told what came of it (the result and whether it passed the job's check, or the error and the end of what the commands printed). Try before you propose steps in the form.",
				Parameters: map[string]ToolParam{
					"steps":        {Type: "string", Description: "The commands, one per line, run in order, e.g. heif-convert {in} {out.jpg}."},
					"timeout_secs": {Type: "integer", Description: "Seconds before it is stopped, all steps together: 60 when left out, 1800 at most."},
					"file":         {Type: "string", Description: "An attached file's id to run on (file-1); the latest when left out. Not for a job that writes documents or takes a link."},
					"direction":    {Type: "string", Description: "Adding a format only: input or output."},
					"takes":        {Type: "string", Description: "Adding an input only: the formats it reads, comma separated (pages, image/heic), or * for any nothing else reads."},
					"becomes":      {Type: "string", Description: "Adding an input only: image or text, or then:<format> to hand the result on to what already reads that format here (then:pdf)."},
					"gives":        {Type: "string", Description: "Adding an output only: its extension (epub, rtf)."},
					"from":         {Type: "string", Description: "Adding an output only: markdown or html, the form of the document it reads."},
					"url":          {Type: "string", Description: "For a job that takes a link (a download): the link to try it on, a public http(s) address."},
				},
				Required: []string{"steps"},
				Caps:     []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return fixTry(ctx, id, args)
			},
		},
		{
			Tool: Tool{
				Name:        "dependency_test",
				Description: "Run a row as it is set up now (a job's native flow, or a saved flow, on or off) exactly as gohort would, and say what came of it. Use it to see how this row fails before changing anything.",
				Parameters: map[string]ToolParam{
					"id":   {Type: "string", Description: "A job's id (read.heic) or an adapted flow's name, from dependency_list. This row when left out."},
					"file": {Type: "string", Description: "An attached file's id to run on (file-1); the latest when left out."},
					"url":  {Type: "string", Description: "For a job that takes a link: the link to test it on."},
				},
				Caps: []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				target, _ := args["id"].(string)
				fid, _ := args["file"].(string)
				link, _ := args["url"].(string)
				return fixTest(ctx, firstNonEmptyStr(strings.TrimSpace(target), id), strings.TrimSpace(fid), strings.TrimSpace(link))
			},
		},
	}
}

// fixTry runs steps for row id, unsaved: on the job's contract for a job,
// on the flow's own for an adapted one. The folders it reads are the saved
// flow's, never the model's.
func fixTry(ctx context.Context, id string, args map[string]any) (string, error) {
	steps, _ := args["steps"].(string)
	str := func(k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	a := flows.Adapted{Job: id}
	if id == "" {
		// Adding a format: its contract is the one being written.
		a = flowForm{Direction: firstNonEmptyStr(str("direction"), "input"), Takes: str("takes"), Becomes: str("becomes"),
			Gives: str("gives"), From: str("from")}.adapted("")
	} else if _, isJob := flows.JobByID(id); !isJob {
		c, ok := GetConnector(RootDB, id)
		if !ok || c.Kind != flows.ConnectorKind {
			return "", fmt.Errorf("there is nothing called %q", id)
		}
		saved, err := adaptedFlowHandler{}.adapted(c)
		if err != nil {
			return "", err
		}
		a = saved
	}
	a.Steps, a.Command = splitLines(steps, false), ""
	a.TimeoutSecs = 0
	switch v := args["timeout_secs"].(type) {
	case float64:
		a.TimeoutSecs = int(v)
	case int:
		a.TimeoutSecs = v
	}
	if err := a.Validate(); err != nil {
		return "", err
	}
	fid, _ := args["file"].(string)
	link, _ := args["url"].(string)
	j, _ := a.Contract()
	in, what, err := fixInput(ctx, j, strings.TrimSpace(fid), strings.TrimSpace(link))
	if err != nil {
		return "", err
	}
	res, _, output, err := runFlowOn(ctx, "try", a, in, strings.TrimSpace(link))
	out := describeRun(res, err, what, output, takesLink(j))
	for _, p := range stepPrograms(a.Flow()) {
		if !flows.Installed(p) {
			out += " " + p + " is not installed on this machine."
		}
	}
	return out + " Nothing was saved.", nil
}

// fixTest runs row id as it is set up.
func fixTest(ctx context.Context, id, fid, link string) (string, error) {
	j, ok := flows.JobByID(id)
	if !ok {
		c, found := GetConnector(RootDB, id)
		if !found || c.Kind != flows.ConnectorKind {
			return "", fmt.Errorf("there is nothing called %q", id)
		}
		a, err := adaptedFlowHandler{}.adapted(c)
		if err != nil {
			return "", err
		}
		j, _ = a.Contract()
	}
	if j.AppRendered() {
		return "", fmt.Errorf("%s is written by the app itself: there is nothing here to test", j.Title)
	}
	in, what, err := fixInput(ctx, j, fid, link)
	if err != nil {
		return "", err
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/?url="+url.QueryEscape(link), nil)
	res, _, output, err := runRow(r, id, in)
	out := describeRun(res, err, what, output, takesLink(j))
	if c, ok := GetConnector(RootDB, id); ok && !c.Approved && err == nil {
		out += " It is off: the admin turns it on from its row."
	}
	return out, nil
}

// fixInput is what a run of job j takes: nothing for an export (the sample
// document), the link for a download, else the attached file.
func fixInput(ctx context.Context, j flows.Job, fid, link string) ([]byte, string, error) {
	switch {
	case j.IsExport():
		return nil, "the sample document", nil
	case takesLink(j):
		if link == "" {
			return nil, "", fmt.Errorf("this job takes a link: give one to run it on")
		}
		return nil, link, nil
	}
	f, err := assist.FileByID(ctx, fid)
	if err != nil {
		return nil, "", err
	}
	return f.Data, f.Label(), nil
}

// describeRun says what came of a run, for the assistant.
func describeRun(res flows.Result, err error, what string, output, link bool) string {
	took := res.Took.Round(100 * time.Millisecond)
	if err != nil {
		out := fmt.Sprintf("Failed on %s after %s: %v", what, took, err)
		if res.Log != "" {
			out += "\nWhat it printed (the end):\n" + res.Log
		}
		return out
	}
	switch {
	case output:
		return fmt.Sprintf("Worked in %s, by %s: it wrote %s as %s of %s, which passed the check.", took, res.By, what, flows.DetectOutput(res.Out), HumanSize(int64(len(res.Out))))
	case link:
		return fmt.Sprintf("Worked in %s, by %s: it fetched %s of %s from the link, which passed the check.", took, res.By, flows.FormatLabel(flows.Sniff(res.Out)), HumanSize(int64(len(res.Out))))
	case len(res.Files) > 0:
		out := fmt.Sprintf("Worked on %s in %s, by %s: it gave %d results, each of which passed the check.", what, took, res.By, len(res.Files))
		if img, _, err := media.DecodeImage(res.Files[0]); err == nil {
			bd := img.Bounds()
			out += fmt.Sprintf(" The first is a %dx%d image.", bd.Dx(), bd.Dy())
		}
		return out
	}
	out := fmt.Sprintf("Worked on %s in %s, by %s: it gave %s of %s, which passed the check.", what, took, res.By, describeResult(res.Out), HumanSize(int64(len(res.Out))))
	if img, _, err := media.DecodeImage(res.Out); err == nil {
		bd := img.Bounds()
		out += fmt.Sprintf(" The image is %dx%d.", bd.Dx(), bd.Dy())
	} else if text := strings.TrimSpace(string(res.Out)); text != "" && flows.Readable(res.Out, flows.KindText) {
		if len(text) > 300 {
			text = text[:300] + "..."
		}
		out += " It begins: " + text
	}
	return out
}

// firstNonEmptyStr is the first of ss that is not empty.
func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a *AdminApp) registerSystemDepsFixRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/system-deps/fix", assist.Handler(assist.Spec{
		Route: "app.admin.system_hooks_fix", Label: "System Hooks: fix a hook", App: "/admin",
		Allow: a.requireAdmin, About: fixAbout, Tools: fixTools,
		Attach: true, Web: true,
	}))
}

// The fix windows. fixAsk is what each opens on.
const fixAsk = "Why can this not run here, and what would fix it?"

// fixPanel is the assist for a row, around form.
func fixPanel(form ui.FormPanel) ui.AssistPanel {
	return ui.AssistPanel{
		Work: form, URL: "api/system-deps/fix?id={id}", Ask: fixAsk,
		Attach: true, Web: true,
		Intro:       "It knows this row and why it fails. It tries steps before it suggests them, and what it suggests fills the form: nothing is saved until you save it. Attach a sample file (📎) for a job that reads one.",
		Placeholder: "Ask about it, or say what to try…",
	}
}

// adaptForm is the form for a new flow filling job {id}.
// Each has its ✨ Get assistance, this row's assistant: told the row, with
// its tools and the 📎 for a sample.
func adaptForm() ui.FormPanel {
	return ui.FormPanel{
		Source:      "api/system-deps/adapt?id={id}",
		PostURL:     "api/system-deps?job={id}",
		SubmitLabel: "Save",
		Fields:      flowFields(true, false),
		Invalidate:  depsSources,
		Assist:      true, AssistURL: "api/system-deps/fix?id={id}", AssistAttach: true,
	}
}

// editForm is the form of the adapted flow {name}.
func editForm(custom bool) ui.FormPanel {
	return ui.FormPanel{
		Source: "api/system-deps/one?name={name}", PostURL: "api/system-deps?edit={name}", SubmitLabel: "Save",
		Fields: flowFields(false, custom), Invalidate: depsSources,
		Assist: true, AssistURL: "api/system-deps/fix?id={id}", AssistAttach: true,
	}
}

// addForm is the form for a format gohort does not support: its assistant
// has no row yet, and takes the format's contract from the form.
func addForm() ui.FormPanel {
	return ui.FormPanel{
		PostURL:     "api/system-deps",
		SubmitLabel: "Save",
		Fields:      flowFields(true, true),
		Invalidate:  depsSources,
		Assist:      true, AssistURL: "api/system-deps/fix", AssistAttach: true,
	}
}

// fixWalkButton opens the walk through every job that cannot run here.
func fixWalkButton() ui.ModalButton {
	p := fixPanel(adaptForm())
	p.Walk = &ui.AssistWalk{Source: "api/system-deps?fix=1", Label: "enables", Empty: "Every job here can run on this machine."}
	return ui.ModalButton{
		Label: "✨ Fix what cannot run here…", Align: "left", Width: ui.AssistWidth,
		Title:    "Fix what cannot run here",
		Subtitle: "One job after another: a flow of tools you have, on the left, with help on the right. Each saves off: test it from its row, then turn it on.",
		Body:     p,
	}
}
