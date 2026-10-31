package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/tools/systemhook"
	"github.com/cmcoffee/snugforge/kvlite"
	"github.com/go-pdf/fpdf"
)

type depsFixture struct {
	mux   *http.ServeMux
	token string
	db    Database
}

func newDepsFixture(t *testing.T) *depsFixture {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "someone", "pw", false)
	f := &depsFixture{mux: http.NewServeMux(), token: AuthCreateSession(db, "root"), db: db}
	a := &AdminApp{db: db}
	a.registerConnectorsRoutes(f.mux)
	a.registerSystemDepsRoutes(f.mux)
	a.registerSystemDepsFixRoutes(f.mux)
	// Runs need a folder outside /tmp, which the sandbox replaces, laid out
	// as gohort's data folder is.
	if cache, err := os.UserCacheDir(); err == nil {
		if root, err := os.MkdirTemp(cache, "gohort-admin-deps-test-"); err == nil {
			flows.SetWorkBase(filepath.Join(root, "data", "workspaces", "_flows"))
			t.Cleanup(func() { flows.SetWorkBase(""); os.RemoveAll(root) })
		}
	}
	lastTests.Lock()
	lastTests.m = map[string]testOutcome{}
	lastTests.Unlock()
	return f
}

func (f *depsFixture) req(t *testing.T, token, method, path, body, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *depsFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	return f.req(t, f.token, method, path, body, "")
}

func (f *depsFixture) rows(t *testing.T) ([]depRow, map[string]depRow) {
	t.Helper()
	var rows []depRow
	if err := json.Unmarshal(f.do(t, "GET", "/api/system-deps", "").Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	byID := map[string]depRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	return rows, byID
}

// wrappedPNG is a PNG behind 8 bytes nothing reads, which the dd command
// drops.
func wrappedPNG(t *testing.T) (wrapped, plain []byte) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	return append([]byte("GOHORTX!"), b.Bytes()...), b.Bytes()
}

const ddCustom = `{"name":"strip8","direction":"input","takes":"*","becomes":"image","steps":"dd if={in} of={out} bs=8 skip=1"}`

func needDepsSandbox(t *testing.T) {
	t.Helper()
	w, _ := wrappedPNG(t)
	a := flows.Adapted{Takes: []string{flows.Any}, Becomes: flows.KindImage, Steps: []string{"dd if={in} of={out} bs=8 skip=1"}}
	if _, err := flows.RunAdapted(context.Background(), "probe", a, flows.Input{Data: w, From: "x/y"}); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox here: %v", err)
	}
}

func samplePDF(t *testing.T, text string) []byte {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 14)
	pdf.Cell(40, 10, text)
	var b bytes.Buffer
	if err := pdf.Output(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func upload(t *testing.T, data []byte) (string, string) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "sample.bin")
	part.Write(data)
	mw.Close()
	return body.String(), mw.FormDataContentType()
}

// One table, by group in order: every job, with what does it and what to
// install, and the tools that are not jobs yet.
func TestTheTableListsJobsAndToolsByGroup(t *testing.T) {
	f := newDepsFixture(t)
	rows, byID := f.rows(t)
	for _, j := range flows.Jobs() {
		if strings.HasPrefix(j.ID, "test.") {
			continue
		}
		if r, ok := byID[j.ID]; !ok || !r.Job || r.Group != j.Group {
			t.Errorf("job %s should be a job row in %s: %+v", j.ID, j.Group, r)
		}
	}
	if r := byID["read.image"]; r.State != "ready" || r.By != "built in" {
		t.Errorf("the built-in image job should be ready: %+v", r)
	}
	if _, err := exec.LookPath("heif-convert"); err != nil {
		if r := byID["read.heic"]; r.State != "missing" || !strings.Contains(r.Detail, "libheif") {
			t.Errorf("a missing heif-convert should say what to install: %+v", r)
		}
	}
	if _, err := exec.LookPath("pdftotext"); err == nil {
		if r := byID["read.pdf"]; r.State != "ready" || !strings.HasPrefix(r.By, "pdftotext") {
			t.Errorf("pdftotext is installed, so PDF is ready by it: %+v", r)
		}
	}
	if r := byID["export.pdf"]; !r.Testable || !r.Output || r.By != "built in" {
		t.Errorf("PDF is written here in Go, and tested: %+v", r)
	}
	if r := byID["export.html"]; r.Testable {
		t.Errorf("HTML export is the app's own, not tested here: %+v", r)
	}
	for _, id := range []string{"export.xlsx", "export.pptx"} {
		if r, ok := byID[id]; !ok || r.Group != flows.GroupWriting {
			t.Errorf("%s should be a row in Writing documents: %+v", id, r)
		}
	}
	// The tools that are not jobs, in their group, with no Adapt.
	if r, ok := byID["tool:git"]; !ok || r.Job || r.Group != flows.GroupCode {
		t.Errorf("git should be a tool row in Running code: %+v", r)
	}
	if _, ok := byID["tool:pdftotext"]; ok {
		t.Error("a job's tool should be in its job's row, not listed again")
	}
	// Groups in order, each together.
	var seen []string
	for _, r := range rows {
		if len(seen) == 0 || seen[len(seen)-1] != r.Group {
			seen = append(seen, r.Group)
		}
	}
	if strings.Join(seen, "|") != strings.Join(groupOrder, "|") {
		t.Errorf("want the groups once each, in order: %v", seen)
	}
}

