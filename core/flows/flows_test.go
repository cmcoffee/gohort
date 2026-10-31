package flows

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/deps"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/go-pdf/fpdf"
	"golang.org/x/image/bmp"
)

// TestMain gives runs a folder outside /tmp, which the sandbox replaces with
// an empty one of its own.
func TestMain(m *testing.M) {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.Getenv("HOME")
	}
	// Laid out as gohort's data folder is: <data>/workspaces/_flows, so the
	// guard that keeps a flow out of gohort's data is exercised for real.
	dir, err := os.MkdirTemp(cache, "gohort-flows-test-")
	if err == nil {
		testRoot = dir
		SetWorkBase(filepath.Join(dir, "data", "workspaces", "_flows"))
	}
	code := m.Run()
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

// testRoot holds the test's data folder and anything else a test needs
// outside /tmp.
var testRoot string

// pngBytes is a small real PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// wrapped is a PNG behind 8 bytes nothing reads: a format gohort does not
// know, which the dd command below "converts" by dropping them.
func wrapped(t *testing.T) []byte { return append([]byte("GOHORTX!"), pngBytes(t)...) }

func ddAdapted() Adapted {
	return Adapted{Takes: []string{Any}, Becomes: KindImage, Command: "dd if={in} of={out} bs=8 skip=1", TimeoutSecs: 20}
}

func needSandbox(t *testing.T) {
	t.Helper()
	if _, err := RunAdapted(context.Background(), "probe", ddAdapted(), Input{Data: wrapped(t), From: "x/y"}); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox to run commands in here: %v", err)
	}
}

func TestAnAdaptedConversionIsChecked(t *testing.T) {
	if err := ddAdapted().Validate(); err != nil {
		t.Fatalf("a good one should pass: %v", err)
	}
	for name, mut := range map[string]func(*Adapted){
		"becomes nothing":    func(a *Adapted) { a.Becomes = "movie" },
		"takes nothing":      func(a *Adapted) { a.Takes = nil },
		"takes a non-format": func(a *Adapted) { a.Takes = []string{"heic"} },
		"no {in}":            func(a *Adapted) { a.Command = "dd of={out}" },
		"no command":         func(a *Adapted) { a.Command = "" },
		"other placeholder":  func(a *Adapted) { a.Command = "dd if={in} of={out} {secret}" },
		"two result names":   func(a *Adapted) { a.Command = "x {in} {out.jpg} {out.png}" },
		"starts with a file": func(a *Adapted) { a.Command = "{in} {out}" },
		"open quote":         func(a *Adapted) { a.Command = "dd 'if={in} of={out}" },
		"too long":           func(a *Adapted) { a.TimeoutSecs = 3600 },
	} {
		a := ddAdapted()
		mut(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	// What it prints may be the result: no {out} is fine.
	a := ddAdapted()
	a.Command = "antiword -w 0 {in}"
	if err := a.Validate(); err != nil {
		t.Errorf("a command whose output is the result should pass: %v", err)
	}
}

func TestParseFormatReadsWhatAPersonWrites(t *testing.T) {
	for in, want := range map[string]string{"heic": "image/heic", ".docx": MIMEDocx, "PDF": "application/pdf", "*": Any, "image/avif": "image/avif"} {
		if got := ParseFormat(in); got != want {
			t.Errorf("%q: want %s, got %s", in, want, got)
		}
	}
}

func TestAConversionRunsAndItsResultIsChecked(t *testing.T) {
	needSandbox(t)
	res, err := RunAdapted(context.Background(), "dd", ddAdapted(), Input{Data: wrapped(t), From: "x/y"})
	if err != nil {
		t.Fatalf("run: %v (log %q)", err, res.Log)
	}
	if !bytes.Equal(res.Out, pngBytes(t)) || res.By != "dd" {
		t.Errorf("want the PNG back from dd, got %d bytes by %q", len(res.Out), res.By)
	}
	// A command that writes something other than the kind it promised
	// produces nothing gohort uses.
	liar := ddAdapted()
	liar.Command = "cp {in} {out}"
	if _, err := RunAdapted(context.Background(), "cp", liar, Input{Data: wrapped(t), From: "x/y"}); err == nil || !strings.Contains(err.Error(), "not used") {
		t.Errorf("an unchecked result should be refused, got %v", err)
	}
}

// With no {out}, what the command prints is the result; {out.jpg} names
// the result file for a program that picks its format by the name.
func TestTheResultIsWhatItPrintsOrTheFileItNames(t *testing.T) {
	needSandbox(t)
	printed := Adapted{Takes: []string{"text/plain"}, Becomes: KindText, Command: "cat {in}"}
	res, err := RunAdapted(context.Background(), "cat", printed, Input{Data: []byte("hello there"), From: "text/plain"})
	if err != nil || string(res.Out) != "hello there" {
		t.Fatalf("want what cat printed, got %v %q", err, res.Out)
	}
	line, out, _ := commandLine("heif-convert {in} {out.jpg}", "in.heic", 0, true, KindImage, nil)
	if out != "out.jpg" || line != `exec 'heif-convert' 'in.heic' 'out.jpg'` {
		t.Errorf("want the result named out.jpg: %s / %s", line, out)
	}
	line, out, _ = commandLine("antiword -w 0 {in}", "in.doc", 0, true, KindText, nil)
	if out != "out.txt" || line != `exec 'antiword' '-w' '0' 'in.doc' > 'out.txt'` {
		t.Errorf("want what it prints sent to out.txt: %s / %s", line, out)
	}
}

// The line is never read by a shell: a ";" is a character in an argument.
func TestACommandLineIsNotAShell(t *testing.T) {
	needSandbox(t)
	marker := filepath.Join(t.TempDir(), "ran")
	a := ddAdapted()
	a.Command = "dd if={in} of={out} bs=8 skip=1 ; touch " + marker
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	RunAdapted(context.Background(), "x", a, Input{Data: wrapped(t), From: "x/y"})
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the part after ; ran as a second command")
	}
	line, _, _ := commandLine("prog '$(id)' {in} {out}", "in", 0, true, KindImage, nil)
	if line != `exec 'prog' '$(id)' 'in' 'out.png'` {
		t.Errorf("every word should be quoted: %s", line)
	}
}

func TestAConversionIsStoppedAtItsTimeLimit(t *testing.T) {
	needSandbox(t)
	a := ddAdapted()
	a.Command = "sh -c 'sleep 30' {in} {out}"
	a.TimeoutSecs = 1
	start := time.Now()
	if _, err := RunAdapted(context.Background(), "slow", a, Input{Data: wrapped(t), From: "x/y"}); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("want a time-limit error, got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("it ran %s past a 1s limit", time.Since(start))
	}
}

// A conversion sees no network: inside the sandbox its only interface is
// loopback, even on a deployment that leaves shell networking open (the
// production default).
func TestAConversionHasNoNetwork(t *testing.T) {
	needSandbox(t)
	was := sandbox.ShellNetworkClosedByDefault
	sandbox.ShellNetworkClosedByDefault = func() bool { return false }
	defer func() { sandbox.ShellNetworkClosedByDefault = was }()
	a := Adapted{Takes: []string{"text/plain"}, Becomes: KindText, Command: "cat /proc/net/dev {in}"}
	res, err := RunAdapted(context.Background(), "net", a, Input{Data: []byte("x"), From: "text/plain"})
	if err != nil {
		t.Fatalf("run: %v (%s)", err, res.Log)
	}
	var ifaces []string
	for _, line := range strings.Split(string(res.Out), "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && !strings.Contains(name, "|") && name != "x" {
			ifaces = append(ifaces, name)
		}
	}
	if len(ifaces) == 0 {
		t.Fatalf("read no interfaces from %q", res.Out)
	}
	for _, i := range ifaces {
		if i != "lo" {
			t.Errorf("the conversion can see interface %q: it has a network", i)
		}
	}
}

