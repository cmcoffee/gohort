package systemhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/snugforge/kvlite"
	"github.com/go-pdf/fpdf"
)

// setup gives the tool a root store, a work folder outside /tmp for flows,
// and a session workspace.
func setup(t *testing.T) (*ToolSession, string) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no folder outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-systemhook-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	flows.SetWorkBase(filepath.Join(root, "data", "workspaces", "_flows"))
	t.Cleanup(func() { flows.SetWorkBase("") })
	ws := filepath.Join(root, "ws")
	os.MkdirAll(ws, 0o700)
	return &ToolSession{WorkspaceDir: ws}, ws
}

func needSandbox(t *testing.T) {
	t.Helper()
	a := flows.Adapted{Takes: []string{"text/plain"}, Becomes: flows.KindText, Command: "cat {in}"}
	if _, err := flows.RunAdapted(context.Background(), "probe", a, flows.Input{Data: []byte("x"), From: "text/plain"}); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox here: %v", err)
	}
}

// With nothing offered, the tool is in no catalog.
func TestTheToolIsHiddenUntilAHookIsOffered(t *testing.T) {
	sess, _ := setup(t)
	if _, params := (&HookTool{}).SchemaWithSession(sess); params != nil {
		t.Fatal("with no hook offered, the tool should not be offered")
	}
	if ChatToolAvailable(&HookTool{}, sess) {
		t.Fatal("ChatToolAvailable should agree: nothing to offer")
	}
	SetOffered("read.image", true)
	desc, params := (&HookTool{}).SchemaWithSession(sess)
	if params == nil || !strings.Contains(desc, "read.image") || len(params["action"].Enum) != 1 {
		t.Fatalf("an offered hook should be the one listed: %q %+v", desc, params["action"])
	}
	if !ChatToolAvailable(&HookTool{}, sess) {
		t.Error("with a hook offered, the tool should be available")
	}
	// An export the app renders itself cannot be offered: nothing runs here.
	j, _ := flows.JobByID("export.pdf")
	if Offerable(j) {
		t.Error("PDF export is the app's own and should not be offerable")
	}
}

// An agent runs an offered hook on a workspace file, and gets the result as
// a file there; a text result comes back inline too. Nothing is overwritten.
func TestAnAgentRunsAHookOnAWorkspaceFile(t *testing.T) {
	needSandbox(t)
	sess, ws := setup(t)
	SetOffered("read.image", true)
	// A BMP is what read.image takes.
	bmp := bmpBytes(t)
	os.WriteFile(filepath.Join(ws, "scan.bmp"), bmp, 0o600)
	out, err := (&HookTool{}).RunWithSession(map[string]any{"action": "read.image", "path": "scan.bmp"}, sess)
	if err != nil || !strings.Contains(out, "scan-image.png") {
		t.Fatalf("want the image written to the workspace: %v %q", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "scan-image.png")); !flows.Readable(got, flows.KindImage) {
		t.Error("the file written should be an image the model reads")
	}
	out, _ = (&HookTool{}).RunWithSession(map[string]any{"action": "read.image", "path": "scan.bmp"}, sess)
	if !strings.Contains(out, "scan-image-2.png") {
		t.Errorf("a second run should not overwrite the first: %q", out)
	}
	if flows.Installed("pdftotext") {
		SetOffered("read.pdf", true)
		pdf := fpdf.New("P", "mm", "A4", "")
		pdf.AddPage()
		pdf.SetFont("Helvetica", "", 14)
		pdf.Cell(40, 10, "Quarterly report text")
		var pb bytes.Buffer
		pdf.Output(&pb)
		os.WriteFile(filepath.Join(ws, "report.pdf"), pb.Bytes(), 0o600)
		out, err := (&HookTool{}).RunWithSession(map[string]any{"action": "read.pdf", "path": "report.pdf"}, sess)
		if err != nil || !strings.Contains(out, "report.txt") || !strings.Contains(out, "Quarterly report text") {
			t.Errorf("a text result should be saved and returned: %v %q", err, out)
		}
	}
}

// A hook that is not offered, a path out of the workspace, or a missing
// file are refused.
func TestTheToolRefusesWhatItShould(t *testing.T) {
	sess, ws := setup(t)
	SetOffered("read.image", true)
	os.WriteFile(filepath.Join(ws, "a.bmp"), []byte("x"), 0o600)
	for name, args := range map[string]map[string]any{
		"not offered":   {"action": "read.pdf", "path": "a.bmp"},
		"out of bounds": {"action": "read.image", "path": "../a.bmp"},
		"absolute":      {"action": "read.image", "path": "/etc/passwd"},
		"missing":       {"action": "read.image", "path": "nope.bmp"},
	} {
		if _, err := (&HookTool{}).RunWithSession(args, sess); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	// A real file just outside the workspace: refused for where it is, not
	// for not being there.
	os.WriteFile(filepath.Join(filepath.Dir(ws), "outside.bmp"), bmpBytes(t), 0o600)
	if _, err := (&HookTool{}).RunWithSession(map[string]any{"action": "read.image", "path": "../outside.bmp"}, sess); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("a file outside the workspace should be refused as such: %v", err)
	}
	SetOffered("read.image", false)
	if _, params := (&HookTool{}).SchemaWithSession(sess); params != nil {
		t.Error("turned off again, the tool should be gone")
	}
}

