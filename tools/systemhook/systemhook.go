// Package systemhook is the convert_file tool (once named system_hook, for
// where its formats are set up rather than what it does): an agent runs one of this
// machine's System Hooks (core/flows) on a file in its workspace, and gets
// what it gives back as files there.
//
// It offers the formats an admin ADDED (System Hooks > Custom), one by one
// as they turn Agents on for it: nothing is offered until one is, and the
// tool is in no catalog until then. Once it is, it is an ordinary tool:
// listed in agents' tool pickers, curated like the rest, and its schema
// names only the hooks that are on and can run here. gohort's own jobs are
// not offered here: agents already reach them through gohort's own tools
// (video, export, attachments), which run as those jobs.
//
// A hook reads one file and writes new ones in a sandboxed folder of its own
// with no network, so running one changes nothing outside the agent's
// workspace: the tool declares that it reads. The one that needs the network
// (a video download, from a link) declares that too, per call (ActionCaps),
// and is not offered to a session that may not go online.
package systemhook

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
)

func init() { RegisterChatTool(&HookTool{}) }

// offeredTable holds which hooks agents may run: a job's id, or a custom
// flow's name, set to true.
const offeredTable = "system_hook_agents"

// Offered reports whether agents may run the hook called id.
func Offered(id string) bool {
	if RootDB == nil {
		return false
	}
	var on bool
	return RootDB.Get(offeredTable, id, &on) && on
}

// SetOffered lets agents run the hook called id, or stops them.
func SetOffered(id string, on bool) {
	if RootDB == nil {
		return
	}
	if on {
		RootDB.Set(offeredTable, id, true)
		return
	}
	RootDB.Unset(offeredTable, id)
}

// hook is one added format an agent may run now: its flow and contract.
type hook struct {
	ID  string
	Job flows.Job
	a   flows.Adapted
}

// available are the hooks agents may run now: added formats that are on
// and offered. online is whether the session may use the network: a hook
// that needs it is left out of one that may not.
func available(online bool) []hook {
	var out []hook
	if RootDB != nil {
		for _, c := range ListConnectors(RootDB) {
			if c.Kind != flows.ConnectorKind || !c.Approved || !Offered(c.Name) {
				continue
			}
			if a, j, ok := flows.Live(c.Name); ok && a.Job == "" && (online || !j.Network) {
				out = append(out, hook{ID: c.Name, Job: j, a: a})
			}
		}
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// HookTool is convert_file.
type HookTool struct{}

func (t *HookTool) Name() string { return "convert_file" }

func (t *HookTool) Desc() string {
	return "Run one of this machine's System Hooks on a file in your workspace: turn a file into something you can read (an image, text), sample frames or details from a video, transcribe audio, or write a Markdown document as another format. The result is saved as files in your workspace, and text results are also returned."
}

func (t *HookTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Description: "Which hook to run."},
		"path":   {Type: "string", Description: "Workspace-relative path to the file to run it on."},
		"count":  {Type: "integer", Description: "For a hook that takes a count: how many."},
		"url":    {Type: "string", Description: "For a hook that takes a link: the link."},
	}
}

// SchemaWithSession offers the hooks that are on and can run, and is nil
// (so the tool is in no catalog) when there are none.
func (t *HookTool) SchemaWithSession(sess *ToolSession) (string, map[string]ToolParam) {
	hooks := available(sess == nil || sess.NetworkAllowed())
	if len(hooks) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(t.Desc() + " The hooks:\n")
	ids := make([]string, len(hooks))
	paramHelp := map[string][]string{}
	for i, h := range hooks {
		ids[i] = h.ID
		fmt.Fprintf(&b, "- %s: %s; %s.", h.ID, h.Job.Title, contract(h.Job))
		for _, p := range h.Job.Params {
			fmt.Fprintf(&b, " Takes %s (%s).", p.Name, p.Help)
			paramHelp[p.Name] = append(paramHelp[p.Name], h.ID)
		}
		b.WriteString("\n")
	}
	params := map[string]ToolParam{
		"action": {Type: "string", Description: "Which hook to run.", Enum: ids},
		"path":   {Type: "string", Description: "Workspace-relative path to the file to run it on: the file it reads, or for one that writes a format, the Markdown (or HTML) document. Not needed for a hook that takes a link."},
	}
	if hs, ok := paramHelp["count"]; ok {
		params["count"] = ToolParam{Type: "integer", Description: "For " + strings.Join(hs, ", ") + ": how many."}
	}
	if hs, ok := paramHelp["url"]; ok {
		params["url"] = ToolParam{Type: "string", Description: "For " + strings.Join(hs, ", ") + ": the link, a public http(s) address."}
	}
	return strings.TrimSpace(b.String()), params
}