// Adapting a job saves a flow under it, off; on, gohort uses it where the
// native tool cannot; deleted, it stops.
func TestAJobIsAdaptedUnderItsRow(t *testing.T) {
	needDepsSandbox(t)
	f := newDepsFixture(t)
	var form flowForm
	json.Unmarshal(f.do(t, "GET", "/api/system-deps/adapt?id=read.heic", "").Body.Bytes(), &form)
	if form.Name != "read-heic-adapted" || form.Prefer {
		t.Errorf("Adapt should suggest a name, not preferred: %+v", form)
	}
	body := `{"name":"heic-two-steps","steps":"dd if={in} of={out.png} bs=8 skip=1\ncp {in} {out}"}`
	if w := f.do(t, "POST", "/api/system-deps?job=read.heic", body); w.Code != 200 {
		t.Fatalf("adapt: %d %s", w.Code, w.Body.String())
	}
	rows, byID := f.rows(t)
	r := byID["heic-two-steps"]
	if !r.Fills || r.Custom || r.State != "off" || r.Group != flows.GroupReading || !strings.Contains(r.By, "then") {
		t.Fatalf("want an off, two-step flow filling read.heic: %+v", r)
	}
	for i, row := range rows {
		if row.ID == "read.heic" && (i+1 >= len(rows) || rows[i+1].ID != "heic-two-steps") {
			t.Error("the adapted flow should sit right under the job it fills")
		}
	}
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	_, plain := wrappedPNG(t)
	in := append(heic[:0:0], []byte("GOHORTX!")...)
	in = append(in, plain...)
	f.do(t, "POST", "/api/connectors?action=approve&name=heic-two-steps", "")
	// The flow keeps the job's contract: a HEIC becomes an image.
	res, err := flows.Convert(context.Background(), in, "image/heic", flows.KindImage)
	if _, installed := exec.LookPath("heif-convert"); installed != nil && (err != nil || res.By != "heic-two-steps") {
		t.Errorf("with heif-convert missing, the adapted flow should do it: %v %+v", err, res.By)
	}
	f.do(t, "DELETE", "/api/connectors?name=heic-two-steps", "")
	if _, ok := mapOf(f, t)["heic-two-steps"]; ok {
		t.Error("deleted, it should be gone")
	}
}

func mapOf(f *depsFixture, t *testing.T) map[string]depRow { _, m := f.rows(t); return m }

