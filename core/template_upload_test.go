package core

// "Upload a file" as a DECLARATION. Before this, a file could only leave
// through Go written for one endpoint — which is how the transcription client
// came to build its own http.Client and bypass every control the framework has.
// A template author now names one parameter and gets a governed, streaming
// upload.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildRestTool(t *testing.T, vals map[string]any) TempTool {
	t.Helper()
	tpl, ok := GetTemplate(TargetTool, "rest_call")
	if !ok {
		t.Fatal("rest_call template not registered")
	}
	raw, _, err := restToolBuildSpec(tpl, vals)
	if err != nil {
		t.Fatalf("restToolBuildSpec: %v", err)
	}
	var tt TempTool
	if err := json.Unmarshal(raw, &tt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return tt
}

func TestDeclaringAFileParamMakesAnUploader(t *testing.T) {
	tt := buildRestTool(t, map[string]any{
		"url":          "https://api.example.com/v1/audio/transcriptions",
		"method":       "POST",
		"upload_param": "audio",
		"description":  "transcribe a clip",
	})
	if tt.UploadParam != "audio" {
		t.Errorf("upload param = %q", tt.UploadParam)
	}
	// The file is not a {placeholder} in the URL, so the parameter has to be
	// added for the author rather than left for them to remember.
	p, ok := tt.Params["audio"]
	if !ok {
		t.Fatal("the declared file parameter must appear in the tool's params")
	}
	if !strings.Contains(strings.ToLower(p.Description), "workspace") {
		t.Errorf("param description should say it takes a workspace filename: %q", p.Description)
	}
	var required bool
	for _, r := range tt.Required {
		if r == "audio" {
			required = true
		}
	}
	if !required {
		t.Error("a file parameter must be required — there is nothing to upload without it")
	}
}

func TestUploadDeclarationRoundTrips(t *testing.T) {
	// Read but never written (or the reverse) makes Configure drop the field on
	// the next save — the same trap the ComfyUI node map fell into.
	tt := buildRestTool(t, map[string]any{
		"url": "https://api.example.com/upload", "method": "POST",
		"upload_param": "doc", "upload_form_field": "image",
	})
	raw, _ := json.Marshal(tt)
	vals := restToolReadValues(Template{}, raw)
	if vals["upload_param"] != "doc" || vals["upload_form_field"] != "image" {
		t.Errorf("round trip lost the upload declaration: %v", vals)
	}
}

func TestNoFileParamLeavesAPlainAPITool(t *testing.T) {
	tt := buildRestTool(t, map[string]any{"url": "https://api.example.com/items/{id}", "method": "GET"})
	if tt.UploadParam != "" {
		t.Error("a tool that declares no file must not become an uploader")
	}
	if _, ok := tt.Params["id"]; !ok {
		t.Error("ordinary placeholder params must still be derived")
	}
}

func TestUploadSourceRefusesWhatItShould(t *testing.T) {
	ws := t.TempDir()
	sess := &ToolSession{WorkspaceDir: ws}
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("hello"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ResolveUploadSource(sess, "notes.txt")
	if err != nil {
		t.Fatalf("a workspace file must resolve: %v", err)
	}
	if string(got.Data) != "hello" || got.Name != "notes.txt" {
		t.Errorf("resolved = %+v", got)
	}
	// Unlike the image path, no format opinion — an upload tool takes anything.
	if _, err := ResolveUploadSource(sess, "notes.txt"); err != nil {
		t.Errorf("a non-image file must upload fine: %v", err)
	}

	for name, ref := range map[string]string{
		"absolute path":   "/etc/passwd",
		"parent escape":   "../outside.txt",
		"url":             "https://example.com/a.pdf",
		"missing file":    "nope.txt",
		"empty reference": "",
	} {
		if _, err := ResolveUploadSource(sess, ref); err == nil {
			t.Errorf("%s (%q) must be refused", name, ref)
		}
	}
}

func TestUploadSourceNeedsAWorkspace(t *testing.T) {
	// A tool running without a workspace has no safe root to resolve against,
	// and falling back to the process CWD would read anything on disk.
	if _, err := ResolveUploadSource(&ToolSession{}, "a.txt"); err == nil {
		t.Fatal("no workspace must refuse rather than resolve somewhere else")
	}
	if _, err := ResolveUploadSource(nil, "a.txt"); err == nil {
		t.Fatal("a nil session must refuse")
	}
}