// contract says what a hook takes and gives, for the schema.
func contract(j flows.Job) string {
	switch {
	case j.IsExport():
		return "takes a Markdown or HTML document, gives " + flows.OutLabel(j.Gives)
	case j.Files:
		return "takes " + flows.FormatsLabel(j.Takes) + ", gives several images"
	case !j.TakesFile():
		return "takes a link, gives " + j.Becomes.Label() + " (it goes online)"
	}
	if j.HandsOff() {
		return "takes " + flows.FormatsLabel(j.Takes) + ", gives " + flows.FormatLabel(j.Then)
	}
	return "takes " + flows.FormatsLabel(j.Takes) + ", gives " + j.Becomes.Label()
}

// Caps are what a call can do: read, and go online where a hook that needs
// the network is offered.
func (t *HookTool) Caps() []Capability {
	for _, h := range available(true) {
		if h.Job.Network {
			return []Capability{CapRead, CapNetwork}
		}
	}
	return []Capability{CapRead}
}

// ActionCaps says which hooks go online: only those calls are treated as
// reaching the network.
func (t *HookTool) ActionCaps() map[string][]Capability {
	out := map[string][]Capability{}
	for _, h := range available(true) {
		if h.Job.Network {
			out[h.ID] = []Capability{CapRead, CapNetwork}
		} else {
			out[h.ID] = []Capability{CapRead}
		}
	}
	return out
}

func (t *HookTool) Category() string { return "System hooks" }

func (t *HookTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("convert_file needs a session with a workspace")
}

// maxFile bounds the file a hook is run on.
const maxFile = 256 << 20

// textInline is how much of a text result is returned in the reply.
const textInline = 6000

func (t *HookTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("convert_file needs a session with a workspace")
	}
	id := strings.TrimSpace(StringArg(args, "action"))
	var h *hook
	var names []string
	for _, x := range available(sess.NetworkAllowed()) {
		names = append(names, x.ID)
		if x.ID == id {
			x := x
			h = &x
		}
	}
	if h == nil {
		if len(names) == 0 {
			return "", fmt.Errorf("no system hooks are offered to agents here")
		}
		return "", fmt.Errorf("%q is not a hook you can run here: the ones you can are %s", id, strings.Join(names, ", "))
	}
	in := flows.Input{Params: map[string]string{}}
	for _, p := range h.Job.Params {
		v := strings.TrimSpace(fmt.Sprint(args[p.Name]))
		if args[p.Name] == nil || v == "" {
			v = p.Default
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil && p.Name != "url" {
			v = strconv.Itoa(int(f))
		}
		in.Params[p.Name] = v
	}
	rel := strings.TrimSpace(StringArg(args, "path"))
	var data []byte
	if h.Job.TakesFile() || h.Job.IsExport() {
		if rel == "" {
			return "", fmt.Errorf("path is required: the workspace file to run %s on", h.ID)
		}
		abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			return "", fmt.Errorf("there is no file %q in your workspace", rel)
		}
		if st.Size() > maxFile {
			return "", fmt.Errorf("%s is %d MB, over the %d MB a hook takes", rel, st.Size()>>20, maxFile>>20)
		}
		if data, err = os.ReadFile(abs); err != nil {
			return "", err
		}
		in.Data, in.From, in.Name = data, flows.Sniff(data), filepath.Base(rel)
	} else {
		// A hook that takes a link names its result for where it came from.
		rel = linkName(in.Params["url"])
	}
	ctx := sess.ContextWithNetworkConnector(sess.Context())
	var err error
	var res flows.Result
	switch {
	case h.Job.IsExport():
		doc := flows.Document{Title: strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))}
		if strings.Contains(http.DetectContentType(data), "html") || strings.HasSuffix(strings.ToLower(rel), ".html") {
			doc.HTML = data
		} else {
			doc.Markdown = data
		}
		res, err = flows.RunAdapted(ctx, h.ID, h.a, flows.Input{Doc: &doc})
	default:
		res, err = flows.RunAdapted(ctx, h.ID, h.a, in)
	}
	if err != nil {
		return "", fmt.Errorf("%s did not work on %s: %v", h.Job.Title, firstNonEmpty(StringArg(args, "path"), in.Params["url"]), err)
	}
	return save(sess.WorkspaceDir, rel, *h, res)
}

