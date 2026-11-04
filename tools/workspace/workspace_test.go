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

// With no model that reads images, view_image says so in either form rather
// than asking a model to describe a note that stands where the picture was.
func TestViewImageSaysSoWhenNoModelReadsImages(t *testing.T) {
	blind, err := NewLLMFromConfig(LLMProviderConfig{Provider: "openai", Endpoint: "http://127.0.0.1:1/v1", Model: "m", APIKey: "k", NoImages: true})
	if err != nil {
		t.Fatalf("build a model: %v", err)
	}
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(blind, blind)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "screen.png"), []byte("\x89PNG\r\n\x1a\n a screenshot"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: dir, LLM: blind}
	for _, args := range []map[string]any{{"path": "screen.png"}, {"path": "screen.png", "show_me": true}} {
		_, err := handleViewImage(args, sess)
		if err == nil || !strings.Contains(err.Error(), "no model on this deployment reads images") {
			t.Errorf("%v: want the no-model refusal, got %v", args, err)
		}
	}
	if q := sess.DrainViewImages(); len(q) != 0 {
		t.Error("nothing may be queued for a model that cannot see it")
	}
}

// view_image looks at a picture by the reference a hand-off gives: a media#N
// someone sent, not only a workspace path. A miss lists what can be looked at.
func TestViewImageTakesAPictureSentToTheAgent(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
	photo := []byte("\x89PNG\r\n\x1a\n the photo someone sent")
	id := sess.RegisterInboundMedia("image", photo, "bob")
	if _, err := handleViewImage(map[string]any{"path": id, "show_me": true}, sess); err != nil {
		t.Fatalf("view_image %s: %v", id, err)
	}
	if q := sess.DrainViewImages(); len(q) != 1 || string(q[0].Data) != string(photo) {
		t.Fatalf("the picture sent as %s must be the one looked at: %+v", id, q)
	}
	_, err := handleViewImage(map[string]any{"path": "nothing-here.png", "show_me": true}, sess)
	if err == nil || !strings.Contains(err.Error(), "Sent to you: media#1") {
		t.Errorf("a miss must list what can be looked at, got %v", err)
	}
}
