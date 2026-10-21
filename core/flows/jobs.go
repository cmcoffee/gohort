package flows

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/media"

	// TIFF and BMP, registered with image so media.DecodeImage reads them:
	// the native flow for those formats is Go's own.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// The groups the page lists jobs under, in its order.
const (
	GroupReading = "Reading what arrives"
	GroupWriting = "Writing documents"
	GroupMedia   = "Media and the web"
	GroupCode    = "Running code"
)

// Job is one thing gohort needs done by an outside tool, as a contract.
type Job struct {
	ID      string // e.g. "read.heic", "export.docx"
	Group   string
	Title   string // what it enables, for the page
	UsedFor string
	// Takes are the formats of the file it reads, {in}: the formats an input
	// takes, or text/markdown or text/html for an export (the document). A
	// job that takes no file (a download takes a link, a Param) leaves it
	// empty.
	Takes []string
	// What it gives: an input job's Kind (Becomes), an export's format
	// (Gives, an extension), or several files (Files, into {outdir}), each
	// checked as Becomes.
	Becomes Kind
	Gives   string
	Files   bool
	// Then is a hand-off's: the format its result is, handed on to whatever
	// reads that format here (an admin's converter, Adapted.Then).
	Then string
	// Params are values a call passes, as {name} placeholders: {count},
	// {url}.
	Params  []Param
	Network bool
	// Builtin is a job gohort does itself: Go (run set, driving the tools
	// in Uses through a sandboxed Box), or the app that asks for it (an
	// export the app renders, run nil), with no command flow.
	Builtin bool
	run     func(ctx context.Context, b *Box, in Input) ([]byte, [][]byte, error)
	// Uses are the programs a built-in job's run drives (ffmpeg).
	Uses []string
	// ready, when set, is whether a built-in job can run here beyond its
	// programs (a service it calls is set up), and install what to do when
	// it cannot.
	ready   func() bool
	install string
	// Native are the ways it is done with tools, in preference: the first
	// whose programs are all installed is used.
	Native  []Flow
	timeout time.Duration
	// Hint says, for an export, what its document has to hold: the shape
	// of the data a structured format (a spreadsheet, a deck) is built from.
	Hint string
}

// AppRendered reports whether j is an export only the app that asks for it
// can write (Scribe's HTML and Markdown): nothing runs for it here.
func (j Job) AppRendered() bool { return j.Builtin && j.run == nil }

// Flow is a way to do a job: its steps, in order, each a command line, and
// where its programs come from. A native flow may also bring Files (a
// generator script, written into the run's folder before its steps run) and
// Py, the Python packages its script imports, provisioned first.
type Flow struct {
	Steps   []string
	Install string
	Files   map[string]string
	Py      []string
}

// Param is a value a job is called with. Default is what a test of the
// job passes. Check, when set, refuses a value before any flow runs, native
// or adapted (a link that is not a public web address).
type Param struct {
	Name, Help, Default string
	Check               func(v string) error
}

// checkParams refuses a call missing one of j's values, or with one its
// check refuses.
func checkParams(j Job, params map[string]string) error {
	for _, p := range j.Params {
		v := strings.TrimSpace(params[p.Name])
		if v == "" {
			return fmt.Errorf("this job needs {%s}: %s", p.Name, p.Help)
		}
		if p.Check != nil {
			if err := p.Check(v); err != nil {
				return fmt.Errorf("{%s} refused: %v", p.Name, err)
			}
		}
	}
	return nil
}

// IsRead reports whether j reads a file into something the model uses.
func (j Job) IsRead() bool { return j.Becomes != "" && !j.Files && j.Gives == "" }

// HandsOff reports whether j's result is a format another reader takes,
// rather than something the model uses.
func (j Job) HandsOff() bool { return j.Then != "" }

// IsExport reports whether j writes a document as a format.
func (j Job) IsExport() bool { return j.Gives != "" }

// TakesFile reports whether j reads a file ({in}).
func (j Job) TakesFile() bool { return len(j.Takes) > 0 }

