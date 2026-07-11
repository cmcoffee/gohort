// Export format registry — the domain-agnostic core of document
// generation. A format is data + a generator; the registry maps a name
// ("xlsx", "docx", "pptx", "pdf", or an app/LLM-defined name) to the
// generator that turns a JSON payload into file bytes.
//
// Two generator kinds share one execution path (RunExportFormat):
//
//   - Go built-in (GoGen != nil): pure in-process, zero deps. Used by
//     "pdf", which reuses MarkdownToPDFBytes.
//   - Sandboxed script (Interp + Script): runs under bwrap via
//     RunSandboxedScript, with PyRequires provisioned host-side by
//     EnsurePyDeps first. Used by the openpyxl / python-docx /
//     python-pptx generators, and by any format an app or the LLM
//     defines at runtime.
//
// The generator contract for script formats is deliberately tiny so it
// composes with anything: read the ExportInput JSON on stdin, write the
// file's bytes base64-encoded to stdout on a line carrying
// ExportB64Marker. stderr is free for diagnostics. RunExportFormat never
// needs to know what a .xlsx *is* — it pipes data in and reads bytes out.

package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// ExportB64Marker prefixes the stdout line carrying a script generator's
// base64 output. Kept distinctive so incidental library prints on stdout
// can't be mistaken for the payload.
const ExportB64Marker = "@@GOHORT_EXPORT_B64@@"

// exportRunTimeout caps a single generator run.
const exportRunTimeout = 90 * time.Second

