package admin

// Setting up a dependency with Builder: a conversation on the System
// Dependencies section, the admin's own, in which Builder finds a tool for a
// job, reads its help, writes the flow and tries it.
//
// WHY BUILDER NEEDS NO AUTHORITY HERE. What it writes is inert: a flow it
// adapts is saved off, and only the admin's Turn on, on the row, makes
// gohort use it. Changing one that is on turns it off first, so a change
// never runs unapproved. It reads a program's help in the same sandbox a
// flow runs in, and tests on files the admin attached, never on anything of
// this machine's. It installs nothing: it says what to install, and the
// admin does.
//
// The admin's attachments are taken out of the message and kept as samples
// (convSamples), for the test tool to run a job on. A HEIC is exactly what
// needs converting, so it is exactly what the model could not have read.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/ui"
)

// convChatContext is what these conversations are filed under.
const convChatContext = "system-deps"

// convBuilderID is the agent that helps: Builder, as on a file store's Map.
const convBuilderID = "builder"

// convSample is a file the admin attached to the conversation.
type convSample struct {
	ID    string
	Name  string
	Type  string
	Data  []byte
	Added time.Time
}

// convSamples keeps each admin's last few samples, in memory: they are test
// inputs for a conversation, not records, and a restart asks for them again.
var convSamples = struct {
	sync.Mutex
	byUser map[string][]convSample
	seq    map[string]int
}{byUser: map[string][]convSample{}, seq: map[string]int{}}

const (
	convSamplesKept = 4
	convSampleMax   = 64 << 20
)

func addConvSample(user, name string, data []byte) convSample {
	convSamples.Lock()
	defer convSamples.Unlock()
	convSamples.seq[user]++
	s := convSample{ID: fmt.Sprintf("sample-%d", convSamples.seq[user]), Name: name, Type: flows.Sniff(data), Data: data, Added: time.Now()}
	list := append(convSamples.byUser[user], s)
	if len(list) > convSamplesKept {
		list = list[len(list)-convSamplesKept:]
	}
	convSamples.byUser[user] = list
	return s
}

func convSamplesFor(user string) []convSample {
	convSamples.Lock()
	defer convSamples.Unlock()
	return append([]convSample(nil), convSamples.byUser[user]...)
}

func (s convSample) label() string {
	l := fmt.Sprintf("%s (%s, %s", s.ID, flows.FormatLabel(s.Type), HumanSize(int64(len(s.Data))))
	if s.Name != "" {
		l += ", " + s.Name
	}
	return l + ")"
}

// takeConvSamples moves the attachments out of a send body into the admin's
// samples, and says in the message what arrived, so the model is told about
// them rather than handed bytes it may not be able to read.
func takeConvSamples(r *http.Request, user string) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 96<<20))
	r.Body.Close()
	restore := func(b []byte) {
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
	}
	if err != nil {
		return err
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body == nil {
		restore(raw)
		return nil
	}
	var got []string
	keep := func(name, b64 string) {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(data) == 0 || len(data) > convSampleMax {
			return
		}
		got = append(got, addConvSample(user, name, data).label())
	}
	if imgs, ok := body["images"].([]any); ok {
		for i, v := range imgs {
			if s, ok := v.(string); ok {
				keep(fmt.Sprintf("attachment %d", i+1), s)
			}
		}
	}
	if docs, ok := body["documents"].([]any); ok {
		for _, v := range docs {
			if d, ok := v.(map[string]any); ok {
				name, _ := d["name"].(string)
				data, _ := d["data"].(string)
				keep(name, data)
			}
		}
	}
	delete(body, "images")
	delete(body, "documents")
	if len(got) > 0 {
		msg, _ := body["message"].(string)
		note := "[I attached sample files for testing conversions: " + strings.Join(got, "; ") + ". They are not shown to you; file_conversion_test converts one.]"
		body["message"] = strings.TrimSpace(note + "\n\n" + msg)
	}
	body["app_context"] = convChatContext
	stamped, err := json.Marshal(body)
	if err != nil {
		restore(raw)
		return nil
	}
	restore(stamped)
	return nil
}

