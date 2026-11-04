package workspace

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A Word document is a zip archive inside, and was delivered as
// application/zip: an app that names downloads from the type saved it as
// .zip. It goes out as a Word document.
func TestAWordDocumentIsAttachedAsOne(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("word/document.xml")
	f.Write([]byte("<w:document/>"))
	zw.Close()
	if err := os.WriteFile(filepath.Join(dir, "comparison.docx"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir}
	sess.AddStagedFiles([]string{"comparison.docx"})
	if _, err := AttachWorkspaceFile(sess, "comparison.docx", "", false); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if len(sess.Files) != 1 {
		t.Fatalf("the document must go out as a file, got %d", len(sess.Files))
	}
	if got := sess.Files[0].MimeType; got != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("a .docx must be sent as a Word document, got %q", got)
	}
}

// show_me puts the image itself in front of the model on its next step,
// labelled, with no second model asked to describe it: a screenshot's small
// print, or two pictures compared, need the pixels.
func TestShowMePutsTheImageInFrontOfTheModel(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n a screenshot")
	if err := os.WriteFile(filepath.Join(dir, "screen.png"), png, 0600); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir} // no LLM: none is asked
	out, err := handleViewImage(map[string]any{"path": "screen.png", "show_me": true}, sess)
	if err != nil {
		t.Fatalf("show_me: %v", err)
	}
	seen := sess.DrainViewImages()
	if len(seen) != 1 || string(seen[0].Data) != string(png) || !strings.Contains(seen[0].Label, "screen.png") {
		t.Fatalf("the image must be queued for the next step, labelled by its path: %+v", seen)
	}
	if !strings.Contains(out, "next step") {
		t.Errorf("the result must say where the image went: %q", out)
	}
	// Without show_me it is still a description, which needs a model.
	if _, err := handleViewImage(map[string]any{"path": "screen.png"}, sess); err == nil {
		t.Error("a description with no model bound must fail as before")
	}
}
