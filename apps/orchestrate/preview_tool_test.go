package orchestrate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
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