// convBuilder resolves the orchestrate app and the admin's Builder. Writes
// the error itself.
func (a *AdminApp) convBuilder(w http.ResponseWriter, r *http.Request) (*orchestrate.OrchestrateApp, orchestrate.AgentRecord, bool) {
	var none orchestrate.AgentRecord
	if !a.requireAdmin(w, r) {
		return nil, none, false
	}
	ag, ok := FindAgent("orchestrate")
	orch, _ := ag.(*orchestrate.OrchestrateApp)
	if !ok || orch == nil {
		http.Error(w, "the agent runtime is not available", http.StatusServiceUnavailable)
		return nil, none, false
	}
	agent, ok := orch.LookupAppAgent(AuthCurrentUser(r), convBuilderID)
	if !ok {
		http.Error(w, "Builder is not available", http.StatusServiceUnavailable)
		return nil, none, false
	}
	return orch, agent, true
}

func (a *AdminApp) registerSystemDepsChatRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/system-deps/chat/send", func(w http.ResponseWriter, r *http.Request) {
		orch, agent, ok := a.convBuilder(w, r)
		if !ok {
			return
		}
		user := AuthCurrentUser(r)
		if err := takeConvSamples(r, user); err != nil {
			http.Error(w, "the message could not be read", http.StatusBadRequest)
			return
		}
		orch.PublicHandleSendWithAppTools(w, r, agent, convBuilderTools(user))
	})
	// The conversations, to go back to one: the list, and one to reopen or
	// delete.
	sub.HandleFunc("/api/system-deps/chat/sessions", func(w http.ResponseWriter, r *http.Request) {
		orch, agent, ok := a.convBuilder(w, r)
		if !ok {
			return
		}
		orch.PublicHandleSessionListOnly(w, r, agent.ID, convChatContext)
	})
	sub.HandleFunc("/api/system-deps/chat/sessions/", func(w http.ResponseWriter, r *http.Request) {
		orch, agent, ok := a.convBuilder(w, r)
		if !ok {
			return
		}
		orch.PublicHandleSessionOne(w, r, agent.ID, strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/system-deps/chat/sessions/"), "/"))
	})
	sub.HandleFunc("/api/system-deps/chat/cancel", func(w http.ResponseWriter, r *http.Request) {
		orch, agent, ok := a.convBuilder(w, r)
		if !ok {
			return
		}
		orch.PublicHandleCancel(w, r, agent)
	})
}

