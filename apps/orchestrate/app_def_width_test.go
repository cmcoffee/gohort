package orchestrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

const wholeDoc = `<!DOCTYPE html><html><head><style>body{margin:0}</style></head><body><canvas></canvas></body></html>`

// A page made only of html sections is an app with its own layout: it is full
// width unless the author keeps it narrow. A page with typed sections keeps
// the centered column unless it asks for full width.
func TestAnHTMLAppIsFullWidthUnlessKeptNarrow(t *testing.T) {
	html := map[string]any{"kind": "html", "html": wholeDoc}
	fragment := map[string]any{"kind": "html", "html": "<p>hello</p>"}
	table := map[string]any{"kind": "table", "columns": []any{map[string]any{"field": "name"}}}
	for _, c := range []struct {
		name string
		spec AppSpec
		secs []any
		want string
	}{
		{"one whole document", AppSpec{}, []any{html}, "100%"},
		{"html fragments only", AppSpec{}, []any{fragment, fragment}, "100%"},
		{"kept narrow", AppSpec{Narrow: true}, []any{html}, "900px"},
		{"full_width wins over narrow", AppSpec{Narrow: true, FullWidth: true}, []any{html}, "100%"},
		{"html beside a table", AppSpec{}, []any{fragment, table}, "900px"},
		{"a table that asked for full width", AppSpec{FullWidth: true}, []any{table}, "100%"},
	} {
		c.spec.Slug, c.spec.Name, c.spec.RecordKey = "x", "X", "id"
		page, err := buildAppPage(c.spec, c.secs)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if page.MaxWidth != c.want {
			t.Errorf("%s: width %q, want %q", c.name, page.MaxWidth, c.want)
		}
	}
}

// A whole document that is the page's only section, with no height, fills the
// window on any screen. Given a height, or sharing the page, it keeps the box
// it was given.
func TestTheOnlyWholeDocumentFillsTheWindow(t *testing.T) {
	frame := func(secs ...any) []ui.Frame {
		page, err := buildAppPage(AppSpec{Slug: "x", Name: "X", RecordKey: "id"}, secs)
		if err != nil {
			t.Fatal(err)
		}
		var out []ui.Frame
		for _, s := range page.Sections {
			if f, ok := s.Body.(ui.Frame); ok {
				out = append(out, f)
			}
		}
		return out
	}
	if f := frame(map[string]any{"kind": "html", "html": wholeDoc}); len(f) != 1 || !f[0].Fill {
		t.Errorf("the only document does not fill the window: %+v", f)
	}
	if f := frame(map[string]any{"kind": "html", "html": wholeDoc, "height": "640px"}); len(f) != 1 || f[0].Fill {
		t.Errorf("a document given a height filled anyway: %+v", f)
	}
	if f := frame(map[string]any{"kind": "html", "html": wholeDoc}, map[string]any{"kind": "html", "html": wholeDoc}); len(f) != 2 || f[0].Fill || f[1].Fill {
		t.Errorf("two documents on one page each filled the window: %+v", f)
	}
	// The flag reaches the browser.
	page, _ := buildAppPage(AppSpec{Slug: "x", Name: "X", RecordKey: "id"}, []any{map[string]any{"kind": "html", "html": wholeDoc}})
	if blob, _ := page.ConfigJSON(); !json.Valid(blob) || !strings.Contains(string(blob), `"fill":true`) {
		t.Errorf("the page config does not carry fill: %s", blob)
	}
}

// narrow is a setting like full_width: saved when given, kept when an update
// leaves it out, read back by get, and carried through a folder.
func TestNarrowIsKeptAndCarried(t *testing.T) {
	turn, ws := folderTestTurn(t)
	if _, err := turn.appDefCheckout(map[string]any{"name": "Wx"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ws, "wx.app")
	os.WriteFile(filepath.Join(dir, "page.html"), []byte(wholeDoc), 0o644)
	manifest := `{"name":"Wx","slug":"wx","narrow":true,"sections":[{"id":"page","kind":"html","html_file":"page.html"}]}`
	os.WriteFile(filepath.Join(dir, "app.json"), []byte(manifest), 0o644)
	args, m, err := turn.appFolderRead(dir, "wx.app")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Narrow || args["narrow"] != true {
		t.Fatalf("a folder's narrow did not reach the save: manifest=%v args=%v", m.Narrow, args["narrow"])
	}
}

// Fingerprints of apps that never set narrow are what they were before it
// existed, or every app's folder would read as edited live and refuse to
// publish.
func TestAnAppWithoutNarrowKeepsItsFingerprint(t *testing.T) {
	spec := AppSpec{Name: "Wx", Slug: "wx", Desc: "weather", RecordKey: "id", FullWidth: true,
		Sections: json.RawMessage(`[{"kind":"html","html":"<p>x</p>"}]`)}
	b, _ := json.Marshal([]any{spec.Name, spec.Desc, spec.Notes, spec.RecordKey, spec.AgentID, spec.PipelineID,
		spec.FullWidth, spec.AskDailyUSD, spec.AskUserDailyUSD, spec.SharedCollections, spec.Settings,
		spec.Sections, spec.DataSources, spec.Actions, spec.Libraries})
	sum := sha256.Sum256(b)
	if got := appLiveFingerprint(spec); got != hex.EncodeToString(sum[:]) {
		t.Error("an app that never set narrow has a new fingerprint")
	}
	narrow := spec
	narrow.Narrow = true
	if appLiveFingerprint(narrow) == appLiveFingerprint(spec) {
		t.Error("setting narrow does not count as a change")
	}
}