// ExportInput is the JSON payload handed to a generator. Data is the
// format-specific content, opaque to the registry — each format
// documents its expected shape via ExportFormat.InputHint.
type ExportInput struct {
	Title string          `json:"title,omitempty"`
	Date  string          `json:"date,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ExportFormat describes one output format and how to generate it.
type ExportFormat struct {
	Name        string   // registry key, e.g. "xlsx"
	Ext         string   // ".xlsx" (with leading dot)
	MIME        string   // advisory; delivery re-sniffs the bytes
	Desc        string   // one-line, shown to the LLM in the format list
	InputHint   string   // documents the expected `data` shape
	PyRequires  []string // pip specs provisioned before a script run
	Interp      string   // "python3" etc.; "" => Go built-in (GoGen)
	Script      string   // generator body for script formats
	UserDefined bool     // true for app/LLM-registered formats

	// GoGen is the in-process generator for built-in Go formats. When
	// set, Interp/Script/PyRequires are ignored.
	GoGen func(in ExportInput) ([]byte, error) `json:"-"`
}

var (
	exportRegistryMu sync.RWMutex
	exportRegistry   = map[string]*ExportFormat{}
)

// RegisterExportFormat adds (or replaces) a format in the process-global
// registry. Apps call this from init() to grow the catalog; built-ins
// register the same way below. Runtime per-user formats are NOT stored
// here — those live in a per-user DB table and are passed to
// RunExportFormat directly (see the export tool's define/create).
func RegisterExportFormat(f *ExportFormat) {
	if f == nil || f.Name == "" {
		return
	}
	exportRegistryMu.Lock()
	defer exportRegistryMu.Unlock()
	exportRegistry[f.Name] = f
}

// LookupExportFormat returns a registered built-in/app format by name.
func LookupExportFormat(name string) (*ExportFormat, bool) {
	exportRegistryMu.RLock()
	defer exportRegistryMu.RUnlock()
	f, ok := exportRegistry[strings.TrimSpace(name)]
	return f, ok
}

// ListExportFormats returns all registered built-in/app formats.
func ListExportFormats() []*ExportFormat {
	exportRegistryMu.RLock()
	defer exportRegistryMu.RUnlock()
	out := make([]*ExportFormat, 0, len(exportRegistry))
	for _, f := range exportRegistry {
		out = append(out, f)
	}
	return out
}

// RunExportFormat executes a format (built-in OR user-defined — the
// caller supplies the resolved *ExportFormat) against the given input
// and returns the generated file bytes. For script formats it
// provisions PyRequires host-side, runs the generator under the sandbox,
// and decodes the base64 marker line.
func RunExportFormat(ctx context.Context, f *ExportFormat, in ExportInput) ([]byte, error) {
	if f == nil {
		return nil, Error("export: nil format")
	}
	if f.GoGen != nil {
		return f.GoGen(in)
	}
	if strings.TrimSpace(f.Interp) == "" || strings.TrimSpace(f.Script) == "" {
		return nil, Error("export: format " + f.Name + " has no generator")
	}
	if len(f.PyRequires) > 0 {
		if err := EnsurePyDeps(ctx, f.PyRequires...); err != nil {
			return nil, err
		}
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return nil, Error("export: marshal input: " + err.Error())
	}

	rctx, cancel := context.WithTimeout(ctx, exportRunTimeout)
	defer cancel()
	res := RunSandboxedScript(rctx, f.Interp, f.Script, string(stdin))
	if res.TimedOut {
		return nil, Error("export: " + f.Name + " generator timed out after " + exportRunTimeout.String())
	}
	// Scan stdout for the marker line even when Err is set — a generator
	// that produced output before a nonzero exit is rare, but the marker
	// is the source of truth. If no marker, surface stderr.
	for _, line := range strings.Split(res.Stdout, "\n") {
		if idx := strings.Index(line, ExportB64Marker); idx >= 0 {
			b64 := strings.TrimSpace(line[idx+len(ExportB64Marker):])
			data, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				return nil, Error("export: " + f.Name + " produced an undecodable payload: " + derr.Error())
			}
			return data, nil
		}
	}
	stderr := strings.TrimSpace(res.Stderr)
	if res.Err != nil {
		return nil, Error("export: " + f.Name + " generator failed: " + tailLines(stderr, 12))
	}
	return nil, Error("export: " + f.Name + " generator produced no output marker. stderr: " + tailLines(stderr, 8))
}

// markdownFromData extracts a markdown string from an ExportInput.Data
// that is either a bare JSON string or an object with a "markdown" (or
// "content"/"body") field. Empty when none present.
func markdownFromData(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		for _, k := range []string{"markdown", "content", "body", "text"} {
			if v, ok := obj[k]; ok {
				if str, ok := v.(string); ok {
					return str
				}
			}
		}
	}
	return ""
}

// tailLines returns at most the last n lines of s, for compact error surfacing.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return "...\n" + strings.Join(lines[len(lines)-n:], "\n")
}

func init() {
	// pdf — Go built-in, zero deps, reuses the markdown→PDF renderer.
	RegisterExportFormat(&ExportFormat{
		Name:      "pdf",
		Ext:       ".pdf",
		MIME:      "application/pdf",
		Desc:      "PDF document from markdown (headings, lists, blockquotes, tables of sources).",
		InputHint: `data: a markdown string, or {"markdown": "..."}. title/date render as a cover header.`,
		GoGen: func(in ExportInput) ([]byte, error) {
			return MarkdownToPDFBytes(in.Title, in.Date, markdownFromData(in.Data))
		},
	})

	// xlsx — openpyxl.
	RegisterExportFormat(&ExportFormat{
		Name:       "xlsx",
		Ext:        ".xlsx",
		MIME:       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Desc:       "Excel spreadsheet (.xlsx) with one or more sheets of rows.",
		InputHint:  `data: {"sheets":[{"name":"Sheet1","rows":[["Name","Qty"],["alpha",1]]}]} — or a single sheet as {"sheet_name":"...","rows":[[...]]}. First row is typically the header.`,
		PyRequires: []string{"openpyxl"},
		Interp:     "python3",
		Script:     xlsxGeneratorPy,
	})

	// docx — python-docx.
	RegisterExportFormat(&ExportFormat{
		Name:       "docx",
		Ext:        ".docx",
		MIME:       "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Desc:       "Word document (.docx) rendered from markdown (headings, bullets, paragraphs).",
		InputHint:  `data: a markdown string, or {"markdown":"..."}. title renders as the document heading. Supported: #/##/### headings, "- " bullets, paragraphs.`,
		PyRequires: []string{"python-docx"},
		Interp:     "python3",
		Script:     docxGeneratorPy,
	})

	// pptx — python-pptx.
	RegisterExportFormat(&ExportFormat{
		Name:       "pptx",
		Ext:        ".pptx",
		MIME:       "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		Desc:       "PowerPoint deck (.pptx) from a list of slides with titles + bullets.",
		InputHint:  `data: {"slides":[{"title":"Slide 1","bullets":["point a","point b"]}]}. Optional top-level title adds an opening title slide.`,
		PyRequires: []string{"python-pptx"},
		Interp:     "python3",
		Script:     pptxGeneratorPy,
	})
}

