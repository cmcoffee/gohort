package orchestrate

// The agent editor draws a different page for each kind of agent: a new one
// gets the type presets and one form, a sub-agent drops what it cannot have
// (intake, publishing, sharing), and an existing agent gets the sections that
// only make sense once it exists: its pictures, what it reaches, where it
// came from, deleting it. Moving it is on the chat page's Agent menu, not
// here. A seed is framework-owned, so it is neither shared nor deleted from
// here.

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheAgentEditorDrawsTheSectionsEachKindHas(t *testing.T) {
	// A shape that exists: a ShapeID naming none is dropped on save.
	shape := ""
	for _, a := range loadArchetypes() {
		if _, ok := shapeBaseRecord(a.Slug); ok {
			shape = a.Slug
			break
		}
	}
	if shape == "" {
		t.Fatal("precondition: no shape ships a record")
	}
	app, udb := accessFixture(t, AgentRecord{ID: "a-main", Name: "Main", ShapeID: shape})
	for _, a := range []AgentRecord{
		{ID: "a-child", Name: "Child", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-main"},
		// Somewhere it could move to, so a missing Move is not for want of one.
		{ID: "a-other", Name: "Other", Owner: "alice", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	render := func(url, id string) string {
		w := httptest.NewRecorder()
		app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", url, nil), "alice"), "alice", udb, id)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s answered %d (cache %q)", url, w.Code, w.Header().Get("Cache-Control"))
		}
		return w.Body.String()
	}
	title := func(s string) string { return `"title":"` + s + `"` }
	for _, c := range []struct {
		url, id   string
		has, lack []string
	}{
		{"/agent/new", "", []string{title("New agent"), title("Agent"), `"templates":`},
			[]string{title("Picture library"), title("Delete agent")}},
		{"/agent/new?owned_by=a-main", "", []string{title("New sub-agent"), title("Sub-agent"), `"field":"owned_by","type":"hidden","default":"a-main"`},
			[]string{title("Intake form"), `"templates":`}},
		{"/agent/a-main", "a-main", []string{title("Edit agent"), title("Intake form"), title("Picture library"),
			title("What it reaches"), "Built from the ", title("Delete agent"),
			`"back_url":"..?agent=a-main"`},
			[]string{`"templates":`, title("Move")}},
		{"/agent/a-child", "a-child", []string{title("Edit sub-agent"), title("Picture library"), title("Delete agent")},
			[]string{title("Intake form"), title("What it reaches")}},
		{"/agent/seed-builder", "seed-builder", []string{title("Edit agent")},
			[]string{title("What it reaches"), title("Delete agent"), "Built from the"}},
	} {
		page := render(c.url, c.id)
		for _, want := range c.has {
			if !strings.Contains(page, want) {
				t.Errorf("%s: the editor is missing %s", c.url, want)
			}
		}
		for _, not := range c.lack {
			if strings.Contains(page, not) {
				t.Errorf("%s: the editor should not have %s", c.url, not)
			}
		}
	}
}