// jobs are the jobs gohort has.
var jobs = []Job{
	{
		ID: "read.heic", Group: GroupReading, Title: "iPhone photos (HEIC), AVIF images",
		UsedFor: "photos you attach, before a model sees them",
		Takes:   []string{"image/heic", "image/avif"}, Becomes: KindImage,
		Native: []Flow{
			{Steps: []string{"heif-convert {in} {out.jpg}"}, Install: heifInstall()},
			{Steps: []string{"magick {in} {out.png}"}, Install: "ImageMagick 7 built with libheif"},
		},
	},
	{
		ID: "read.image", Group: GroupReading, Title: "TIFF and BMP images",
		UsedFor: "images you attach, before a model sees them",
		Takes:   []string{"image/tiff", "image/bmp"}, Becomes: KindImage,
		Builtin: true, run: builtinPNG,
	},
	{
		ID: "read.pdf", Group: GroupReading, Title: "PDF attachments",
		UsedFor: "PDF attachments and documents added to collections",
		Takes:   []string{"application/pdf"}, Becomes: KindText,
		Native: []Flow{{Steps: []string{"pdftotext -layout {in} {out}"}, Install: pick("poppler (brew install poppler)", "poppler-utils")}},
	},
	{
		ID: "read.word", Group: GroupReading, Title: "Word, OpenDocument and RTF attachments",
		UsedFor: "documents you attach or add to collections",
		Takes:   []string{MIMEDocx, MIMEOdt, "application/rtf"}, Becomes: KindText,
		Native: []Flow{{Steps: []string{"pandoc -t plain {in} -o {out}"}, Install: "pandoc"}},
	},
	{
		// antiword first where it exists: "-w 0" gives unwrapped paragraphs,
		// which embed and search better than column-wrapped text. textutil
		// ships with macOS, where Homebrew no longer has antiword: the
		// capability was never missing there, only the package. catdoc is
		// common on Linux where antiword is absent.
		ID: "read.doc", Group: GroupReading, Title: "Legacy Word (.doc) attachments",
		UsedFor: "old .doc files, which pandoc cannot read",
		Takes:   []string{"application/msword"}, Becomes: KindText,
		Native: []Flow{
			{Steps: []string{"antiword -w 0 {in}"}, Install: "antiword or catdoc"},
			{Steps: []string{"textutil -convert txt -stdout {in}"}, Install: "ships with macOS at /usr/bin/textutil"},
			{Steps: []string{"catdoc {in}"}, Install: "catdoc"},
		},
	},
	{
		ID: "export.pdf", Group: GroupWriting, Title: "PDF", UsedFor: "exporting a guide, and documents an agent makes",
		Takes: []string{"text/markdown"}, Gives: "pdf", Builtin: true, run: pdfNative,
		Hint: "a Markdown document; its title and date make a cover header",
	},
	{ID: "export.html", Group: GroupWriting, Title: "HTML", UsedFor: "exporting and previewing a guide", Takes: []string{"text/html"}, Gives: "html", Builtin: true},
	{ID: "export.md", Group: GroupWriting, Title: "Markdown", UsedFor: "exporting a guide", Takes: []string{"text/markdown"}, Gives: "md", Builtin: true},
	{
		ID: "export.docx", Group: GroupWriting, Title: "Word (.docx)", UsedFor: "exporting a guide as Word, and Word files an agent makes",
		Takes: []string{"text/markdown"}, Gives: "docx",
		Hint: "a Markdown document (headings, lists, tables, pictures)",
		Native: []Flow{
			{Steps: []string{"pandoc -f markdown {in} -o {out}"}, Install: "pandoc"},
			// Where pandoc is not installed: headings, bullets and paragraphs.
			{Steps: []string{"python3 gen.py {in} {out}"}, Files: map[string]string{"gen.py": docxGeneratorPy},
				Py: []string{"python-docx"}, Install: "python3 (python-docx is provisioned for it)"},
		},
	},
	{
		ID: "export.odt", Group: GroupWriting, Title: "OpenDocument (.odt)", UsedFor: "exporting a guide as OpenDocument",
		Takes: []string{"text/markdown"}, Gives: "odt",
		Hint:   "a Markdown document",
		Native: []Flow{{Steps: []string{"pandoc -f markdown {in} -o {out}"}, Install: "pandoc"}},
	},
	{
		ID: "export.xlsx", Group: GroupWriting, Title: "Excel (.xlsx)", UsedFor: "spreadsheets an agent makes",
		Takes: []string{"application/json"}, Gives: "xlsx",
		Hint: `data: {"sheets":[{"name":"Sheet1","rows":[["Name","Qty"],["alpha",1]]}]}, or one sheet as {"sheet_name":"...","rows":[[...]]}; the first row is usually the header`,
		Native: []Flow{{Steps: []string{"python3 gen.py {in} {out}"}, Files: map[string]string{"gen.py": xlsxGeneratorPy},
			Py: []string{"openpyxl"}, Install: "python3 (openpyxl is provisioned for it)"}},
	},
	{
		ID: "export.pptx", Group: GroupWriting, Title: "PowerPoint (.pptx)", UsedFor: "slide decks an agent makes",
		Takes: []string{"application/json"}, Gives: "pptx",
		Hint: `data: {"slides":[{"title":"Slide 1","bullets":["point a","point b"]}]}; a title adds an opening title slide`,
		Native: []Flow{{Steps: []string{"python3 gen.py {in} {out}"}, Files: map[string]string{"gen.py": pptxGeneratorPy},
			Py: []string{"python-pptx"}, Install: "python3 (python-pptx is provisioned for it)"}},
	},
}