// --- built-in python generators (stdin JSON → base64 marker on stdout) ---

const xlsxGeneratorPy = `
import sys, json, base64, io
import openpyxl
MARKER = "@@GOHORT_EXPORT_B64@@"
payload = json.load(sys.stdin)
data = payload.get("data") or {}
if isinstance(data, str):
    s = data.strip()
    # Tuple membership, NOT substring: an empty/whitespace string has ""
    # as its first char, and "" is a substring of every string, so the
    # old test routed "" into json.loads("") and raised. s[:1] in the
    # tuple is False for "", so it falls through to a one-cell sheet.
    data = json.loads(s) if s[:1] in ("{", "[") else {"rows": [[data]]}
if isinstance(data, list):
    data = {"rows": data}
sheets = data.get("sheets")
if not sheets:
    sheets = [{"name": data.get("sheet_name", "Sheet1"), "rows": data.get("rows", [])}]
wb = openpyxl.Workbook()
default = wb.active
for i, sh in enumerate(sheets):
    name = str(sh.get("name", "Sheet%d" % (i + 1)))[:31]
    ws = default if i == 0 else wb.create_sheet()
    ws.title = name
    for row in sh.get("rows", []):
        ws.append(row if isinstance(row, list) else [row])
buf = io.BytesIO()
wb.save(buf)
print(MARKER + base64.b64encode(buf.getvalue()).decode())
`

const docxGeneratorPy = `
import sys, json, base64, io
from docx import Document
MARKER = "@@GOHORT_EXPORT_B64@@"
payload = json.load(sys.stdin)
title = payload.get("title", "")
data = payload.get("data")
md = data if isinstance(data, str) else (data or {}).get("markdown", "") if isinstance(data, dict) else ""
doc = Document()
if title:
    doc.add_heading(title, level=0)
for raw in md.split("\n"):
    s = raw.strip()
    if not s:
        continue
    if s.startswith("### "):
        doc.add_heading(s[4:], level=3)
    elif s.startswith("## "):
        doc.add_heading(s[3:], level=2)
    elif s.startswith("# "):
        doc.add_heading(s[2:], level=1)
    elif s[:2] in ("- ", "* "):
        doc.add_paragraph(s[2:], style="List Bullet")
    else:
        doc.add_paragraph(s)
buf = io.BytesIO()
doc.save(buf)
print(MARKER + base64.b64encode(buf.getvalue()).decode())
`

const pptxGeneratorPy = `
import sys, json, base64, io
from pptx import Presentation
MARKER = "@@GOHORT_EXPORT_B64@@"
payload = json.load(sys.stdin)
data = payload.get("data") or {}
if isinstance(data, str):
    s = data.strip()
    # Only parse a string that looks like JSON; treat any other string as
    # prose -> one slide whose bullets are its non-empty lines, instead of
    # letting json.loads raise an opaque "generator failed".
    if s[:1] in ("{", "["):
        data = json.loads(s)
    else:
        data = {"slides": [{"title": payload.get("title", ""),
                            "bullets": [ln.strip() for ln in data.split("\n") if ln.strip()]}]}
if isinstance(data, list):
    data = {"slides": data}
slides = data.get("slides", [])
prs = Presentation()
deck_title = payload.get("title", "")
if deck_title:
    s = prs.slides.add_slide(prs.slide_layouts[0])
    s.shapes.title.text = deck_title
for sl in slides:
    slide = prs.slides.add_slide(prs.slide_layouts[1])
    slide.shapes.title.text = str(sl.get("title", ""))
    tf = slide.placeholders[1].text_frame
    bullets = sl.get("bullets", [])
    for i, b in enumerate(bullets):
        p = tf.paragraphs[0] if i == 0 else tf.add_paragraph()
        p.text = str(b)
buf = io.BytesIO()
prs.save(buf)
print(MARKER + base64.b64encode(buf.getvalue()).decode())
`