// customRows are the Custom area's table.
func (f *depsFixture) customRows(t *testing.T) map[string]depRow {
	t.Helper()
	var rows []depRow
	if err := json.Unmarshal(f.do(t, "GET", "/api/system-deps?custom=1", "").Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]depRow{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// A format gohort does not support is added as its own row; changing one
// that is on turns it off; the form reads short names.
func TestAFormatIsAddedAndEditedSafely(t *testing.T) {
	needDepsSandbox(t)
	f := newDepsFixture(t)
	if w := f.do(t, "POST", "/api/system-deps", ddCustom); w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	r := f.customRows(t)["strip8"]
	if !r.Custom || r.Fills || r.State != "off" || r.Enables != "Reads anything else" {
		t.Fatalf("want an off added format in the Custom table: %+v", r)
	}
	if _, ok := mapOf(f, t)["strip8"]; ok {
		t.Error("an added format belongs in the Custom table, not the main one")
	}
	in, plain := wrappedPNG(t)
	if _, err := flows.Convert(context.Background(), in, "", flows.KindImage); !errors.Is(err, flows.ErrNoConversion) {
		t.Fatalf("off, it should not be used: %v", err)
	}
	f.do(t, "POST", "/api/connectors?action=approve&name=strip8", "")
	if res, err := flows.Convert(context.Background(), in, "", flows.KindImage); err != nil || !bytes.Equal(res.Out, plain) {
		t.Fatalf("on, it should be used: %v", err)
	}
	edited := strings.Replace(ddCustom, "bs=8 skip=1", "bs=4 skip=2", 1)
	if w := f.do(t, "POST", "/api/system-deps?edit=strip8", edited); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if c, _ := GetConnector(f.db, "strip8"); c.Approved {
		t.Error("an edit should turn it off")
	}
	// Formats no reader of gohort's takes (these two are media jobs'), by
	// their short names.
	short := `{"name":"stills","direction":"input","takes":"mov, mkv","becomes":"image","steps":"ffmpeg -i {in} -frames:v 1 {out.png}"}`
	if w := f.do(t, "POST", "/api/system-deps", short); w.Code >= 300 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var form flowForm
	json.Unmarshal(f.do(t, "GET", "/api/system-deps/one?name=stills", "").Body.Bytes(), &form)
	if form.Takes != "mov, mkv" || form.Direction != "input" {
		t.Errorf("the edit form should show it short: %+v", form)
	}
	if w := f.do(t, "POST", "/api/system-deps", short); w.Code != http.StatusBadRequest {
		t.Errorf("adding a name twice should be refused, got %d", w.Code)
	}
	bad := strings.Replace(ddCustom, `"strip8"`, `"bad"`, 1)
	bad = strings.Replace(bad, " if={in}", "", 1)
	if w := f.do(t, "POST", "/api/system-deps", bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "{in}") {
		t.Errorf("a flow with no {in} should be refused: %d %s", w.Code, w.Body.String())
	}
}

// Test runs a row on a sample: a job's native flow, an adapted flow (off),
// and an export on gohort's sample document.
func TestARowIsTested(t *testing.T) {
	needDepsSandbox(t)
	f := newDepsFixture(t)
	f.do(t, "POST", "/api/system-deps", ddCustom)
	in, _ := wrappedPNG(t)
	body, ctype := upload(t, in)
	var got struct {
		OK       bool   `json:"ok"`
		Message  string `json:"message"`
		Preview  string `json:"preview"`
		Text     string `json:"text"`
		Download string `json:"download"`
	}
	w := f.req(t, f.token, "POST", "/api/system-deps/test?id=strip8", body, ctype)
	json.Unmarshal(w.Body.Bytes(), &got)
	if !got.OK || !strings.HasPrefix(got.Preview, "data:image/png;base64,") {
		t.Fatalf("want a worked test with a preview: %s", w.Body.String())
	}
	if _, err := exec.LookPath("pdftotext"); err == nil {
		body, ctype = upload(t, samplePDF(t, "Test of the native row"))
		got = struct {
			OK       bool   `json:"ok"`
			Message  string `json:"message"`
			Preview  string `json:"preview"`
			Text     string `json:"text"`
			Download string `json:"download"`
		}{}
		w = f.req(t, f.token, "POST", "/api/system-deps/test?id=read.pdf", body, ctype)
		json.Unmarshal(w.Body.Bytes(), &got)
		if !got.OK || !strings.Contains(got.Text, "native row") {
			t.Errorf("want the PDF's text from the job's row: %s", w.Body.String())
		}
	}
	if _, err := exec.LookPath("pandoc"); err == nil {
		w = f.do(t, "POST", "/api/system-deps/test?id=export.docx", "")
		json.Unmarshal(w.Body.Bytes(), &got)
		if !got.OK || !strings.HasPrefix(got.Download, "data:"+flows.MIMEDocx) {
			t.Errorf("want the sample as Word to download: %s", w.Body.String())
		}
	}
}

func TestSystemDepsAreAdminOnly(t *testing.T) {
	f := newDepsFixture(t)
	tok := AuthCreateSession(f.db, "someone")
	for _, p := range []string{"/api/system-deps", "/api/system-deps/test?id=x", "/api/system-deps/fix?id=read.heic"} {
		if w := f.req(t, tok, "POST", p, ddCustom, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s: a non-admin should be refused, got %d", p, w.Code)
		}
	}
}

// One section on Capabilities: the table, then the Custom area; the old
// File Conversion section is gone.
func TestSystemDependenciesIsOneSection(t *testing.T) {
	var sec *ui.Section
	n := 0
	for _, s := range (&AdminApp{}).capabilitiesSections() {
		if s.Title == "System Hooks" {
			s := s
			sec = &s
			n++
		}
		if s.Title == "File Conversion" {
			t.Error("File Conversion should be part of System Hooks now")
		}
	}
	if sec == nil || n != 1 {
		t.Fatalf("want one System Hooks section, got %d", n)
	}
	// Fix what cannot run here, the grouped table, then the Custom area:
	// its table and Add behind a button. No Builder conversation.
	kids := sec.Body.(ui.Stack).Children
	if len(kids) != 3 {
		t.Fatalf("want the walk, the table and the Custom area, got %d parts", len(kids))
	}
	walk, ok := kids[0].(ui.ModalButton)
	p, _ := walk.Body.(ui.AssistPanel)
	if !ok || p.Walk == nil || p.Walk.Source != "api/system-deps?fix=1" || !p.Attach || p.URL != "api/system-deps/fix?id={id}" {
		t.Errorf("the section should open with the walk through what cannot run: %+v", kids[0])
	}
	if tbl, ok := kids[1].(ui.Table); !ok || tbl.GroupBy != "group" {
		t.Error("then the grouped table")
	}
	custom, ok := kids[2].(ui.Subsection)
	if !ok || custom.Title != "Custom" {
		t.Fatalf("the last part should be the Custom area: %+v", kids[2])
	}
	parts := custom.Body.(ui.Stack).Children
	if tbl, ok := parts[0].(ui.Table); !ok || tbl.Source != "api/system-deps?custom=1" {
		t.Error("the Custom area should list the custom formats")
	}
	if len(parts) != 2 {
		t.Fatalf("want the custom table and Add, got %d", len(parts))
	}
	if mb, ok := parts[1].(ui.ModalButton); !ok || mb.Label != "Add…" {
		t.Errorf("Add should follow the custom table: %+v", parts[1])
	}
	raw, _ := json.Marshal(sec)
	if strings.Contains(string(raw), "agent_loop") || strings.Contains(string(raw), "Ask Builder") {
		t.Error("the Builder conversation should be gone")
	}
}

func TestSystemDepsScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	path := filepath.Join(t.TempDir(), "test.js")
	os.WriteFile(path, []byte("var f = "+systemDepsTestAction+";\n"), 0o600)
	if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

// --- Fix ------------------------------------------------------------------

// A row whose test failed, or which cannot run, has ✨ Fix in place of its
// Adapt or Edit, and says why; a test that passes clears it, and so does a
// change to the flow.
func TestABrokenRowHasFixInPlaceOfAdapt(t *testing.T) {
	f := newDepsFixture(t)
	_, byID := f.rows(t)
	if r := byID["read.pdf"]; r.FixJob && r.State != "missing" {
		t.Fatalf("read.pdf should only be fixable when it cannot run: %+v", r)
	}
	recordTest("read.pdf", testOutcome{Message: "pdftotext failed: exit status 1"})
	_, byID = f.rows(t)
	if r := byID["read.pdf"]; !r.FixJob || r.Adapt || !strings.Contains(r.Detail, "Last test failed: pdftotext failed") {
		t.Errorf("a failed test should put Fix in place of Adapt, with why: %+v", r)
	}
	recordTest("read.pdf", testOutcome{OK: true})
	if _, byID = f.rows(t); byID["read.pdf"].FixJob && byID["read.pdf"].State != "missing" {
		t.Error("a passing test should clear Fix")
	}
	// A flow whose program is not installed.
	if _, err := saveAdapted("root", "heic-nope", flows.Adapted{Job: "read.heic", Steps: []string{"no-such-program-here {in} {out}"}}, false, "x"); err != nil {
		t.Fatal(err)
	}
	_, byID = f.rows(t)
	if r := byID["heic-nope"]; !r.FixFill || r.EditFill || r.State != "missing" {
		t.Errorf("a flow that cannot run should have Fix in place of Edit: %+v", r)
	}
	recordTest("heic-nope", testOutcome{Message: "boom"})
	saveAdapted("root", "heic-nope", flows.Adapted{Job: "read.heic", Steps: []string{"cp {in} {out}"}}, true, "x")
	if _, failed := failedTest("heic-nope"); failed {
		t.Error("a changed flow should forget the old flow's test")
	}
}

// The walk lists the jobs that need fixing, and only those.
func TestTheWalkListsWhatCannotRun(t *testing.T) {
	f := newDepsFixture(t)
	recordTest("read.pdf", testOutcome{Message: "failed"})
	var rows []depRow
	json.Unmarshal(f.do(t, "GET", "/api/system-deps?fix=1", "").Body.Bytes(), &rows)
	found := false
	for _, r := range rows {
		if !r.FixJob {
			t.Errorf("%s is in the walk but needs no fixing", r.ID)
		}
		found = found || r.ID == "read.pdf"
	}
	if !found {
		t.Error("the job whose test failed should be in the walk")
	}
}

// The adapt form never offers a name that is taken.
func TestTheAdaptFormOffersAFreeName(t *testing.T) {
	f := newDepsFixture(t)
	saveAdapted("root", "read-heic-adapted", flows.Adapted{Job: "read.heic", Steps: []string{"cp {in} {out}"}}, false, "x")
	var ff flowForm
	json.Unmarshal(f.do(t, "GET", "/api/system-deps/adapt?id=read.heic", "").Body.Bytes(), &ff)
	if ff.Name != "read-heic-adapted-2" {
		t.Errorf("want a free name, got %q", ff.Name)
	}
}

// The assistant is told which row, what it has to do, why it fails, and
// what the form beside it is.
func TestTheFixKnowsTheRow(t *testing.T) {
	newDepsFixture(t)
	recordTest("read.heic", testOutcome{Message: "heif-dec is not installed", Log: "the end of the log"})
	about, err := fixAbout(httptest.NewRequest("POST", "/api/system-deps/fix?id=read.heic", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"job read.heic: iPhone photos", "placeholders {in} {out}", "Its last test failed: heif-dec is not installed", "the end of the log", "a new flow for this job", "cannot install software"} {
		if !strings.Contains(about, want) {
			t.Errorf("the assistant is not told %q:\n%s", want, about)
		}
	}
	if _, err := fixAbout(httptest.NewRequest("POST", "/api/system-deps/fix?id=nope", nil)); err == nil {
		t.Error("an unknown row should be refused")
	}
}

// The assistant's tools read and try; none of them saves or turns on.
func TestTheFixToolsNeverSave(t *testing.T) {
	newDepsFixture(t)
	var names []string
	for _, d := range fixTools(httptest.NewRequest("POST", "/?id=read.heic", nil)) {
		names = append(names, d.Tool.Name)
		for _, c := range d.Tool.Caps {
			if c == CapWrite {
				t.Errorf("%s writes", d.Tool.Name)
			}
		}
	}
	if strings.Join(names, ",") != "dependency_list,dependency_check_program,dependency_try,dependency_test" {
		t.Errorf("unexpected tools: %v", names)
	}
	text := depsText()
	for _, want := range []string{"job read.heic: iPhone photos", "placeholders {in} {out}", "tool git"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// A try runs the steps on the attached file, keeping the job's contract,
// and saves nothing; the folders it may read are never the model's to set.
func TestATryRunsOnTheAttachedFile(t *testing.T) {
	needDepsSandbox(t)
	newDepsFixture(t)
	ctx := context.Background()
	if _, err := fixTry(ctx, "read.heic", map[string]any{"steps": "dd if={in} of={out} bs=8 skip=1"}); err == nil {
		t.Error("a try on a job that reads a file, with nothing attached, should ask for one")
	}
	in, _ := wrappedPNG(t)
	ctx = assist.WithFiles(ctx, []assist.File{{ID: "file-1", Name: "photo.heic", Data: in}})
	out, err := fixTry(ctx, "read.heic", map[string]any{"steps": "dd if={in} of={out} bs=8 skip=1", "reads": "/etc"})
	if err != nil || !strings.Contains(out, "Worked on file-1") || !strings.Contains(out, "4x4") || !strings.Contains(out, "Nothing was saved") {
		t.Errorf("want the try to work on the attached file: %v %q", err, out)
	}
	if len(ListConnectors(RootDB)) != 0 {
		t.Error("a try saved a flow")
	}
	if out, _ := fixTry(ctx, "read.heic", map[string]any{"steps": "dd if={in} of={out} bs=1 skip=0"}); !strings.Contains(out, "Failed") {
		t.Errorf("a try that gives what the job cannot use should say it failed: %q", out)
	}
	if _, err := fixTest(context.Background(), "media.download", "", ""); err == nil {
		t.Error("testing a download without a link should ask for one")
	}
}

// The whole turn, through the endpoint: the worker is told the row, tries
// steps on the file attached in the window, and fills the form.
func TestTheFixEndpointFillsTheForm(t *testing.T) {
	needDepsSandbox(t)
	f := newDepsFixture(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "dependency_try", Args: map[string]any{"steps": "dd if={in} of={out} bs=8 skip=1"}}}},
		{Content: "dd strips the header, and the try worked.\n\n```fields\n{\"steps\": [{\"command\": \"dd if={in} of={out} bs=8 skip=1\"}]}\n```"},
	}}
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(fake, nil)
	defer SetSharedLLMs(prevW, prevL)
	in, _ := wrappedPNG(t)
	body, _ := json.Marshal(map[string]any{
		"message": "fix it", "window": "w1",
		"files":  []map[string]string{{"name": "photo.heic", "data": base64.StdEncoding.EncodeToString(in)}},
		"form":   []map[string]any{{"field": "steps", "type": "rows", "columns": []map[string]string{{"field": "command"}}}},
		"values": map[string]any{"steps": []any{}},
	})
	r := httptest.NewRequest("POST", "/api/system-deps/fix?id=read.heic", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/x-ndjson") // as the browser's helper asks
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: f.token})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	got := w.Body.String()
	if !strings.Contains(got, `"progress":"dependency try: dd if={in} of={out} bs=8 skip=1"`) || !strings.Contains(got, `"fields":{"steps":[{"command":"dd if={in} of={out} bs=8 skip=1"}]}`) {
		t.Fatalf("want the try reported and the form filled: %s", got)
	}
	if sys := fake.Config(0).SystemPrompt; !strings.Contains(sys, "job read.heic") || !strings.Contains(sys, "file-1 (photo.heic") {
		t.Errorf("the worker should know the row and the file: %s", sys)
	}
	last := fake.LastSent()[len(fake.LastSent())-1]
	reached := strings.Contains(last.Content, "Worked on file-1")
	for _, tr := range last.ToolResults {
		reached = reached || strings.Contains(tr.Content, "Worked on file-1")
	}
	if !reached {
		t.Errorf("the try's outcome should reach the model: %+v", last)
	}
}

