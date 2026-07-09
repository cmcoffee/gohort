// LLM-facing `export` grouped tool. Turns structured data into a
// downloadable document (pdf / xlsx / docx / pptx, or a runtime-defined
// format) and delivers it to the user as an attachment.
//
// The heavy lifting lives in core's export registry (core.ExportFormat /
// core.RunExportFormat) and the managed-python-deps primitive
// (core.EnsurePyDeps). This package is just the tool surface: it maps
// LLM args onto an ExportInput, runs the resolved format, and appends
// the resulting bytes to the session's file-delivery channel.
//
// Extensibility has two tiers, both routing through the same executor:
//   - Built-in / app formats: registered in-code via
//     core.RegisterExportFormat (see core/export_registry.go).
//   - Runtime per-user formats: authored by the LLM via action="define",
//     stored per-user in the DB, and resolved by action="create".

package export

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxExportBytes caps a delivered document, matching the attach cap.
const maxExportBytes = 20 * 1024 * 1024

// userFormatsTable holds per-user runtime-defined formats, keyed
// owner+":"+name. GoGen is `json:"-"` so only script formats persist.
const userFormatsTable = "export_user_formats"

// validFormatName restricts format + interpreter names to identifier
// characters, so a name can never inject anything downstream.
var validFormatName = regexp.MustCompile(`^[a-z0-9_\-]{1,40}$`)

// allowedInterps are the interpreters a user-defined format may run.
var allowedInterps = map[string]bool{"python3": true, "node": true}