// Convert: an adapted conversion the admin preferred comes first, then the
// native one, then the other adapted ones; nothing at all is ErrNoConversion.
func TestConvertTriesPreferredThenNativeThenAdapted(t *testing.T) {
	needSandbox(t)
	if _, err := Convert(context.Background(), wrapped(t), "", KindImage); !errors.Is(err, ErrNoConversion) {
		t.Fatalf("with nothing for it, want ErrNoConversion, got %v", err)
	}
	if err := SetAdapted("dd", ddAdapted()); err != nil {
		t.Fatal(err)
	}
	defer RemoveAdapted("dd")
	res, err := Convert(context.Background(), wrapped(t), "", KindImage)
	if err != nil || res.By != "dd" {
		t.Fatalf("want the dd conversion, got %v %+v", err, res)
	}

	// A BMP has a native (built-in) conversion: it wins over a catch-all.
	var b bytes.Buffer
	bmp.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	if res, err := Convert(context.Background(), b.Bytes(), "", KindImage); err != nil || res.By != "built in" {
		t.Fatalf("a BMP should be converted by Go itself: %v %+v", err, res)
	}
	// An adapted one the admin preferred goes before the native tool; not
	// preferred, after it.
	if !Installed("pdftotext") {
		t.Skip("pdftotext is not installed here")
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	var pb bytes.Buffer
	pdf.Output(&pb)
	mine := Adapted{Takes: []string{"application/pdf"}, Becomes: KindText, Command: "sh -c 'echo mine' sh {in}"}
	SetAdapted("mine", mine)
	defer RemoveAdapted("mine")
	if res, err := Convert(context.Background(), pb.Bytes(), "application/pdf", KindText); err != nil || res.By != "pdftotext" {
		t.Errorf("not preferred, the native tool goes first: %v %+v", err, res.By)
	}
	mine.Prefer = true
	SetAdapted("mine", mine)
	if res, err := Convert(context.Background(), pb.Bytes(), "application/pdf", KindText); err != nil || res.By != "mine" || strings.TrimSpace(string(res.Out)) != "mine" {
		t.Errorf("preferred, the admin's goes first: %v %+v", err, res.By)
	}
	mine.Command = "sh -c 'exit 3' sh {in}"
	SetAdapted("mine", mine)
	if res, err := Convert(context.Background(), pb.Bytes(), "application/pdf", KindText); err != nil || res.By != "pdftotext" {
		t.Errorf("a preferred one that fails falls back to the native tool: %v %+v", err, res.By)
	}
}

// The built-in conversion: TIFF and BMP become a PNG, in Go.
func TestBMPAndTIFFAreConvertedInGo(t *testing.T) {
	var b bytes.Buffer
	bmp.Encode(&b, image.NewRGBA(image.Rect(0, 0, 5, 2)))
	j, _ := JobByID("read.image")
	res, err := RunNative(context.Background(), j, Input{Data: b.Bytes(), From: "image/bmp"})
	if err != nil || !bytes.HasPrefix(res.Out, []byte("\x89PNG")) {
		t.Fatalf("want a PNG, got %v", err)
	}
}

// The real tools, sandboxed, where this machine has them: a PDF through
// pdftotext and a Word file through pandoc, the way an attachment is read.
func TestDocumentsAreReadByTheirToolsInTheSandbox(t *testing.T) {
	needSandbox(t)
	if Installed("pdftotext") {
		pdf := fpdf.New("P", "mm", "A4", "")
		pdf.AddPage()
		pdf.SetFont("Helvetica", "", 14)
		pdf.Cell(40, 10, "The quarterly numbers are in")
		var b bytes.Buffer
		if err := pdf.Output(&b); err != nil {
			t.Fatal(err)
		}
		res, err := Convert(context.Background(), b.Bytes(), "application/pdf", KindText)
		if err != nil || !strings.Contains(string(res.Out), "quarterly numbers") || res.By != "pdftotext" {
			t.Errorf("want the PDF's text by pdftotext, got %v %+v", err, res)
		}
	}
	if Installed("pandoc") {
		res, err := Convert(context.Background(), minimalDocx(t, "Hello from Word"), "", KindText)
		if err != nil || !strings.Contains(string(res.Out), "Hello from Word") || res.By != "pandoc" {
			t.Errorf("want the docx's text by pandoc, got %v %+v", err, res)
		}
	}
}

// minimalDocx is the smallest .docx pandoc reads.
func minimalDocx(t *testing.T, text string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return b.Bytes()
}

func TestSniffNamesWhatGoDoesNot(t *testing.T) {
	ftyp := func(brand string) []byte {
		return append([]byte{0, 0, 0, 24}, []byte("ftyp"+brand+"\x00\x00\x00\x00mif1")...)
	}
	for want, data := range map[string][]byte{
		"image/heic":         ftyp("heic"),
		"image/avif":         ftyp("avif"),
		"image/tiff":         []byte("II*\x00rest"),
		"application/rtf":    []byte(`{\rtf1 hello}`),
		"application/msword": []byte("\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1rest"),
		MIMEDocx:             minimalDocx(t, "x"),
		"image/png":          pngBytes(t),
	} {
		if got := Sniff(data); got != want {
			t.Errorf("want %s, got %s", want, got)
		}
	}
}

// A kind's check: an image must be one the model reads and open; text must
// not be binary, and invalid UTF-8 is mended rather than refused.
func TestAKindChecksItsResult(t *testing.T) {
	if _, err := KindImage.accept(pngBytes(t)); err != nil {
		t.Errorf("a PNG is an image the model reads: %v", err)
	}
	var b bytes.Buffer
	bmp.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	if _, err := KindImage.accept(b.Bytes()); err == nil {
		t.Error("a BMP is not an image the model reads")
	}
	if _, err := KindText.accept([]byte("a\x00b")); err == nil {
		t.Error("text with NUL bytes is binary")
	}
	if out, err := KindText.accept([]byte("caf\xe9")); err != nil || !strings.Contains(string(out), "caf") {
		t.Errorf("latin-1 text should be mended, got %v %q", err, out)
	}
}

// Every platform has something that reads a legacy .doc, antiword is
// preferred where it exists, and the install hint is one this platform can
// follow (Homebrew no longer ships antiword; macOS has textutil).
func TestLegacyDocHasAConverterOnEveryPlatform(t *testing.T) {
	n, ok := JobByID("read.doc")
	if !ok {
		t.Fatal("no job for legacy .doc")
	}
	var names []string
	for _, f := range n.Native {
		names = append(names, f.Programs()[0])
		if !strings.Contains(f.Steps[0], "{in}") {
			t.Errorf("%s is not handed the file", f.Programs()[0])
		}
	}
	if names[0] != "antiword" || !strings.Contains(strings.Join(names, ","), "textutil") {
		t.Errorf("want antiword first and textutil listed: %v", names)
	}
	hint := n.InstallHint()
	if runtime.GOOS == "darwin" && strings.Contains(hint, "antiword") {
		t.Errorf("macOS is told about antiword: %q", hint)
	}
	if runtime.GOOS != "darwin" && strings.Contains(hint, "macOS") {
		t.Errorf("Linux is told about macOS: %q", hint)
	}
}

// A probe asks a program what it is, in the sandbox, and only that.
func TestAProbeOnlyAsksWhatAProgramIs(t *testing.T) {
	needSandbox(t)
	out, err := Probe(context.Background(), "dd", "--help")
	if err != nil || !strings.Contains(out, "Found at") || !strings.Contains(strings.ToLower(out), "usage") {
		t.Fatalf("want dd's usage, got %v %q", err, out)
	}
	for _, bad := range [][2]string{{"dd", "if=/etc/passwd"}, {"dd; id", "--help"}, {"sh -c id", ""}, {"no-such-program-here", "--help"}} {
		if _, err := Probe(context.Background(), bad[0], bad[1]); err == nil {
			t.Errorf("%q %q should be refused", bad[0], bad[1])
		}
	}
}

// Reading an attachment (media.ExtractDocument) goes through here: a PDF by
// pdftotext and an RTF by pandoc, in the sandbox.
func TestAttachmentsAreReadThroughFileConversion(t *testing.T) {
	needSandbox(t)
	if Installed("pdftotext") {
		pdf := fpdf.New("P", "mm", "A4", "")
		pdf.AddPage()
		pdf.SetFont("Helvetica", "", 14)
		pdf.Cell(40, 10, "Attached for review")
		var b bytes.Buffer
		pdf.Output(&b)
		text, err := media.ExtractDocument(context.Background(), media.DocumentAttachment{Name: "review.pdf", Data: b.Bytes()})
		if err != nil || !strings.Contains(text, "Attached for review") {
			t.Errorf("want the PDF's text, got %v %q", err, text)
		}
	}
	if Installed("pandoc") {
		text, err := media.ExtractDocument(context.Background(), media.DocumentAttachment{Name: "note.rtf", Data: []byte(`{\rtf1\ansi Plain words here.}`)})
		if err != nil || !strings.Contains(text, "Plain words here") {
			t.Errorf("want the RTF's text, got %v %q", err, text)
		}
	}
}

// Output: Word and OpenDocument by pandoc, in the sandbox, with a picture
// inlined the way an exported guide carries it.
func TestDocumentsAreWrittenAsWordAndOpenDocument(t *testing.T) {
	needSandbox(t)
	if !Installed("pandoc") {
		t.Skip("pandoc is not installed here")
	}
	img := base64.StdEncoding.EncodeToString(pngBytes(t))
	doc := SampleDocument()
	doc.Markdown = append(doc.Markdown, []byte("\n![a picture](data:image/png;base64,"+img+")\n")...)
	for _, ext := range []string{"docx", "odt"} {
		res, err := Export(context.Background(), doc, ext, nil)
		if err != nil || res.By != "pandoc" {
			t.Fatalf("%s: want pandoc's file, got %v %+v", ext, err, res.By)
		}
		zr, err := zip.NewReader(bytes.NewReader(res.Out), int64(len(res.Out)))
		if err != nil {
			t.Fatalf("%s: not a zip: %v", ext, err)
		}
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		joined := strings.Join(names, " ")
		t.Logf("%s holds: %s", ext, joined)
		if ext == "docx" && !strings.Contains(joined, "word/media/") {
			t.Errorf("the docx should carry the picture: %s", joined)
		}
	}
}

func TestAnOutputConversionIsChecked(t *testing.T) {
	ok := Adapted{Takes: []string{"text/markdown"}, Gives: "epub", Command: "pandoc -f markdown {in} -o {out}"}
	if err := ok.Validate(); err != nil || !ok.IsOutput() {
		t.Fatalf("a good output should pass: %v", err)
	}
	for name, mut := range map[string]func(*Adapted){
		"also becomes":  func(a *Adapted) { a.Becomes = KindText },
		"bad extension": func(a *Adapted) { a.Gives = "e pub" },
		"takes a file":  func(a *Adapted) { a.Takes = []string{"application/pdf"} },
		"takes two":     func(a *Adapted) { a.Takes = []string{"text/markdown", "text/html"} },
	} {
		a := ok
		mut(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	for in, want := range map[string]string{"docx": "docx", ".odt": "odt", "Word": "docx", "EPUB": "epub", "xyz": "xyz", "two words": ""} {
		if got := ParseOutFormat(in); got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}
	// A result has to be the format it says.
	for ext, bad := range map[string][]byte{"pdf": []byte("not a pdf"), "docx": []byte("PK\x03\x04not really"), "xyz": nil} {
		if _, err := outTarget(ext).accept(bad); err == nil {
			t.Errorf("%s: %q should be refused", ext, bad)
		}
	}
	if _, err := outTarget("xyz").accept([]byte("anything")); err != nil {
		t.Errorf("an unknown format only has to be something: %v", err)
	}
}

// Export: an adapted output the admin preferred, then the app's own
// renderer, then the native tool, then the other adapted outputs.
func TestExportTriesPreferredThenBuiltinThenNativeThenAdapted(t *testing.T) {
	needSandbox(t)
	doc := SampleDocument()
	builtin := func() ([]byte, error) { return []byte("from the app"), nil }
	if res, err := Export(context.Background(), doc, "txt", builtin); err != nil || res.By != "built in" {
		t.Fatalf("with nothing adapted, the app's renderer: %v %+v", err, res.By)
	}
	if _, err := Export(context.Background(), doc, "txt", nil); !errors.Is(err, ErrNoConversion) {
		t.Fatalf("with nothing at all, ErrNoConversion: %v", err)
	}
	mine := Adapted{Takes: []string{"text/markdown"}, Gives: "txt", Command: "cat {in}"}
	SetAdapted("mine", mine)
	defer RemoveAdapted("mine")
	if res, err := Export(context.Background(), doc, "txt", builtin); err != nil || res.By != "built in" {
		t.Errorf("not preferred, the app's renderer goes first: %v %+v", err, res.By)
	}
	if res, err := Export(context.Background(), doc, "txt", nil); err != nil || res.By != "mine" || !strings.Contains(string(res.Out), "Sample document") {
		t.Errorf("with no renderer, the adapted one writes it from the Markdown: %v %+v", err, res.By)
	}
	mine.Prefer, mine.Takes = true, []string{"text/html"}
	SetAdapted("mine", mine)
	if res, err := Export(context.Background(), doc, "txt", builtin); err != nil || res.By != "mine" || !strings.Contains(string(res.Out), "<h1>") {
		t.Errorf("preferred, the adapted one goes first, and reads the HTML: %v %+v", err, res.By)
	}
}

// Available lists what can be exported beyond the app's own formats: an
// installed native tool, an adapted output that is on.
func TestAvailableListsWhatCanBeExportedHere(t *testing.T) {
	SetAdapted("epub-mine", Adapted{Takes: []string{"text/markdown"}, Gives: "epub", Command: "pandoc -f markdown {in} -o {out}"})
	defer RemoveAdapted("epub-mine")
	got := strings.Join(Available("text/markdown", "text/html"), ",")
	if !strings.Contains(got, "epub") || strings.Contains(got, "pdf") || strings.Contains(got, "html") {
		t.Errorf("want the adapted epub and none of the app's formats: %s", got)
	}
	if Installed("pandoc") && (!strings.Contains(got, "docx") || !strings.Contains(got, "odt")) {
		t.Errorf("pandoc is installed, so Word and OpenDocument: %s", got)
	}
}

// --- Flows -----------------------------------------------------------------

// Steps run in order, each step's result the next one's {in}.
func TestAFlowRunsItsStepsInOrder(t *testing.T) {
	needSandbox(t)
	a := Adapted{Takes: []string{Any}, Becomes: KindImage, Steps: []string{
		"dd if={in} of={out.png} bs=8 skip=1",
		"cp {in} {out}",
	}}
	res, err := RunAdapted(context.Background(), "two", a, Input{Data: wrapped(t), From: "x/y"})
	if err != nil || !bytes.Equal(res.Out, pngBytes(t)) {
		t.Fatalf("want the PNG through both steps, got %v (%s)", err, res.Log)
	}
	a.Steps[1] = "cp step1.png {out}"
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "step 2") {
		t.Errorf("a later step that ignores the step before should be refused: %v", err)
	}
	// A failing step says which one it was.
	a.Steps[1] = "sh -c 'exit 4' {in}"
	if _, err := RunAdapted(context.Background(), "two", a, Input{Data: wrapped(t), From: "x/y"}); err == nil || !strings.Contains(err.Error(), "step 2") {
		t.Errorf("want the failing step named: %v", err)
	}
}

// Can also read: a folder the flow names is readable, read-only; without
// it, the sandbox does not show it; and gohort's data never can be.
func TestAFlowReadsOnlyTheFoldersItNames(t *testing.T) {
	needSandbox(t)
	models := filepath.Join(testRoot, "models")
	os.MkdirAll(models, 0o700)
	os.WriteFile(filepath.Join(models, "base.txt"), []byte("model weights"), 0o600)
	a := Adapted{Takes: []string{"text/plain"}, Becomes: KindText, Command: "cat " + filepath.Join(models, "base.txt") + " {in}"}
	if _, err := RunAdapted(context.Background(), "m", a, Input{Data: []byte(" and input"), From: "text/plain"}); err == nil {
		t.Fatal("a folder the flow does not name should not be visible to it")
	}
	a.Reads = []string{models}
	res, err := RunAdapted(context.Background(), "m", a, Input{Data: []byte(" and input"), From: "text/plain"})
	if err != nil || !strings.Contains(string(res.Out), "model weights") {
		t.Fatalf("a folder it names should be readable: %v (%s)", err, res.Log)
	}
	a.Command = "sh -c 'echo x > \"$0\"/new.txt' " + models + " {in}"
	RunAdapted(context.Background(), "m", a, Input{Data: []byte("x"), From: "text/plain"})
	if _, err := os.Stat(filepath.Join(models, "new.txt")); err == nil {
		t.Error("a folder it reads should be read-only")
	}
	for _, bad := range []string{"models", "/", filepath.Join(testRoot, "data"), filepath.Join(testRoot, "data", "workspaces"), testRoot} {
		a.Reads = []string{bad}
		if err := a.Validate(); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// A job's values reach its flow as placeholders; a missing one is said,
// and a placeholder the job does not have is refused.
func TestAJobsValuesReachItsFlow(t *testing.T) {
	needSandbox(t)
	RegisterJob(Job{ID: "test.params", Group: GroupMedia, Title: "test", Takes: []string{"text/plain"}, Becomes: KindText,
		Params: []Param{{Name: "count", Help: "how many"}}})
	a := Adapted{Job: "test.params", Command: "sh -c 'echo \"$0\"' {count} {in}"}
	SetAdapted("p", a)
	defer RemoveAdapted("p")
	res, err := Do(context.Background(), "test.params", Input{Data: []byte("x"), From: "text/plain", Params: map[string]string{"count": "7"}})
	if err != nil || strings.TrimSpace(string(res.Out)) != "7" {
		t.Fatalf("want {count} filled: %v %q", err, res.Out)
	}
	if _, err := Do(context.Background(), "test.params", Input{Data: []byte("x"), From: "text/plain"}); err == nil || !strings.Contains(err.Error(), "{count}") {
		t.Errorf("a missing value should be said: %v", err)
	}
	a.Command = "echo {seconds} {in}"
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "{count}") {
		t.Errorf("a placeholder the job does not have should be refused, naming the ones it has: %v", err)
	}
}

// A job that gives several files gets them from {outdir}, each checked.
func TestAJobGivesSeveralFilesFromOutdir(t *testing.T) {
	needSandbox(t)
	RegisterJob(Job{ID: "test.files", Group: GroupMedia, Title: "test", Takes: []string{"image/png"}, Becomes: KindImage, Files: true})
	a := Adapted{Job: "test.files", Command: `sh -c 'cp "$0" "$1"/a.png && cp "$0" "$1"/b.png' {in} {outdir}`}
	res, err := RunAdapted(context.Background(), "f", a, Input{Data: pngBytes(t), From: "image/png"})
	if err != nil || len(res.Files) != 2 || !bytes.Equal(res.Files[1], pngBytes(t)) {
		t.Fatalf("want two images from {outdir}: %v %d", err, len(res.Files))
	}
	a.Command = `sh -c 'echo nope > "$1"/a.png' {in} {outdir}`
	if _, err := RunAdapted(context.Background(), "f", a, Input{Data: pngBytes(t), From: "image/png"}); err == nil {
		t.Error("a file in {outdir} that is not an image should be refused")
	}
	a.Command = "cp {in} {out}"
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "{outdir}") {
		t.Errorf("a job that gives several files needs {outdir}: %v", err)
	}
	b := Adapted{Takes: []string{Any}, Becomes: KindImage, Command: "cp {in} {outdir}"}
	if err := b.Validate(); err == nil {
		t.Error("a job that gives one result should refuse {outdir}")
	}
}

// Only a job that needs the network has it.
func TestOnlyAJobThatNeedsTheNetworkHasIt(t *testing.T) {
	needSandbox(t)
	hostIfaces, _ := os.ReadFile("/proc/net/dev")
	if !strings.Contains(string(hostIfaces), "eth") && !strings.Contains(string(hostIfaces), "en") {
		t.Skip("this host has no network interface to see")
	}
	RegisterJob(Job{ID: "test.net", Group: GroupMedia, Title: "test", Takes: []string{"text/plain"}, Becomes: KindText, Network: true})
	a := Adapted{Job: "test.net", Command: "cat /proc/net/dev {in}"}
	res, err := RunAdapted(context.Background(), "n", a, Input{Data: []byte("x"), From: "text/plain"})
	if err != nil {
		t.Fatalf("run: %v (%s)", err, res.Log)
	}
	others := 0
	for _, line := range strings.Split(string(res.Out), "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && name != "lo" && !strings.Contains(name, "|") {
			others++
		}
	}
	if others == 0 {
		t.Error("a job with Network should see the host's interfaces")
	}
}

// Do: an adapted flow the admin preferred goes before the native one; one
// not preferred fills in when the native tool is missing.
func TestDoUsesTheNativeFlowOrTheAdaptedOne(t *testing.T) {
	needSandbox(t)
	RegisterJob(Job{ID: "test.native", Group: GroupMedia, Title: "test", Takes: []string{"text/plain"}, Becomes: KindText,
		Native: []Flow{{Steps: []string{"sh -c 'echo native' {in}"}}}})
	RegisterJob(Job{ID: "test.missing", Group: GroupMedia, Title: "missing thing", Takes: []string{"text/plain"}, Becomes: KindText,
		Native: []Flow{{Steps: []string{"no-such-tool-here {in}"}, Install: "the missing tool"}}})
	in := Input{Data: []byte("x"), From: "text/plain"}
	if res, err := Do(context.Background(), "test.native", in); err != nil || strings.TrimSpace(string(res.Out)) != "native" {
		t.Fatalf("want the native flow: %v %q", err, res.Out)
	}
	if _, err := Do(context.Background(), "test.missing", in); err == nil || !strings.Contains(err.Error(), "the missing tool") {
		t.Fatalf("with nothing to do it, say what to install: %v", err)
	}
	SetAdapted("fill", Adapted{Job: "test.missing", Command: "sh -c 'echo adapted' {in}"})
	defer RemoveAdapted("fill")
	if res, err := Do(context.Background(), "test.missing", in); err != nil || res.By != "fill" {
		t.Errorf("an adapted flow should fill in for a missing tool: %v %+v", err, res.By)
	}
	SetAdapted("mine", Adapted{Job: "test.native", Command: "sh -c 'echo mine' {in}"})
	defer RemoveAdapted("mine")
	if res, _ := Do(context.Background(), "test.native", in); res.By == "mine" {
		t.Error("not preferred, the native flow goes first")
	}
	SetAdapted("mine", Adapted{Job: "test.native", Command: "sh -c 'echo mine' {in}", Prefer: true})
	if res, _ := Do(context.Background(), "test.native", in); res.By != "mine" {
		t.Errorf("preferred, the adapted flow goes first: %+v", res.By)
	}
}

// --- Exports: one list, every format a job ---------------------------------

// pyDeps points the Python package folder at a cache that outlives the
// test, so the generators' packages are installed once, and skips when pip
// cannot provision them here (no python3, no network).
func pyDeps(t *testing.T, pkgs ...string) {
	t.Helper()
	if !Installed("python3") {
		t.Skip("python3 is not installed here")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache folder")
	}
	root := filepath.Join(cache, "gohort-flows-pydeps", "workspaces")
	os.MkdirAll(root, 0o700)
	was := deps.WorkspacesDir
	deps.WorkspacesDir = func() string { return root }
	t.Cleanup(func() { deps.WorkspacesDir = was })
	if err := deps.EnsurePyDeps(context.Background(), pkgs...); err != nil {
		t.Skipf("could not provision %v here: %v", pkgs, err)
	}
}

// The export formats are one list: PDF in Go, the documents by pandoc, the
// structured ones by their generators; a guide (Markdown, HTML) is offered
// only the formats it can feed.
func TestTheExportsAreOneList(t *testing.T) {
	var names []string
	for _, j := range Exports() {
		names = append(names, j.Gives)
	}
	got := strings.Join(names, ",")
	if !strings.Contains(got, "pdf") || strings.Contains(got, "html") || strings.Contains(got, "md") {
		t.Errorf("PDF is written here; HTML and Markdown are the app's own: %s", got)
	}
	if Installed("python3") && (!strings.Contains(got, "xlsx") || !strings.Contains(got, "pptx")) {
		t.Errorf("with python3, Excel and PowerPoint: %s", got)
	}
	doc := strings.Join(Available("text/markdown", "text/html"), ",")
	if strings.Contains(doc, "xlsx") || strings.Contains(doc, "pptx") {
		t.Errorf("a document has no data for a spreadsheet or a deck: %s", doc)
	}
	if Installed("python3") && !strings.Contains(strings.Join(Available("application/json"), ","), "xlsx") {
		t.Error("data should be offered Excel")
	}
}

// PDF is Go's own renderer, with the title and date for a cover.
func TestPDFIsWrittenInGo(t *testing.T) {
	res, err := Export(context.Background(), Document{Title: "Report", Date: "Oct 20", Markdown: []byte("# Findings\n\nAll good.")}, "pdf", nil)
	if err != nil || !bytes.HasPrefix(res.Out, []byte("%PDF-")) || res.By != "built in" {
		t.Fatalf("want a PDF in Go: %v %+v", err, res.By)
	}
	// An app's own renderer still goes first where it gives one.
	res, _ = Export(context.Background(), Document{Markdown: []byte("x")}, "pdf", func() ([]byte, error) { return []byte("%PDF-app"), nil })
	if string(res.Out) != "%PDF-app" {
		t.Error("the app's renderer should be used where it gives one")
	}
}

// Excel and PowerPoint from data, and Word from Markdown where pandoc is
// not there: the generators that were core's, as native flows in the
// sandbox with their packages provisioned.
func TestStructuredFormatsAreWrittenFromData(t *testing.T) {
	needSandbox(t)
	pyDeps(t, "openpyxl", "python-pptx")
	doc := SampleDocument()
	for ext, part := range map[string]string{"xlsx": "xl/workbook.xml", "pptx": "ppt/presentation.xml"} {
		res, err := Export(context.Background(), doc, ext, nil)
		if err != nil {
			t.Fatalf("%s: %v (%s)", ext, err, res.Log)
		}
		if err := zipHolds(res.Out, part, ext); err != nil {
			t.Errorf("%s: %v", ext, err)
		}
	}
	if _, err := Export(context.Background(), Document{Markdown: []byte("no data")}, "xlsx", nil); err == nil || !strings.Contains(err.Error(), "sheets") {
		t.Errorf("a spreadsheet with no data should say what it needs: %v", err)
	}
	// Word's fallback, as if pandoc were missing.
	t.Run("word fallback", func(t *testing.T) { wordFallback(t) })
}

func wordFallback(t *testing.T) {
	pyDeps(t, "python-docx")
	j, _ := JobByID("export.docx")
	j.Native = j.Native[1:]
	res, err := RunNative(context.Background(), j, Input{Doc: &Document{Markdown: []byte("# Title\n\n- a point\n\nA paragraph.")}})
	if err != nil || zipDocType(res.Out) != MIMEDocx {
		t.Fatalf("want a Word file from python-docx: %v (%s)", err, res.Log)
	}
}

// A converter can hand its result on as a format something here reads: the
// file goes through it, its result is checked to be that format, and that
// format's reader finishes it. The table and the Add form list what reads
// each format, gohort's own marked as such.
func TestAHandOffIsReadByTheNextReader(t *testing.T) {
	needSandbox(t)
	var b bytes.Buffer
	bmp.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	wrappedBMP := append([]byte("GOHORTX!"), b.Bytes()...)
	unwrap := Adapted{Takes: []string{"x/wrapped"}, Then: "image/bmp", Command: "dd if={in} of={out} bs=8 skip=1", TimeoutSecs: 20}
	if err := SetAdapted("unwrap", unwrap); err != nil {
		t.Fatal(err)
	}
	defer RemoveAdapted("unwrap")
	res, err := Convert(context.Background(), wrappedBMP, "x/wrapped", KindImage)
	if err != nil || res.By != "unwrap, then built in" || !Readable(res.Out, KindImage) {
		t.Fatalf("want it unwrapped, then read by gohort's BMP reader: %v %+v", err, res.By)
	}

	// A converter whose result is not the format it promised is refused,
	// not handed on as something it is not.
	liar := unwrap
	liar.Then = "application/pdf"
	SetAdapted("unwrap", liar)
	if _, err := Convert(context.Background(), wrappedBMP, "x/wrapped", KindImage); err == nil || !strings.Contains(err.Error(), "not PDF") {
		t.Errorf("a result that is not the promised format should be refused: %v", err)
	}
	SetAdapted("unwrap", unwrap)

	// Never back to a format already passed through, and no deeper than
	// maxHandOffs converters.
	if _, _, tried := handOff(context.Background(), Input{Data: wrappedBMP, From: "x/wrapped"}, KindImage, []string{"x/wrapped", "image/bmp"}); tried {
		t.Error("a hand-off back to a format already passed through was tried")
	}
	if _, _, tried := handOff(context.Background(), Input{Data: wrappedBMP, From: "x/wrapped"}, KindImage, []string{"a/a", "b/b", "x/wrapped"}); tried {
		t.Error("a hand-off past the depth was tried")
	}

	// What reads each format: gohort's own, then the converters.
	var pdf, mine *Reader
	for _, r := range Readers() {
		r := r
		switch r.Format {
		case "application/pdf":
			pdf = &r
		case "x/wrapped":
			mine = &r
		}
	}
	if pdf == nil || !pdf.Builtin || mine == nil || mine.Builtin || mine.By != "unwrap" {
		t.Errorf("want PDF read by gohort's own job and the wrapped format by the converter: %+v %+v", pdf, mine)
	}
	if BuiltinReader("application/pdf") == "" || BuiltinReader("x/wrapped") != "" {
		t.Error("BuiltinReader should name gohort's own readers only")
	}

	// A hand-off is one or the other, and never to what it takes.
	for name, bad := range map[string]Adapted{
		"both":  {Takes: []string{"x/a"}, Then: "image/bmp", Becomes: KindImage, Command: "cp {in} {out}"},
		"self":  {Takes: []string{"image/bmp"}, Then: "image/bmp", Command: "cp {in} {out}"},
		"vague": {Takes: []string{"x/a"}, Then: "pdf", Command: "cp {in} {out}"},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

// A connector that converts files can do a job in place of commands: it is
// sent the job's file and asked for the job's format, and what comes back is
// checked by the job as a command's result is. It cannot do a job that does
// not start from a file, or one that gives several files.
func TestAConnectorDoesAJob(t *testing.T) {
	prev := ConvertService
	t.Cleanup(func() { ConvertService = prev })
	var asked []string
	reply := []byte("The text of the PDF.")
	ConvertService = func(_ context.Context, connector string, in []byte, from, to string) ([]byte, error) {
		asked = append(asked, connector+" "+from+"->"+to+" "+string(in))
		return reply, nil
	}
	a := Adapted{Job: "read.pdf", Service: "converter"}
	res, err := RunAdapted(context.Background(), "pdf-by-api", a, Input{Data: []byte("%PDF-1.4 x"), From: "application/pdf"})
	if err != nil || string(res.Out) != "The text of the PDF." || res.By != "pdf-by-api" || len(asked) != 1 || asked[0] != "converter pdf->txt %PDF-1.4 x" {
		t.Fatalf("%+v %v %v", res, err, asked)
	}
	reply = []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	if _, err := RunAdapted(context.Background(), "pdf-by-api", a, Input{Data: []byte("%PDF-1.4 x"), From: "application/pdf"}); err == nil || !strings.Contains(err.Error(), "was not used") {
		t.Errorf("a result that is not text: %v", err)
	}
	for want, bad := range map[string]Adapted{
		"no commands of its own":     {Job: "read.pdf", Service: "converter", Steps: []string{"cat {in}"}},
		"reads no folders":           {Job: "read.pdf", Service: "converter", Reads: []string{"/opt/models"}},
		"does not start from a file": {Job: "media.download", Service: "converter"},
		"gives several files":        {Job: "media.frames", Service: "converter"},
	} {
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	ConvertService = nil
	if _, err := RunAdapted(context.Background(), "pdf-by-api", a, Input{Data: []byte("%PDF-1.4 x"), From: "application/pdf"}); err == nil || !strings.Contains(err.Error(), "no connectors linked") {
		t.Errorf("with no connectors linked: %v", err)
	}
}