// The media jobs are rows in Media and the web, with the tools they drive;
// ffmpeg and ffprobe are no longer listed apart; transcription says what to
// set up when no speech service is.
func TestTheMediaJobsAreRows(t *testing.T) {
	f := newDepsFixture(t)
	was := media.GetTranscribeConfig()
	defer media.SetTranscribeConfig(was)
	media.SetTranscribeConfig(media.TranscribeConfig{})
	_, byID := f.rows(t)
	for _, id := range []string{"media.frames", "media.metadata", "media.audio", "media.wav", "media.transcribe"} {
		if r, ok := byID[id]; !ok || !r.Job || r.Group != flows.GroupMedia {
			t.Errorf("%s should be a job row in Media: %+v", id, r)
		}
	}
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		if r := byID["media.frames"]; r.State != "ready" || !strings.Contains(r.By, "ffprobe") || !strings.Contains(r.By, "ffmpeg") {
			t.Errorf("frame sampling should be ready by ffprobe and ffmpeg: %+v", r)
		}
	}
	for _, tool := range []string{"tool:ffmpeg", "tool:ffprobe"} {
		if _, ok := byID[tool]; ok {
			t.Errorf("%s is a job's tool now and should not be listed apart", tool)
		}
	}
	if r := byID["media.transcribe"]; r.State != "missing" || !strings.HasPrefix(r.Detail, "To use it, set up a speech-to-text service") {
		t.Errorf("with no speech service, transcription should say what to set up: %+v", r)
	}
}