// save writes a hook's results next to where they came from, never over a
// file that is there, and says what it wrote.
func save(root, rel string, h hook, res flows.Result) (string, error) {
	base := strings.TrimSuffix(rel, filepath.Ext(rel))
	var wrote []string
	write := func(name string, b []byte) error {
		name = unique(root, name)
		if err := os.WriteFile(filepath.Join(root, name), b, 0o644); err != nil {
			return err
		}
		wrote = append(wrote, fmt.Sprintf("%s (%s)", name, HumanSize(int64(len(b)))))
		return nil
	}
	if len(res.Files) > 0 {
		for i, f := range res.Files {
			if err := write(fmt.Sprintf("%s-%02d%s", base, i+1, extOf(f, h.Job)), f); err != nil {
				return "", err
			}
		}
	} else if err := write(base+suffix(h)+extOf(res.Out, h.Job), res.Out); err != nil {
		return "", err
	}
	from := rel
	if !h.Job.TakesFile() && !h.Job.IsExport() {
		from = "the link"
	}
	msg := fmt.Sprintf("Ran %s on %s, by %s: wrote %s to your workspace.", h.Job.Title, from, res.By, strings.Join(wrote, ", "))
	if h.Job.Becomes == flows.KindText && !h.Job.Files && !h.Job.IsExport() {
		text := strings.TrimSpace(string(res.Out))
		if len(text) > textInline {
			text = text[:textInline] + "\n... (the rest is in the file)"
		}
		msg += "\n\n" + text
	}
	return msg, nil
}

// suffix keeps a result from taking its source's name when both would end
// the same (a TIFF made readable is still an image).
func suffix(h hook) string {
	switch {
	case h.Job.IsExport():
		return ""
	case h.Job.Becomes == flows.KindText:
		return ""
	}
	return "-" + strings.TrimPrefix(strings.TrimPrefix(h.ID, "read."), "media.")
}

// extOf is the extension for a result, by what it is.
func extOf(b []byte, j flows.Job) string {
	if j.IsExport() {
		return "." + j.Gives
	}
	switch flows.Sniff(b) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "audio/wave":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "application/ogg":
		return ".ogg"
	case "audio/mp4":
		return ".m4a"
	case "audio/flac":
		return ".flac"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/x-matroska":
		return ".mkv"
	}
	return ".txt"
}

// unique is name, or name with -2, -3... where name is taken.
func unique(root, name string) string {
	if _, err := os.Stat(filepath.Join(root, name)); err != nil {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(root, n)); err != nil {
			return n
		}
	}
}

// linkName is a file name for what a link gave: its host and last path
// segment, letters and digits only.
func linkName(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return "download"
	}
	raw := strings.TrimPrefix(u.Hostname(), "www.") + "-" + filepath.Base(strings.TrimSuffix(u.Path, "/"))
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '.':
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 60 {
		name = name[:60]
	}
	if name == "" {
		return "download"
	}
	return name + ".link"
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