// convBuilderTools are Builder's tools in this conversation: read the
// table, read a program's help, adapt a job, test a row. Nothing that turns
// one on or installs anything.
func convBuilderTools(user string) []AgentToolDef {
	scope := " You are helping an admin with System Hooks in Admin > Capabilities: the jobs gohort has outside tools do (reading what arrives into an image the model can read or into text, writing documents as formats), each with a native flow, its ideal tool. Where that tool is missing, or the admin wants another, you adapt the job: a flow of commands that are installed here, which keeps the job's contract. A flow you adapt is saved off; only the admin turns it on. You cannot install software: when a program is missing, say which package provides it on this machine's system and let the admin install it."
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "dependency_list",
				Description: "The table: each job (its id, what it enables, what it takes and gives, its placeholders), what does it here (its native tool and version, built in, or a flow the admin adapted), whether it is ready, not installed, stale or off, and what to install; the tools that are not jobs yet; and the sample files the admin attached. Read this first." + scope,
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return depsText(user), nil
			},
		},
		{
			Tool: Tool{
				Name:        "dependency_check_program",
				Description: "Whether a program is installed here, and what it prints for a help or version flag, run in the same sandbox a flow runs in (no network, an empty folder). Use it to learn a tool's command line before adapting a job: how it names its input and output, and how it picks the output format." + scope,
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
				Name:        "dependency_adapt",
				Description: "Adapt a job: save a flow, off. To fill one of gohort's jobs, give its id (job) and the steps; it keeps that job's contract. To add a format gohort does not support, leave job out and say what the flow is: an input that takes formats and becomes image or text, or an output that gives a format from the document's markdown or html. Each step is a command run as written with no shell; {in} is the file it reads (in a later step, the step before's result); {out} the file it writes (write {out.wav} to name it for the next program); with no {out}, what it prints is its result; a job that gives several files writes them into {outdir}. Changing one that is on turns it off until the admin turns it on again." + scope,
				Parameters: map[string]ToolParam{
					"name":         {Type: "string", Description: "Lowercase letters, numbers, - and _, e.g. heic-magick. An existing name changes that flow."},
					"job":          {Type: "string", Description: "The id of the job it fills, from dependency_list (read.heic, read.pdf, export.docx). Leave out to add a format."},
					"steps":        {Type: "string", Description: "The commands, one per line, run in order, e.g. heif-convert {in} {out.jpg}, or for frame sampling ffmpeg -i {in} -vf fps=1 -frames:v {count} {outdir}/f%02d.jpg."},
					"reads":        {Type: "string", Description: "Folders it needs to read, comma separated, full paths: a model file, say. Nothing else on the machine is visible to it."},
					"direction":    {Type: "string", Description: "Adding a format only: input or output."},
					"takes":        {Type: "string", Description: "Adding an input format: the formats it reads, comma separated, short or full (pages, image/heic), or * for any nothing else reads."},
					"becomes":      {Type: "string", Description: "Adding an input format: image or text."},
					"gives":        {Type: "string", Description: "Adding an output format: its extension (epub, rtf)."},
					"from":         {Type: "string", Description: "Adding an output format: markdown or html, the form of the document it reads."},
					"timeout_secs": {Type: "integer", Description: "Seconds before it is stopped, all steps together: 60 when left out, 1800 at most."},
					"prefer":       {Type: "boolean", Description: "True to use it before the native flow even where that one is installed. Leave it false to fill in for a missing tool."},
				},
				Required: []string{"name", "steps"},
				Caps:     []Capability{CapWrite},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return builderAdapt(user, args)
			},
		},
		{
			Tool: Tool{
				Name:        "dependency_test",
				Description: "Run one row (a job's native flow, or an adapted flow, on or off) exactly as gohort would, and say what came of it: the result and whether it passed the job's check, or the error and the end of what the commands printed. A job that reads a file runs on a sample the admin attached (ask for one when there is none); an export writes gohort's sample document, so it needs no attachment." + scope,
				Parameters: map[string]ToolParam{
					"id":     {Type: "string", Description: "A job's id (read.heic, export.docx) or an adapted flow's name, from dependency_list."},
					"sample": {Type: "string", Description: "A sample id from dependency_list (sample-1). The latest when left out."},
					"url":    {Type: "string", Description: "For a job that takes a link (a video download): the link to test it on, a public http(s) address."},
				},
				Required: []string{"id"},
				Caps:     []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				id, _ := args["id"].(string)
				sid, _ := args["sample"].(string)
				link, _ := args["url"].(string)
				return builderTest(ctx, user, strings.TrimSpace(id), strings.TrimSpace(sid), strings.TrimSpace(link))
			},
		},
	}
}

