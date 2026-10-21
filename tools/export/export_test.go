package export

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/deps"
	"github.com/cmcoffee/gohort/core/flows"
)

// setup gives flows a folder outside /tmp to run in, as gohort's data
// folder lays it out.
func setup(t *testing.T) *ToolSession {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no folder outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-export-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	flows.SetWorkBase(filepath.Join(root, "data", "workspaces", "_flows"))
	t.Cleanup(func() { flows.SetWorkBase("") })
	return &ToolSession{}
}

func attached(t *testing.T, sess *ToolSession) FileAttachment {
	t.Helper()
	files := sess.ClaimUnflushedFiles()
	if len(files) != 1 {
		t.Fatalf("want one file attached, got %d", len(files))
	}
	return files[0]
}

func decode(t *testing.T, f FileAttachment) []byte {
	b, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func needSandbox(t *testing.T) {
	t.Helper()
	a := flows.Adapted{Takes: []string{"text/plain"}, Becomes: flows.KindText, Command: "cat {in}"}
	if _, err := flows.RunAdapted(context.Background(), "probe", a, flows.Input{Data: []byte("x"), From: "text/plain"}); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox here: %v", err)
	}
}

// The tool's formats are the deployment's export jobs: PDF in Go, and the
// rest as they can run here.
func TestTheToolExportsThroughTheJobs(t *testing.T) {
	sess := setup(t)
	out, _ := handleFormats(nil, sess)
	if !strings.Contains(out, "- pdf (.pdf)") {
		t.Errorf("formats should list PDF: %s", out)
	}
	if _, err := handleCreate(map[string]any{"format": "pdf", "title": "Report", "data": "# Findings\n\nAll good."}, sess); err != nil {
		t.Fatal(err)
	}
	f := attached(t, sess)
	if f.Name != "Report.pdf" || f.MimeType != "application/pdf" || !strings.HasPrefix(string(decode(t, f)), "%PDF-") {
		t.Errorf("want Report.pdf: %+v", f.Name)
	}
	if flows.Installed("pandoc") {
		needSandbox(t)
		if _, err := handleCreate(map[string]any{"format": "docx", "title": "Notes", "data": map[string]any{"markdown": "Some **text**."}}, sess); err != nil {
			t.Fatal(err)
		}
		if f := attached(t, sess); f.Name != "Notes.docx" || !strings.HasPrefix(string(decode(t, f)), "PK") {
			t.Errorf("want Notes.docx: %+v", f.Name)
		}
	}
	if _, err := handleCreate(map[string]any{"format": "docx", "data": map[string]any{"rows": 1}}, sess); err == nil {
		t.Error("Word with no markdown should say what it needs")
	}
}

// An admin's adaptation of a format is what the tool writes: an adapted
// PDF flow the admin preferred replaces Go's renderer here as everywhere.
func TestAnAdaptedFormatIsWhatTheToolWrites(t *testing.T) {
	needSandbox(t)
	sess := setup(t)
	a := flows.Adapted{Job: "export.pdf", Prefer: true, Command: `sh -c 'printf "%%PDF-adapted" > "$1"' {in} {out}`}
	if err := flows.SetAdapted("pdf-mine", a); err != nil {
		t.Fatal(err)
	}
	defer flows.RemoveAdapted("pdf-mine")
	if _, err := handleCreate(map[string]any{"format": "pdf", "data": "x"}, sess); err != nil {
		t.Fatal(err)
	}
	if got := string(decode(t, attached(t, sess))); got != "%PDF-adapted" {
		t.Errorf("the admin's flow should have written it: %q", got)
	}
}

// A spreadsheet from data, by the generator that was core's, now a job.
func TestASpreadsheetIsWrittenFromData(t *testing.T) {
	needSandbox(t)
	sess := setup(t)
	if !flows.Installed("python3") {
		t.Skip("python3 is not installed here")
	}
	cache, _ := os.UserCacheDir()
	root := filepath.Join(cache, "gohort-flows-pydeps", "workspaces")
	os.MkdirAll(root, 0o700)
	was := deps.WorkspacesDir
	deps.WorkspacesDir = func() string { return root }
	defer func() { deps.WorkspacesDir = was }()
	if err := deps.EnsurePyDeps(context.Background(), "openpyxl"); err != nil {
		t.Skipf("could not provision openpyxl: %v", err)
	}
	data := map[string]any{"sheets": []any{map[string]any{"name": "Q3", "rows": []any{[]any{"Item", "Qty"}, []any{"alpha", 1}}}}}
	if _, err := handleCreate(map[string]any{"format": "xlsx", "title": "Stock", "data": data}, sess); err != nil {
		t.Fatal(err)
	}
	if f := attached(t, sess); f.Name != "Stock.xlsx" || !strings.HasPrefix(string(decode(t, f)), "PK") {
		t.Errorf("want Stock.xlsx: %+v", f.Name)
	}
}

// A format an agent defines cannot take one of the deployment's names.
func TestADefinedFormatCannotShadowTheDeployments(t *testing.T) {
	for _, name := range []string{"pdf", "docx", "odt", "xlsx", "pptx"} {
		if !isDeploymentFormat(name) {
			t.Errorf("%s should be one of the deployment's formats", name)
		}
	}
	if isDeploymentFormat("csv") {
		t.Error("csv is not one of the deployment's formats")
	}
}