func init() {
	gt := NewGroupedTool("export",
		"Generate a downloadable document (PDF, Excel .xlsx, Word .docx, PowerPoint .pptx, or a format you define) from structured data and deliver it to the user. Use when the user asks for a file/report/spreadsheet/deck they can download — not for showing content inline in chat.")

	gt.AddAction("formats", &GroupedToolAction{
		Description: "List the available export formats (built-in + any you've defined) with the data shape each expects. Call this FIRST if you're unsure which format to use or how to shape the `data` payload.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler:     handleFormats,
	})

	gt.AddAction("create", &GroupedToolAction{
		Description: "Generate a document in the given format and deliver it to the user as a download. Provide `data` in the shape that format documents (see action=formats). The file ships with your reply automatically.",
		Params: map[string]ToolParam{
			"format":   {Type: "string", Description: "Format name: pdf, xlsx, docx, pptx, or one you defined. See action=formats."},
			"data":     {Type: "object", Description: "The content payload, in the shape the chosen format expects (action=formats documents each). May be an object, array, or a string (e.g. markdown for pdf/docx)."},
			"filename": {Type: "string", Description: "Optional download filename. The correct extension is appended if missing. Defaults to the title (or format name) + extension."},
			"title":    {Type: "string", Description: "Optional document title / cover heading (used by pdf, docx, pptx)."},
			"date":     {Type: "string", Description: "Optional date string shown under the title (pdf)."},
		},
		Required: []string{"format", "data"},
		Caps:     []Capability{CapRead, CapWrite},
		Handler:  handleCreate,
	})

	gt.AddAction("define", &GroupedToolAction{
		Description: "Register a NEW reusable export format backed by a generator script. The script reads the ExportInput JSON ({title,date,data}) on stdin and writes the file's bytes base64-encoded to stdout on a line beginning with the marker \"" + ExportB64Marker + "\". Any pip packages it imports must be listed in py_requires — they're provisioned (sandboxed, host-side) at define time so create is fast. Use when a user wants a format the built-ins don't cover (CSV, an invoice .xlsx with your layout, a branded .docx, etc.).",
		Params: map[string]ToolParam{
			"name":        {Type: "string", Description: "Format name (lowercase identifier: letters, digits, _, -). Cannot shadow a built-in (pdf/xlsx/docx/pptx)."},
			"ext":         {Type: "string", Description: "File extension including the dot, e.g. \".csv\". Defaults to \".\"+name."},
			"mime":        {Type: "string", Description: "MIME type for the download, e.g. \"text/csv\". Optional but recommended."},
			"desc":        {Type: "string", Description: "One-line description shown in action=formats."},
			"input_hint":  {Type: "string", Description: "Documents the expected `data` shape for future create calls."},
			"py_requires": {Type: "array", Description: "pip package specs the script imports, e.g. [\"pandas\"]. Provisioned at define time. Omit for stdlib-only scripts.", Items: &ToolParam{Type: "string"}},
			"interpreter": {Type: "string", Description: "Interpreter for the script: \"python3\" (default) or \"node\"."},
			"script":      {Type: "string", Description: "Generator body. Reads ExportInput JSON on stdin; prints \"" + ExportB64Marker + "\" + base64(file bytes) to stdout."},
		},
		Required:     []string{"name", "script"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler:      handleDefine,
	})

	gt.AddAction("undefine", &GroupedToolAction{
		Description: "Delete a format you previously defined. Built-in formats can't be removed.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "The defined format's name."},
		},
		Required:     []string{"name"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler:      handleUndefine,
	})

	RegisterChatTool(gt)
}

// ----------------------------------------------------------------------
// handlers
// ----------------------------------------------------------------------

func handleFormats(_ map[string]any, sess *ToolSession) (string, error) {
	var b strings.Builder
	b.WriteString("Built-in formats:\n")
	builtins := ListExportFormats()
	sort.Slice(builtins, func(i, j int) bool { return builtins[i].Name < builtins[j].Name })
	for _, f := range builtins {
		fmt.Fprintf(&b, "- %s (%s) — %s\n    data: %s\n", f.Name, f.Ext, f.Desc, f.InputHint)
	}
	if user := listUserFormats(sess); len(user) > 0 {
		b.WriteString("\nYour defined formats:\n")
		for _, f := range user {
			hint := f.InputHint
			if hint == "" {
				hint = "(no input hint provided)"
			}
			fmt.Fprintf(&b, "- %s (%s) — %s\n    data: %s\n", f.Name, f.Ext, f.Desc, hint)
		}
	}
	return b.String(), nil
}

func handleCreate(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", Error("export requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "format"))
	if name == "" {
		return "", Error("format is required (call action=formats to list options)")
	}
	f, err := resolveFormat(sess, name)
	if err != nil {
		return "", err
	}
	if _, ok := args["data"]; !ok {
		return "", Error("data is required — see the expected shape via action=formats")
	}

	in := ExportInput{
		Title: strings.TrimSpace(StringArg(args, "title")),
		Date:  strings.TrimSpace(StringArg(args, "date")),
		Data:  rawArg(args, "data"),
	}

	// Use the turn's cancelable context (nil-safe: falls back to
	// Background) so cancelling the request also aborts the sandbox run
	// and any cold pip provisioning, instead of leaving them running.
	data, err := RunExportFormat(sess.Context(), f, in)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", Error("export: " + f.Name + " generator produced no bytes")
	}
	if len(data) > maxExportBytes {
		return "", Error(fmt.Sprintf("export: generated file too large (%d bytes, cap %d)", len(data), maxExportBytes))
	}

	fname := deriveFilename(strings.TrimSpace(StringArg(args, "filename")), in.Title, f)
	mime := f.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	sess.AppendFile(FileAttachment{
		Name:     fname,
		MimeType: mime,
		Data:     base64.StdEncoding.EncodeToString(data),
		Size:     len(data),
	})
	return fmt.Sprintf("Generated %q (%s, %s) and attached it to your reply.",
		fname, mime, humanSize(len(data))), nil
}

