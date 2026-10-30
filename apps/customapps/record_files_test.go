package customapps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appassets"
)

// songsWithTakes is a co-authored workbench whose songs keep their takes.
func songsWithTakes(t *testing.T) (*CustomApps, AppSpec) {
	t.Helper()
	prev := appassets.RecordFilesDir()
	appassets.SetRecordFilesDir(t.TempDir())
	t.Cleanup(func() { appassets.SetRecordFilesDir(prev) })
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "songs", Name: "Songs", Owner: "u", RecordKey: "id", BodyField: "lyrics", AgentID: "lyricist",
		Sections: json.RawMessage(`[{"kind":"workbench","editable":true,"item_noun":"song","files_field":"takes","files_label":"Takes"}]`)}
	w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/records",
		strings.NewReader(`{"id":"s1","title":"Money in the Cloud","lyrics":"la"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	return T, spec
}

// keepTake runs keep_file as the agent would, on a file in its folder.
func keepTake(t *testing.T, T *CustomApps, spec AppSpec, open, file, title string) (string, error) {
	t.Helper()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, file), []byte("ID3\x04\x00\x00\x00\x00\x00\x00"+strings.Repeat("a", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := (&ToolSession{WorkspaceDir: ws}).ContextWithSession(context.Background())
	tool := T.keepFileTool(T.recordBase(spec, "u"), spec, "u", "u", func() string { return open })
	return tool.Handler(ctx, map[string]any{"file": file, "title": title})
}

func takesOf(t *testing.T, T *CustomApps, spec AppSpec) []appassets.RecordFile {
	t.Helper()
	w := appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/songs/record?id=s1", nil))
	var rec map[string]any
	json.Unmarshal(w.Body.Bytes(), &rec)
	return recordFiles(rec, "takes")
}

// The agent keeps a take on the open song: it is listed on the song, and the
// song's page plays it, seeking included.
func TestTheAgentKeepsATakeOnTheOpenSong(t *testing.T) {
	T, spec := songsWithTakes(t)
	out, err := keepTake(t, T, spec, "s1", "take.mp3", "Slower take")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"Slower take"`) || !strings.Contains(out, `"Money in the Cloud"`) || !strings.Contains(out, "Takes") {
		t.Errorf("the tool says %q", out)
	}
	takes := takesOf(t, T, spec)
	if len(takes) != 1 || takes[0].Name != "take.mp3" || takes[0].Title != "Slower take" || takes[0].Type != "audio/mpeg" {
		t.Fatalf("takes %+v", takes)
	}
	r := httptest.NewRequest("GET", "/apps/songs/file?id=s1&name=take.mp3", nil)
	r.Header.Set("Range", "bytes=0-2")
	w := appAs(t, T, spec, "u", r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "ID3" || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("a range of the take: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("a kept file is served sandboxed")
	}
	if w := appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/songs/file?id=s1&name=other.mp3", nil)); w.Code != http.StatusNotFound {
		t.Errorf("a file the song does not list: %d", w.Code)
	}
	// Someone the app is shared with has records of their own, not these.
	if w := appAs(t, T, spec, "v", httptest.NewRequest("GET", "/apps/songs/file?id=s1&name=take.mp3", nil)); w.Code != http.StatusNotFound {
		t.Errorf("another person reached the take: %d", w.Code)
	}
}

// keep_file needs an open song and a file that is there.
func TestKeepFileSaysWhatIsMissing(t *testing.T) {
	T, spec := songsWithTakes(t)
	if _, err := keepTake(t, T, spec, "", "take.mp3", ""); err == nil || !strings.Contains(err.Error(), "no song is open") {
		t.Errorf("nothing open: %v", err)
	}
	if _, err := keepTake(t, T, spec, "gone", "take.mp3", ""); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Errorf("a song that is not there: %v", err)
	}
	tool := T.keepFileTool(T.recordBase(spec, "u"), spec, "u", "u", func() string { return "s1" })
	ctx := (&ToolSession{WorkspaceDir: t.TempDir()}).ContextWithSession(context.Background())
	if _, err := tool.Handler(ctx, map[string]any{"file": "nothing.mp3"}); err == nil {
		t.Error("a file that is not in the folder was kept")
	}
	if strings.Contains(tool.Tool.Description, "—") {
		t.Error("no em-dash in a tool description")
	}
}

// The list of takes is the server's: neither a whole-record save nor the
// editor's merge changes it, whatever they send.
func TestSavesKeepTheTakesList(t *testing.T) {
	T, spec := songsWithTakes(t)
	if _, err := keepTake(t, T, spec, "s1", "take.mp3", "One"); err != nil {
		t.Fatal(err)
	}
	appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/records",
		strings.NewReader(`{"id":"s1","title":"Money in the Cloud","lyrics":"new"}`)))
	if takes := takesOf(t, T, spec); len(takes) != 1 {
		t.Errorf("a form's save dropped the takes: %+v", takes)
	}
	appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/record?id=s1",
		strings.NewReader(`{"id":"s1","lyrics":"newer","takes":[{"name":"../../etc/passwd.mp3"}]}`)))
	if takes := takesOf(t, T, spec); len(takes) != 1 || takes[0].Name != "take.mp3" {
		t.Errorf("an editor save rewrote the takes: %+v", takes)
	}
	w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/records",
		strings.NewReader(`{"id":"s2","title":"New","takes":[{"name":"x.mp3"}]}`)))
	var rec map[string]any
	json.Unmarshal(w.Body.Bytes(), &rec)
	if _, has := rec["takes"]; has {
		t.Errorf("a new song arrived with takes: %v", rec)
	}
}

// Remove takes a take off the song and the disk; deleting the song takes
// the rest.
func TestRemovingATakeAndDeletingTheSong(t *testing.T) {
	T, spec := songsWithTakes(t)
	keepTake(t, T, spec, "s1", "take.mp3", "One")
	keepTake(t, T, spec, "s1", "take.mp3", "Two")
	takes := takesOf(t, T, spec)
	if len(takes) != 2 || takes[1].Name != "take-2.mp3" {
		t.Fatalf("takes %+v", takes)
	}
	one, _, _ := appassets.RecordFilePath("u", "songs", "u", "s1", "take.mp3")
	two, _, _ := appassets.RecordFilePath("u", "songs", "u", "s1", "take-2.mp3")
	if w := appAs(t, T, spec, "u", httptest.NewRequest("DELETE", "/apps/songs/file?id=s1&name=take.mp3", nil)); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if takes := takesOf(t, T, spec); len(takes) != 1 || takes[0].Title != "Two" {
		t.Errorf("after the remove: %+v", takes)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Error("the removed take is still on disk")
	}
	if w := appAs(t, T, spec, "u", httptest.NewRequest("DELETE", "/apps/songs/record?id=s1", nil)); w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	if _, err := os.Stat(two); !os.IsNotExist(err) {
		t.Error("the deleted song's take is still on disk")
	}
}

// Only a workbench that names a files field gives its agent keep_file.
func TestKeepFileComesWithAFilesField(t *testing.T) {
	if f, label, noun := workbenchFiles(AppSpec{BodyField: "lyrics", Sections: json.RawMessage(`[{"kind":"workbench","files_field":"takes"}]`)}); f != "takes" || label != "Files" || noun != "document" {
		t.Errorf("defaults: %q %q %q", f, label, noun)
	}
	if f, _, _ := workbenchFiles(AppSpec{BodyField: "lyrics", Sections: json.RawMessage(`[{"kind":"workbench"}]`)}); f != "" {
		t.Errorf("no files field: %q", f)
	}
}
