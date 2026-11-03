package workspace

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
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