func handleDefine(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return "", Error("define requires an authenticated session with persistence")
	}
	name := strings.ToLower(strings.TrimSpace(StringArg(args, "name")))
	if !validFormatName.MatchString(name) {
		return "", Error("invalid format name — use lowercase letters, digits, _ or - (max 40 chars)")
	}
	if _, ok := LookupExportFormat(name); ok {
		return "", Error("cannot define " + name + " — it's a reserved built-in format")
	}
	script := StringArg(args, "script")
	if strings.TrimSpace(script) == "" {
		return "", Error("script is required")
	}
	interp := strings.TrimSpace(StringArg(args, "interpreter"))
	if interp == "" {
		interp = "python3"
	}
	if !allowedInterps[interp] {
		return "", Error("interpreter must be python3 or node")
	}
	ext := strings.TrimSpace(StringArg(args, "ext"))
	if ext == "" {
		ext = "." + name
	} else if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}

	f := ExportFormat{
		Name:        name,
		Ext:         ext,
		MIME:        strings.TrimSpace(StringArg(args, "mime")),
		Desc:        strings.TrimSpace(StringArg(args, "desc")),
		InputHint:   strings.TrimSpace(StringArg(args, "input_hint")),
		PyRequires:  stringsArg(args, "py_requires"),
		Interp:      interp,
		Script:      script,
		UserDefined: true,
	}

	// Provision now so pip errors surface at define time, not on first
	// create. Only python formats have pip deps.
	if interp == "python3" && len(f.PyRequires) > 0 {
		if err := EnsurePyDeps(sess.Context(), f.PyRequires...); err != nil {
			return "", err
		}
	}

	sess.DB.Set(userFormatsTable, userFormatKey(sess.Username, name), f)
	dep := ""
	if len(f.PyRequires) > 0 {
		dep = " (provisioned: " + strings.Join(f.PyRequires, ", ") + ")"
	}
	return fmt.Sprintf("Defined export format %q → %s%s. Use it via action=create, format=%q.",
		name, ext, dep, name), nil
}

func handleUndefine(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return "", Error("undefine requires an authenticated session with persistence")
	}
	name := strings.ToLower(strings.TrimSpace(StringArg(args, "name")))
	if _, ok := LookupExportFormat(name); ok {
		return "", Error(name + " is a built-in format and cannot be removed")
	}
	key := userFormatKey(sess.Username, name)
	var existing ExportFormat
	if !sess.DB.Get(userFormatsTable, key, &existing) {
		return "", Error("no defined format named " + name)
	}
	sess.DB.Unset(userFormatsTable, key)
	return fmt.Sprintf("Removed defined format %q.", name), nil
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

// resolveFormat looks up a built-in first, then the caller's per-user
// defined formats.
func resolveFormat(sess *ToolSession, name string) (*ExportFormat, error) {
	if f, ok := LookupExportFormat(name); ok {
		return f, nil
	}
	if f, ok := loadUserFormat(sess, name); ok {
		return f, nil
	}
	return nil, Error("unknown export format " + strconvQuote(name) + " — call action=formats to see what's available")
}

func loadUserFormat(sess *ToolSession, name string) (*ExportFormat, bool) {
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return nil, false
	}
	var f ExportFormat
	if sess.DB.Get(userFormatsTable, userFormatKey(sess.Username, name), &f) {
		return &f, true
	}
	return nil, false
}

func listUserFormats(sess *ToolSession) []ExportFormat {
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return nil
	}
	prefix := sess.Username + ":"
	var out []ExportFormat
	for _, k := range sess.DB.Keys(userFormatsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var f ExportFormat
		if sess.DB.Get(userFormatsTable, k, &f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func userFormatKey(owner, name string) string { return owner + ":" + name }

// deriveFilename picks the delivered filename: an explicit filename
// (extension appended if missing), else the title, else the format name.
func deriveFilename(explicit, title string, f *ExportFormat) string {
	if explicit != "" {
		if !strings.HasSuffix(strings.ToLower(explicit), strings.ToLower(f.Ext)) {
			explicit += f.Ext
		}
		return explicit
	}
	base := sanitizeBase(title)
	if base == "" {
		base = f.Name
	}
	return base + f.Ext
}

var nonFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// sanitizeBase turns a title into a safe filename stem.
func sanitizeBase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = nonFilenameChars.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_.")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// rawArg marshals an arbitrary arg value into a json.RawMessage. Strings
// become JSON strings (markdown payloads), objects/arrays pass through.
func rawArg(args map[string]any, key string) json.RawMessage {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// stringsArg extracts a []string arg, accepting a JSON array of strings
// or a comma-separated string.
func stringsArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	var out []string
	switch t := v.(type) {
	case []string:
		out = t
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		for _, part := range strings.Split(t, ",") {
			out = append(out, part)
		}
	}
	var clean []string
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	return clean
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// humanSize formats bytes for tool-result display.
func humanSize(n int) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