// depsText is dependency_list's answer.
func depsText(user string) string {
	var b strings.Builder
	states := map[string]string{"ready": "ready", "installed": "installed", "missing": "not installed", "stale": "stale, update it",
		"nosandbox": "this machine has no sandbox, so it cannot run", "on": "on", "off": "off"}
	group := ""
	for _, row := range append(depRows(false), depRows(true)...) {
		if row.Group != group {
			group = row.Group
			b.WriteString("\n" + group + ":\n")
		}
		switch {
		case row.Job:
			j, _ := flows.JobByID(row.ID)
			contract := flows.FormatsLabel(j.Takes) + " into " + j.Becomes.Label()
			if j.IsExport() {
				contract = "the document as " + map[bool]string{true: "HTML", false: "Markdown"}[len(j.Takes) == 1 && j.Takes[0] == "text/html"] + " into " + flows.OutLabel(j.Gives)
			}
			fmt.Fprintf(&b, "  job %s: %s (%s; placeholders %s), by %s; %s", row.ID, row.Enables, contract, strings.Join(j.Placeholders(), " "), row.By, states[row.State])
		case row.Adapted:
			fmt.Fprintf(&b, "  adapted %s: %s, steps %q; %s", row.Name, strings.TrimPrefix(row.Enables, "↳ "), row.By, states[row.State])
		default:
			fmt.Fprintf(&b, "  tool %s: %s, %s; %s (not a job yet: install it)", strings.TrimPrefix(row.ID, "tool:"), row.Enables, row.By, states[row.State])
		}
		if row.Detail != "" {
			b.WriteString("; " + row.Detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nSamples attached:\n")
	samples := convSamplesFor(user)
	for _, s := range samples {
		b.WriteString("  " + s.label() + "\n")
	}
	if len(samples) == 0 {
		b.WriteString("  none: ask the admin to attach a file of the kind the job reads\n")
	}
	return b.String()
}

// builderAdapt saves Builder's flow, off.
func builderAdapt(user string, args map[string]any) (string, error) {
	str := func(k string) string { s, _ := args[k].(string); return strings.TrimSpace(s) }
	timeout := 0
	switch v := args["timeout_secs"].(type) {
	case float64:
		timeout = int(v)
	case int:
		timeout = v
	}
	prefer, _ := args["prefer"].(bool)
	job := str("job")
	if job != "" {
		if _, ok := flows.JobByID(job); !ok {
			return "", fmt.Errorf("there is no job %q: the ids are in dependency_list", job)
		}
	}
	ff := flowForm{Name: str("name"), Direction: firstNonEmptyStr(str("direction"), "input"), Takes: str("takes"), Becomes: str("becomes"),
		Gives: str("gives"), From: str("from"), Steps: stepsOf(splitLines(str("steps"), false)), Reads: str("reads"), TimeoutSecs: timeout, Prefer: prefer}
	ad := ff.adapted(job)
	_, existed := GetConnector(RootDB, ff.Name)
	turnedOff, err := saveAdapted(user, ff.Name, ad, existed, "Adapted with Builder in System Hooks")
	if err != nil {
		return "", err
	}
	j, _ := ad.Contract()
	msg := fmt.Sprintf("Saved %s, off: %s, with %s. ", ff.Name, j.Title, strings.Join(stepPrograms(ad.Flow()), " then "))
	if turnedOff {
		msg += "It was on, so it is off now until the admin turns it on again. "
	}
	for _, p := range stepPrograms(ad.Flow()) {
		if !flows.Installed(p) {
			msg += p + " is not installed on this machine yet. "
		}
	}
	return msg + "Test it with dependency_test before the admin turns it on.", nil
}

// builderTest runs a row, on one of the admin's samples when it reads a
// file.
func builderTest(ctx context.Context, user, id, sid, link string) (string, error) {
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/?url="+url.QueryEscape(link), nil)
	if isLinkRow(id) {
		if link == "" {
			return "", fmt.Errorf("this job takes a link: give one to test it on")
		}
		res, _, _, err := runRow(r, id, nil)
		took := res.Took.Round(100 * time.Millisecond)
		if err != nil {
			out := fmt.Sprintf("Failed on %s after %s: %v", link, took, err)
			if res.Log != "" {
				out += "\nWhat it printed (the end):\n" + res.Log
			}
			return out, nil
		}
		return fmt.Sprintf("Worked in %s, by %s: it fetched %s of %s from the link, which passed the check.", took, res.By, flows.FormatLabel(flows.Sniff(res.Out)), HumanSize(int64(len(res.Out)))), nil
	}
	if isOutputRow(id) {
		res, _, _, err := runRow(r, id, nil)
		took := res.Took.Round(100 * time.Millisecond)
		if err != nil {
			out := fmt.Sprintf("Failed on the sample document after %s: %v", took, err)
			if res.Log != "" {
				out += "\nWhat it printed (the end):\n" + res.Log
			}
			return out, nil
		}
		out := fmt.Sprintf("Worked in %s, by %s: it wrote the sample document as %s of %s, which passed the check.", took, res.By, flows.DetectOutput(res.Out), HumanSize(int64(len(res.Out))))
		if c, ok := GetConnector(RootDB, id); ok && !c.Approved {
			out += " It is off: the admin turns it on from its row."
		}
		return out, nil
	}
	samples := convSamplesFor(user)
	if len(samples) == 0 {
		return "", fmt.Errorf("no sample is attached: ask the admin to attach a file of the kind this job reads")
	}
	s := samples[len(samples)-1]
	if sid != "" {
		found := false
		for _, x := range samples {
			if x.ID == sid {
				s, found = x, true
			}
		}
		if !found {
			return "", fmt.Errorf("there is no %s: the samples are those listed by dependency_list", sid)
		}
	}
	res, _, _, err := runRow(r, id, s.Data)
	took := res.Took.Round(100 * time.Millisecond)
	if err != nil {
		out := fmt.Sprintf("Failed on %s after %s: %v", s.label(), took, err)
		if res.Log != "" {
			out += "\nWhat it printed (the end):\n" + res.Log
		}
		return out, nil
	}
	if len(res.Files) > 0 {
		out := fmt.Sprintf("Worked on %s in %s, by %s: it gave %d results, each of which passed the check.", s.label(), took, res.By, len(res.Files))
		if img, _, err := media.DecodeImage(res.Files[0]); err == nil {
			bd := img.Bounds()
			out += fmt.Sprintf(" The first is a %dx%d image.", bd.Dx(), bd.Dy())
		}
		return out, nil
	}
	out := fmt.Sprintf("Worked on %s in %s, by %s: it gave %s of %s, which passed the check.", s.label(), took, res.By, describeResult(res.Out), HumanSize(int64(len(res.Out))))
	if img, _, err := media.DecodeImage(res.Out); err == nil {
		bd := img.Bounds()
		out += fmt.Sprintf(" The image is %dx%d.", bd.Dx(), bd.Dy())
	} else if text := strings.TrimSpace(string(res.Out)); text != "" && flows.Readable(res.Out, flows.KindText) {
		if len(text) > 300 {
			text = text[:300] + "..."
		}
		out += " It begins: " + text
	}
	if c, ok := GetConnector(RootDB, id); ok && !c.Approved {
		out += " It is off: the admin turns it on from its row."
	}
	return out, nil
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

// depsBuilderPanel is the conversation on the System Hooks section.
func depsBuilderPanel() ui.AgentLoopPanel {
	return ui.AgentLoopPanel{
		SendURL:      "api/system-deps/chat/send",
		ListURL:      "api/system-deps/chat/sessions",
		LoadURL:      "api/system-deps/chat/sessions/{id}",
		DeleteURL:    "api/system-deps/chat/sessions/{id}",
		CancelURL:    "api/system-deps/chat/cancel",
		Invalidate:   depsSources,
		ListTitle:    "Earlier conversations",
		NewLabel:     "New",
		ListPosition: "top",
		Markdown:     true,
		LockActivity: true,
		Attachments:  true,
		Height:       "420px",
		Placeholder:  "Ask Builder to adapt a missing dependency. Attach a sample file to test on.",
		Actions: []ui.ToolbarAction{
			{Label: "What is missing?", Variant: "primary",
				Title:  "Go through the table and say what cannot run here",
				Prompt: "Which jobs cannot run on this machine, and for each, what would you install, or which installed tool could you adapt it to?"},
			{Label: "Adapt a missing one",
				Title:  "Pick a job that cannot run here and adapt it to a tool that is installed",
				Prompt: "Pick a job that cannot run here (ask me which if there are several), find a tool that is installed that can do it, read its help, adapt the job and test it, on a sample I attach if it reads a file. If nothing suitable is installed, tell me what to install."},
			{Label: "Add a format",
				Title:  "Read or write a format gohort does not support",
				Prompt: "I want gohort to read or write another format. Ask me which one, then find a tool here that does it, add it and test it."},
			{Label: "Fix a failing one",
				Title:  "Test what is set up and work out why something fails",
				Prompt: "Test the rows that are set up, on the sample I attached where they read a file, and for any that fails, work out why and propose a fix."},
		},
	}
}