// Test on a media job gives its results: a video's frames, the first shown.
func TestAMediaJobIsTested(t *testing.T) {
	needDepsSandbox(t)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed here")
	}
	f := newDepsFixture(t)
	out := filepath.Join(t.TempDir(), "v.mp4")
	if b, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=3:size=64x64:rate=10", "-pix_fmt", "yuv420p", out).CombinedOutput(); err != nil {
		t.Skipf("could not make a sample video: %v %s", err, b)
	}
	video, _ := os.ReadFile(out)
	body, ctype := upload(t, video)
	w := f.req(t, f.token, "POST", "/api/system-deps/test?id=media.frames", body, ctype)
	var got struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Preview string `json:"preview"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if !got.OK || !strings.Contains(got.Message, "results") || !strings.HasPrefix(got.Preview, "data:image/") {
		t.Errorf("want the frames, the first shown: %s", w.Body.String())
	}
}

// Steps are a list the form adds to a row at a time ("+ Add step"): saved
// as the list, read back as the list, blank rows dropped. A string of one
// command per line still saves (Builder's tool posts that).
func TestStepsAreAListOfRows(t *testing.T) {
	f := newDepsFixture(t)
	body := `{"name":"two","direction":"input","takes":"*","becomes":"image","steps":[{"command":"dd if={in} of={out.png} bs=8 skip=1"},{"command":""},{"command":"cp {in} {out}"}]}`
	if w := f.do(t, "POST", "/api/system-deps", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	c, _ := GetConnector(f.db, "two")
	var a flows.Adapted
	json.Unmarshal(c.Spec, &a)
	if len(a.Steps) != 2 || a.Steps[1] != "cp {in} {out}" {
		t.Errorf("want two steps, the blank row dropped: %q", a.Steps)
	}
	var raw map[string]any
	json.Unmarshal(f.do(t, "GET", "/api/system-deps/one?name=two", "").Body.Bytes(), &raw)
	rows, _ := raw["steps"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["command"] != "dd if={in} of={out.png} bs=8 skip=1" {
		t.Errorf("the edit form should get the steps back as rows: %v", raw["steps"])
	}
	var field ui.FormField
	for _, fl := range flowFields(true, true) {
		if fl.Field == "steps" {
			field = fl
		}
	}
	if field.Type != "rows" || field.AddLabel != "+ Add step" || len(field.Columns) != 1 || field.Columns[0].Type != "textarea" {
		t.Errorf("Steps should be rows with + Add step: %+v", field)
	}
}

// System Hooks is the first thing on Capabilities: the settings below it
// are what some of its rows point to.
func TestSystemHooksIsFirstOnCapabilities(t *testing.T) {
	secs := (&AdminApp{}).capabilitiesSections()
	if len(secs) == 0 || secs[0].Title != "System Hooks" || sectionTab(secs[0].Title, "") != "Capabilities" {
		t.Errorf("System Hooks should open the Capabilities tab, got %q", secs[0].Title)
	}
}

// A format an admin added reaches every agent once it is on, with
// convert_file: there is no Agents switch per format any more.
func TestAnAddedFormatReachesAgentsOnceItIsOn(t *testing.T) {
	needDepsSandbox(t)
	f := newDepsFixture(t)
	f.do(t, "POST", "/api/system-deps", ddCustom)
	if systemhook.Available(false) {
		t.Fatal("a format that is off is offered")
	}
	f.do(t, "POST", "/api/connectors?action=approve&name=strip8", "")
	if !systemhook.Available(false) {
		t.Error("a format that is on is not offered to agents")
	}
	if w := f.do(t, "POST", "/api/system-deps/agents?id=strip8", `{"agents":true}`); w.Code == http.StatusOK {
		t.Error("the per-format Agents switch is still there")
	}
}

// The download is a row that takes a link: yt-dlp is its tool now (not
// listed apart), and Test fetches from the link the admin gives.
func TestTheDownloadRowTakesALink(t *testing.T) {
	needDepsSandbox(t)
	f := newDepsFixture(t)
	_, byID := f.rows(t)
	r, ok := byID["media.download"]
	if !ok || !r.Job || !r.Link || r.Group != flows.GroupMedia {
		t.Fatalf("the download should be a link row in Media: %+v", r)
	}
	if _, ok := byID["tool:yt-dlp"]; ok {
		t.Error("yt-dlp is the download's tool now and should not be listed apart")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed here")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed here")
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
			return errors.New("not the test server")
		}
		return nil
	}
	defer func() { flows.URLGuard = was }()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("url", srv.URL+"/clip.mp4")
	mw.Close()
	w := f.req(t, f.token, "POST", "/api/system-deps/test?id=media.download", body.String(), mw.FormDataContentType())
	if !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), "from the link") {
		t.Errorf("want the video fetched from the link: %s", w.Body.String())
	}
	if out, err := fixTest(context.Background(), "media.download", "", ""); err == nil {
		t.Errorf("testing a download without a link should ask for one: %q", out)
	}
}

// The row's Test is what marks it: a test that fails through the page puts
// Fix on the row.
func TestAFailedTestMarksTheRow(t *testing.T) {
	f := newDepsFixture(t)
	saveAdapted("root", "heic-nope", flows.Adapted{Job: "read.heic", Steps: []string{"no-such-program-here {in} {out}"}}, false, "x")
	forgetTest("heic-nope")
	in, _ := wrappedPNG(t)
	body, ctype := upload(t, in)
	w := f.req(t, f.token, "POST", "/api/system-deps/test?id=heic-nope", body, ctype)
	if !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("the test should fail: %s", w.Body.String())
	}
	if _, failed := failedTest("heic-nope"); !failed {
		t.Error("a failed test should be recorded for the row")
	}
}

// Add has the hooks assistant too: no row yet, so it is told what adding a
// format is, and a try takes the contract being written.
func TestTheAddFormHasTheHooksAssistant(t *testing.T) {
	needDepsSandbox(t)
	newDepsFixture(t)
	about, err := fixAbout(httptest.NewRequest("POST", "/api/system-deps/fix", nil))
	if err != nil || !strings.Contains(about, "adding a custom format") {
		t.Fatalf("Add's assistant should know it is adding a format: %v %q", err, about)
	}
	in, _ := wrappedPNG(t)
	ctx := assist.WithFiles(context.Background(), []assist.File{{ID: "file-1", Data: in}})
	out, err := fixTry(ctx, "", map[string]any{"steps": "dd if={in} of={out} bs=8 skip=1", "direction": "input", "takes": "*", "becomes": "image"})
	if err != nil || !strings.Contains(out, "Worked on file-1") {
		t.Errorf("a try of a new input format: %v %q", err, out)
	}
	if fp := addForm(); !fp.Assist || fp.AssistURL != "api/system-deps/fix" || !fp.AssistAttach {
		t.Errorf("the Add form should offer the hooks assistant: %+v", fp)
	}
	if fp := adaptForm(); fp.AssistURL != "api/system-deps/fix?id={id}" {
		t.Errorf("Adapt's assistant should be the row's: %q", fp.AssistURL)
	}
}

// Add takes a format gohort does not read, and may hand its result on to a
// reader that is here: listed with what reads it, built in or a converter.
// A format gohort already reads is that row's Adapt, not an Add.
func TestAddHandsOnToAReaderThatIsHere(t *testing.T) {
	f := newDepsFixture(t)
	if w := f.do(t, "POST", "/api/system-deps", `{"name":"mypdf","direction":"input","takes":"pdf","becomes":"text","steps":"pdftotext {in} {out}"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "adapt that row") {
		t.Errorf("a format gohort reads should point to its row: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(t, "POST", "/api/system-deps", `{"name":"pages","direction":"input","takes":"application/x-pages","becomes":"then:x/nothing-reads","steps":"cp {in} {out}"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a hand-off to a format nothing reads should be refused: %d", w.Code)
	}
	if w := f.do(t, "POST", "/api/system-deps", `{"name":"pages","direction":"input","takes":"application/x-pages","becomes":"then:application/pdf","steps":"cp {in} {out}"}`); w.Code >= 300 {
		t.Fatalf("a hand-off to PDF: %d %s", w.Code, w.Body.String())
	}
	var ff flowForm
	json.Unmarshal(f.do(t, "GET", "/api/system-deps/one?name=pages", "").Body.Bytes(), &ff)
	if ff.Becomes != "then:application/pdf" {
		t.Errorf("the form should read the hand-off back: %q", ff.Becomes)
	}
	var custom []depRow
	json.Unmarshal(f.do(t, "GET", "/api/system-deps?custom=1", "").Body.Bytes(), &custom)
	if len(custom) != 1 || !strings.Contains(custom[0].Detail, "Hands it on as PDF, which gohort reads (built in") {
		t.Errorf("the row should name the chain: %+v", custom)
	}
	body := f.do(t, "GET", "/api/system-deps/becomes", "").Body.String()
	for _, want := range []string{`"value":"image"`, `"value":"then:application/pdf"`, `"label":"PDF (built in)"`, `"group":"Or hand it on as"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Becomes should offer %s: %s", want, body)
		}
	}
}

// A job can be adapted to a connector that converts files: the form offers
// Done by once one is approved (the steps then only for commands), saves it
// as the flow's service with no steps, reads back the same, and the row says
// which connector does it.
func TestAJobCanBeDoneByAConnector(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	field := func(name string) (ui.FormField, bool) {
		for _, f := range flowFields(false, false) {
			if f.Field == name {
				return f, true
			}
		}
		return ui.FormField{}, false
	}
	if _, ok := field("by"); ok {
		t.Error("Done by offered with only commands to choose")
	}
	spec := `{"provider":{"transport":"http","base_url":"https://convert.example.test","auth":{"none":true},
		"operations":{"convert":{"method":"POST","path":"/convert/{from}/to/{to}","send":"multipart","body":{"File":"{file}"}}}},
		"provides":[{"capability":"file.convert","steps":[{"call":"convert"}],"output":{"file":{"path":"Files.0.FileData","as":"base64"}}}]}`
	SaveConnector(RootDB, Connector{Name: "converter", Kind: "service", Spec: json.RawMessage(spec)})
	ApproveConnector(RootDB, "converter")
	if f, ok := field("by"); !ok || len(f.Options) != 2 || f.Options[1].Value != "service:converter" {
		t.Errorf("Done by: %+v", f)
	}
	if f, _ := field("steps"); f.ShowWhen != "by:commands" {
		t.Errorf("steps shown for a connector: %+v", f)
	}
	a := flowForm{By: "service:converter", Steps: stepsOf([]string{"cat {in}"})}.adapted("read.pdf")
	if a.Service != "converter" || len(a.Steps) != 0 || a.Validate() != nil {
		t.Errorf("saved as: %+v %v", a, a.Validate())
	}
	if ff := formOf("pdf-by-api", a); ff.By != "service:converter" {
		t.Errorf("read back: %+v", ff)
	}
	if ff := formOf("pdf", flows.Adapted{Job: "read.pdf", Steps: []string{"pdftotext {in} {out}"}}); ff.By != "commands" {
		t.Errorf("a command flow reads back as: %+v", ff)
	}
	spec2, _ := json.Marshal(a)
	if row := adaptedRow(Connector{Name: "pdf-by-api", Kind: flows.ConnectorKind, Spec: spec2, Approved: true}, a, false); row.By != "the connector converter" || row.State != "on" {
		t.Errorf("row: %+v", row)
	}
}
