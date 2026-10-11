package orchestrate

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// paneFrames reads the html_artifact blocks a turn sent to the pane.
func paneFrames(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["type"] == "html_artifact" {
			out = append(out, m)
		}
	}
	return out
}

const testPage = "<!doctype html><html><body><h1>Agent controls</h1><p>Memory, rules, knowledge, schedules.</p></body></html>"

// The failure from a live session: a later turn re-sent its earlier call
// with the record's note as the page, and the pane showed the note. Then,
// seeing only notes in its history, the agent decided its real pages had
// been truncated and wrote them out twice more.
func TestShowHTMLRefusesItsOwnRecordAsAPage(t *testing.T) {
	rec := httptest.NewRecorder()
	turn := &chatTurn{session: &ChatSession{ID: "s1"}, sse: newSSEWriter(rec)}
	tool := turn.showHTMLToolDef()

	args := map[string]any{"title": "Agent controls", "html": testPage}
	out, err := tool.Handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, fmt.Sprintf("full %d-byte document", len(testPage))) {
		t.Errorf("the result does not say the whole page arrived: %q", out)
	}
	note, _ := args["html"].(string)
	if !strings.Contains(note, "NOT truncated") || !strings.Contains(note, "just this id") {
		t.Errorf("the record's note can still be read as a cut-off push: %q", note)
	}
	id := paneFrames(t, rec)[0]["id"].(string)

	// Both wordings of the note, copied back as the next page, are refused,
	// and nothing reaches the pane.
	old := `(10450-byte HTML document: stored as session artifact "` + id + `")`
	for _, html := range []string{note, old} {
		_, err := tool.Handler(context.Background(), map[string]any{"title": "Agent controls", "id": id, "html": html})
		if err == nil || !strings.Contains(err.Error(), "only its id") {
			t.Errorf("a record note was accepted as a page: %q (%v)", html, err)
		}
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"title": "x", "html": "just some words"}); err == nil {
		t.Error("text with no markup was accepted as a document")
	}
	if n := len(paneFrames(t, rec)); n != 1 {
		t.Errorf("refused calls reached the pane: %d frames", n)
	}

	// What the agent meant: show it again. The id alone does that, with the
	// page as it was.
	out, err = tool.Handler(context.Background(), map[string]any{"title": "Agent controls", "id": id})
	if err != nil {
		t.Fatal(err)
	}
	frames := paneFrames(t, rec)
	if len(frames) != 2 || frames[1]["html"] != testPage || frames[1]["open"] != true {
		t.Errorf("the id did not reopen the page as it was: %+v", frames)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("result: %q", out)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"id": "artifact-nope"}); err == nil {
		t.Error("an id from nowhere was accepted")
	}
}

// A picture goes into an artifact by name: the page says <img src="chart">,
// the call says images=["chart=<handle>"], and what reaches the pane, and the
// stored artifact, carries the picture embedded. The model never writes the
// image data; a name the page never uses is refused, as is a handle that
// resolves to nothing.
func TestAnArtifactEmbedsItsPicturesByName(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chart.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{WorkspaceDir: dir}
	ctx := sess.ContextWithSession(context.Background())

	rec := httptest.NewRecorder()
	turn := &chatTurn{session: &ChatSession{ID: "s1"}, sse: newSSEWriter(rec)}
	tool := turn.showHTMLToolDef()
	page := `<!doctype html><html><head><style>.hero{background:url(chart)}</style></head><body><h1>Sales</h1><img src="chart" alt="Sales"></body></html>`
	out, err := tool.Handler(ctx, map[string]any{"title": "Sales", "html": page, "images": []any{"chart=chart.png"}})
	if err != nil {
		t.Fatalf("show_html with a picture: %v", err)
	}
	if !strings.Contains(out, "1 picture(s) embedded") {
		t.Errorf("the result should say the picture was embedded: %s", out)
	}
	frames := paneFrames(t, rec)
	if len(frames) != 1 {
		t.Fatalf("frames: %d", len(frames))
	}
	shown, _ := frames[0]["html"].(string)
	if strings.Count(shown, "data:image/png;base64,") != 2 || strings.Contains(shown, `src="chart"`) {
		t.Errorf("both uses of the name should carry the picture: %.300s", shown)
	}
	if blocks := turn.session.UIBlocks; len(blocks) != 1 || !strings.Contains(blocks[0].HTML, "data:image/png;base64,") {
		t.Error("the stored artifact does not carry the picture, so a reopened chat would lose it")
	}

	if _, err := tool.Handler(ctx, map[string]any{"title": "Sales", "html": page, "images": []any{"logo=chart.png", "chart=chart.png"}}); err == nil || !strings.Contains(err.Error(), "never uses it") {
		t.Errorf("a picture the page never uses should be refused: %v", err)
	}
	if _, err := tool.Handler(ctx, map[string]any{"title": "Sales", "html": page, "images": []any{"chart=missing.png"}}); err == nil {
		t.Error("a handle that resolves to nothing should be refused")
	}
	if _, err := tool.Handler(ctx, map[string]any{"title": "Sales", "html": page, "images": []any{"chart"}}); err == nil || !strings.Contains(err.Error(), "name=handle") {
		t.Errorf("an entry with no handle should say the form: %v", err)
	}
}
