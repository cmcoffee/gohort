package scribe

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A guide exports as Word through the System Hooks table's Word export, its
// sections in the document; the Export menu offers what is available.
func TestAGuideExportsAsWord(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("pandoc is not installed here")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no folder outside /tmp to run a conversion in")
	}
	base, _ := os.MkdirTemp(cache, "gohort-scribe-export-test-")
	flows.SetWorkBase(base)
	defer func() { flows.SetWorkBase(""); os.RemoveAll(base) }()

	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(T.DB, "alice")
	saveGuide(udb, Guide{ID: "g1", Title: "Deploy runbook", Owner: "alice", Sections: []Section{
		{ID: "a", Title: "Before you start", Markdown: "Check the **release notes** first.", Order: 1}}})
	w := httptest.NewRecorder()
	T.handleExport(w, httptest.NewRequest(http.MethodGet, "/scribe/export?id=g1&format=docx", nil), udb, "alice")
	if w.Code != 200 {
		if strings.Contains(w.Body.String(), "sandbox") {
			t.Skipf("no sandbox here: %s", w.Body.String())
		}
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != flows.MIMEDocx || !strings.Contains(w.Header().Get("Content-Disposition"), `Deploy-runbook.docx`) {
		t.Errorf("want a Word download: %s / %s", ct, w.Header().Get("Content-Disposition"))
	}
	body := w.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			if !strings.Contains(b.String(), "release notes") || !strings.Contains(b.String(), "Before you start") {
				t.Errorf("the Word file should hold the guide's sections")
			}
		}
	}
	labels := []string{}
	for _, a := range exportMenu() {
		labels = append(labels, a.Label)
	}
	if !strings.Contains(strings.Join(labels, ","), "Word (.docx)") {
		t.Errorf("the Export menu should offer Word: %v", labels)
	}
	// An unknown format is refused, not served empty.
	w = httptest.NewRecorder()
	T.handleExport(w, httptest.NewRequest(http.MethodGet, "/scribe/export?id=g1&format=no%20such", nil), udb, "alice")
	if w.Code != http.StatusBadRequest {
		t.Errorf("an unknown format should be refused, got %d", w.Code)
	}
}