// ExportPyPackages are the Python packages the export jobs provision: the
// ones any format an agent defines may use too (tools/export).
func ExportPyPackages() []string {
	var out []string
	for _, j := range jobs {
		for _, f := range j.Native {
			for _, p := range f.Py {
				if !contains(out, p) {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// pdfNative is Go's own Markdown renderer.
func pdfNative(_ context.Context, _ *Box, in Input) ([]byte, [][]byte, error) {
	if in.Doc == nil || len(in.Doc.Markdown) == 0 {
		return nil, nil, fmt.Errorf("this document has no Markdown to write as PDF")
	}
	out, err := media.MarkdownToPDFBytes(in.Doc.Title, in.Doc.Date, string(in.Doc.Markdown))
	return out, nil, err
}

// Jobs are gohort's jobs, in the page's order.
func Jobs() []Job { return append([]Job(nil), jobs...) }

// JobByID is the job with id.
func JobByID(id string) (Job, bool) {
	for _, j := range jobs {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// RegisterJob adds a job: one a later part of gohort defines, or a test.
func RegisterJob(j Job) { jobs = append(jobs, j) }

// Programs are the programs a flow runs, in order.
func (f Flow) Programs() []string {
	var out []string
	for _, s := range f.Steps {
		if p := Program(s); p != "" && !contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// Ready reports whether every program f runs is installed.
func (f Flow) Ready() bool {
	for _, p := range f.Programs() {
		if !Installed(p) {
			return false
		}
	}
	return true
}

// Ready is the native flow j would run here: the first whose programs are
// installed. ok is true for a built-in job too, with no flow.
func (j Job) Ready() (Flow, bool) {
	if j.Builtin {
		if j.ready != nil && !j.ready() {
			return Flow{}, false
		}
		for _, p := range j.Uses {
			if !Installed(p) {
				return Flow{}, false
			}
		}
		return Flow{}, true
	}
	for _, f := range j.Native {
		if f.Ready() {
			return f, true
		}
	}
	return Flow{}, false
}

// By is what does j here, for the page: "built in", the programs of the flow
// that is installed, or those of the ones that would be.
func (j Job) By() string {
	if j.Builtin {
		if len(j.Uses) > 0 {
			return strings.Join(j.Uses, " and ")
		}
		if j.ready != nil {
			return j.install
		}
		return "built in"
	}
	if f, ok := j.Ready(); ok {
		return strings.Join(f.Programs(), " then ")
	}
	var names []string
	for _, f := range j.Native {
		names = append(names, strings.Join(f.Programs(), " then "))
	}
	return strings.Join(names, " or ")
}

// Programs are every program j's native flows run, for the page to look up
// their versions.
func (j Job) Programs() []string {
	out := append([]string(nil), j.Uses...)
	for _, f := range j.Native {
		for _, p := range f.Programs() {
			if !contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// InstallHint is what to install for j, on this platform.
func (j Job) InstallHint() string {
	if j.Builtin {
		if j.ready != nil && !j.ready() {
			return j.install
		}
		if len(j.Uses) > 0 {
			return strings.Join(j.Uses, " and ")
		}
	}
	var hints []string
	for _, f := range j.Native {
		if f.Install == "" || contains(hints, f.Install) {
			continue
		}
		if runtime.GOOS != "darwin" && strings.Contains(f.Install, "macOS") {
			continue
		}
		if runtime.GOOS == "darwin" && contains(f.Programs(), "antiword") {
			continue // Homebrew dropped it; textutil is there instead
		}
		hints = append(hints, f.Install)
	}
	return strings.Join(hints, ", or ")
}

// Placeholders are what a flow for j may write, for the page and Builder:
// {in} for the file it reads (and, in a later step, the step before's
// result), {out} for its result or {outdir} for several, and j's values.
func (j Job) Placeholders() []string {
	var out []string
	if j.TakesFile() {
		out = append(out, "{in}")
	}
	if j.Files {
		out = append(out, "{outdir}")
	} else {
		out = append(out, "{out}")
	}
	for _, p := range j.Params {
		out = append(out, "{"+p.Name+"}")
	}
	return out
}

// target is what j's result has to be.
func (j Job) target() target {
	if j.IsExport() {
		return outTarget(j.Gives)
	}
	if j.HandsOff() {
		return formatTarget(j.Then)
	}
	return j.Becomes
}

// defaultTimeout is how long j's native flow may take: a document's
// extraction timeout for text (core's tunable), a minute otherwise.
func (j Job) defaultTimeout() time.Duration {
	if j.timeout > 0 {
		return j.timeout
	}
	if j.Becomes == KindText && !j.Files {
		return media.DocumentExtractTimeout()
	}
	return time.Minute
}

// Program is the program a command runs, as written.
func Program(command string) string {
	words, _ := splitArgs(command)
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// Installed reports whether program is on this machine.
func Installed(program string) bool {
	if program == "" {
		return false
	}
	_, err := exec.LookPath(program)
	return err == nil
}

// Document is what gohort hands an export: the document as Markdown and as
// HTML, its pictures inside both, or for a structured format (a
// spreadsheet, a deck) its data, as JSON.
type Document struct {
	Title    string
	Date     string
	Markdown []byte
	HTML     []byte
	Data     json.RawMessage
}

// SampleDocument is what an export is tested on: a heading, emphasis, a
// list, a table and a code block, the things a format most often gets wrong.
func SampleDocument() Document {
	md := "# Sample document\n\nA paragraph with **bold** and *italic* text.\n\n- First point\n- Second point\n\n| Name | Value |\n|---|---|\n| one | 1 |\n| two | 2 |\n\n```\nconfig set mode=fast\n```\n"
	html := "<!doctype html><html><head><meta charset=\"utf-8\"><title>Sample document</title></head><body><h1>Sample document</h1><p>A paragraph with <b>bold</b> and <i>italic</i> text.</p><ul><li>First point</li><li>Second point</li></ul><table><tr><th>Name</th><th>Value</th></tr><tr><td>one</td><td>1</td></tr><tr><td>two</td><td>2</td></tr></table><pre>config set mode=fast</pre></body></html>"
	data := `{"sheets":[{"name":"Sample","rows":[["Name","Value"],["one",1],["two",2]]}],"slides":[{"title":"First point","bullets":["A paragraph with bold text","A list"]},{"title":"Second point","bullets":["A table","A code block"]}]}`
	return Document{Title: "Sample document", Markdown: []byte(md), HTML: []byte(html), Data: json.RawMessage(data)}
}

// DetectOutput names what a result looks like, for a test's message.
func DetectOutput(out []byte) string {
	if t := zipDocType(out); t != "" {
		return FormatLabel(t)
	}
	return http.DetectContentType(out)
}

// builtinPNG re-encodes an image Go decodes (TIFF, BMP) as PNG.
func builtinPNG(_ context.Context, _ *Box, in Input) ([]byte, [][]byte, error) {
	img, _, err := media.DecodeImage(in.Data)
	if err != nil {
		return nil, nil, err
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), nil, nil
}

// pick is the install hint for this platform: macOS's, or Linux's.
func pick(mac, linux string) string {
	if runtime.GOOS == "darwin" {
		return mac
	}
	return linux
}

func heifInstall() string {
	return pick("libheif (brew install libheif)", "libheif: libheif-examples on Debian and Ubuntu, libheif-tools on Fedora and RHEL (EPEL)")
}
