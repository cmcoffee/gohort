package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func pageApp(agent, html string) AppSpec {
	secs, _ := json.Marshal([]any{map[string]any{"kind": "html", "html": html}})
	return AppSpec{AgentID: agent, Sections: secs}
}

// A page that writes its own editor for a document the person writes with
// the agent is held to the editable workbench; a page with a form's
// textarea, or no agent to write with, is not.
func TestAnAppPageWithItsOwnEditor(t *testing.T) {
	for name, c := range map[string]struct {
		spec AppSpec
		want bool
	}{
		"an editable div with an agent": {pageApp("lyricist", `<div class="editor" id="editor" contenteditable="true"></div>`), true},
		"made editable from script":     {pageApp("lyricist", `<div id=ed></div><script>ed.contentEditable = true;</script>`), true},
		"a bare attribute":              {pageApp("lyricist", `<div contenteditable>x</div>`), true},
		"a textarea read for its selection, asking the agent": {pageApp("",
			`<textarea id=t></textarea><script>t.addEventListener('mouseup',()=>{const s=t.value.slice(t.selectionStart,t.selectionEnd); app.ask('Tighten: '+s)})</script>`), true},
		"a form's textarea":                {pageApp("helper", `<form><textarea name=notes></textarea></form><script>app.ask('x')</script>`), false},
		"an editable div with no agent":    {pageApp("", `<div contenteditable="true"></div>`), false},
		"a key guard asking about editing": {pageApp("helper", `<script>if (a.isContentEditable || a.tagName === 'INPUT') return;</script>`), false},
		"made not editable":                {pageApp("helper", `<div contenteditable="false"></div>`), false},
		"an editable workbench":            {AppSpec{AgentID: "lyricist", Sections: []byte(`[{"kind":"workbench","editable":true}]`)}, false},
	} {
		if got := appOwnEditorProblem(c.spec) != ""; got != c.want {
			t.Errorf("%s: objected %v, want %v", name, got, c.want)
		}
	}
	asked := pageApp("lyricist", `<div contenteditable="true"></div>`)
	asked.Notes = "Own editor: the person wants a typewriter look the workbench cannot give."
	if appOwnEditorProblem(asked) != "" {
		t.Error("an editor of its own the person asked for was objected to")
	}
	p := appOwnEditorProblem(pageApp("lyricist", `<div contenteditable="true"></div>`))
	for _, want := range []string{"editable: true", "workbench", "own editor: <why>"} {
		if !strings.Contains(p, want) {
			t.Errorf("the objection does not say %q: %s", want, p)
		}
	}
	if strings.Contains(p, "—") {
		t.Error("prompt text carries an em dash")
	}
}

// A create of such a page is refused, with what to build instead, and
// nothing is saved.
func TestACreateWithItsOwnEditorIsRefused(t *testing.T) {
	pinRootDB(t)
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "seed-builder"}, session: &ChatSession{ID: "s1"}}
	sections := []any{map[string]any{"kind": "html", "html": `<!DOCTYPE html><div contenteditable="true"></div><script>app.ask('rhyme it')</script>`}}
	_, err := turn.appDefCreateOrUpdate(map[string]any{"name": "Lyrics", "sections": sections}, false)
	if err == nil || !strings.Contains(err.Error(), "NOT CREATED") || !strings.Contains(err.Error(), "editable: true") {
		t.Fatalf("a page with its own editor: %v", err)
	}
	if _, ok := LoadAppSpec("u", "lyrics"); ok {
		t.Fatal("a refused create was saved")
	}
}