// A custom format an admin added and turned on, offered to agents: an
// export of a Markdown document as EPUB.
func TestACustomHookIsOfferedWhenOn(t *testing.T) {
	needSandbox(t)
	if !flows.Installed("pandoc") {
		t.Skip("pandoc is not installed here")
	}
	sess, ws := setup(t)
	a := flows.Adapted{Takes: []string{"text/markdown"}, Gives: "epub", Command: "pandoc -f markdown {in} -o {out}"}
	spec, _ := json.Marshal(a)
	RootDB.Set("connectors", "epub-pandoc", Connector{Name: "epub-pandoc", Kind: flows.ConnectorKind, Spec: spec, Approved: true})
	flows.SetAdapted("epub-pandoc", a)
	defer flows.RemoveAdapted("epub-pandoc")
	SetOffered("epub-pandoc", true)
	if desc, _ := (&HookTool{}).SchemaWithSession(sess); !strings.Contains(desc, "epub-pandoc") || !strings.Contains(desc, "EPUB") {
		t.Fatalf("the custom hook should be listed: %q", desc)
	}
	os.WriteFile(filepath.Join(ws, "notes.md"), []byte("# Notes\n\nSome **text**."), 0o600)
	out, err := (&HookTool{}).RunWithSession(map[string]any{"action": "epub-pandoc", "path": "notes.md"}, sess)
	if err != nil || !strings.Contains(out, "notes.epub") {
		t.Fatalf("want the document as EPUB in the workspace: %v %q", err, out)
	}
}

// bmpBytes is a small BMP, written by hand: a 2x2 24-bit image.
func bmpBytes(t *testing.T) []byte {
	t.Helper()
	pix := []byte{0, 0, 255, 0, 255, 0, 0, 0, 255, 0, 0, 0, 0, 0, 0, 0}
	var b bytes.Buffer
	le := func(n uint32, size int) {
		for i := 0; i < size; i++ {
			b.WriteByte(byte(n >> (8 * i)))
		}
	}
	b.WriteString("BM")
	le(uint32(54+len(pix)), 4)
	le(0, 4)
	le(54, 4)
	le(40, 4)
	le(2, 4)
	le(2, 4)
	le(1, 2)
	le(24, 2)
	le(0, 4)
	le(uint32(len(pix)), 4)
	le(2835, 4)
	le(2835, 4)
	le(0, 4)
	le(0, 4)
	b.Write(pix)
	return b.Bytes()
}

// The download hook takes a link, goes online (said per call), is left out
// of a session that may not go online, and saves the video it fetched.
func TestTheDownloadHookTakesALinkAndGoesOnline(t *testing.T) {
	needSandbox(t)
	sess, ws := setup(t)
	SetOffered("media.download", true)
	SetOffered("read.image", true)
	if !flows.Installed("yt-dlp") {
		t.Skip("yt-dlp is not installed here")
	}
	_, params := (&HookTool{}).SchemaWithSession(sess)
	if _, ok := params["url"]; !ok || !contains(params["action"].Enum, "media.download") {
		t.Fatalf("an offered download should add the link: %+v", params)
	}
	caps := (&HookTool{}).ActionCaps()
	if !hasCap(caps["media.download"], CapNetwork) || hasCap(caps["read.image"], CapNetwork) {
		t.Errorf("only the download should go online: %+v", caps)
	}
	private := &ToolSession{WorkspaceDir: ws, Network: NewNetworkConnector(true)}
	if _, p := (&HookTool{}).SchemaWithSession(private); p != nil && contains(p["action"].Enum, "media.download") {
		t.Error("a session that may not go online should not be offered the download")
	}

	clip := filepath.Join(t.TempDir(), "v.mp4")
	if err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=32x32:rate=5", "-pix_fmt", "yuv420p", clip).Run(); err != nil {
		t.Skip("could not make a sample video")
	}
	video, _ := os.ReadFile(clip)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(video) }))
	defer srv.Close()
	was := flows.URLGuard
	flows.URLGuard = func(v string) error {
		if !strings.HasPrefix(v, srv.URL) {
			return fmt.Errorf("not the test server")
		}
		return nil
	}
	defer func() { flows.URLGuard = was }()
	out, err := (&HookTool{}).RunWithSession(map[string]any{"action": "media.download", "url": srv.URL + "/reel/abc.mp4"}, sess)
	if err != nil || !strings.Contains(out, ".mp4") {
		t.Fatalf("want the video saved: %v %q", err, out)
	}
	matches, _ := filepath.Glob(filepath.Join(ws, "*.mp4"))
	if len(matches) != 1 {
		t.Fatalf("want one video in the workspace: %v", matches)
	}
	if got, _ := os.ReadFile(matches[0]); !bytes.Equal(got, video) {
		t.Error("the saved video should be what the link served")
	}
	if _, err := (&HookTool{}).RunWithSession(map[string]any{"action": "media.download", "url": "http://127.0.0.1:1/x"}, sess); err == nil {
		t.Error("a link the guard refuses should be refused")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasCap(caps []Capability, c Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}
