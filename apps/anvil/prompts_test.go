package anvil

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Saved prompts are tidied on save: names lower case without the slash,
// blank rows dropped; a bad name, an empty prompt or a name used twice is
// refused.
func TestSavedPromptsAreTidiedAndChecked(t *testing.T) {
	got, err := validatePrompts([]SavedPrompt{{Name: " /Lint ", Prompt: " Run the linter. "}, {}, {Name: "ship_it", Prompt: "Commit."}})
	if err != nil || len(got) != 2 || got[0] != (SavedPrompt{"lint", "Run the linter."}) || got[1].Name != "ship_it" {
		t.Fatalf("tidied %+v, %v", got, err)
	}
	for _, bad := range [][]SavedPrompt{
		{{Name: "two words", Prompt: "x"}},
		{{Name: "lint", Prompt: "  "}},
		{{Name: "lint", Prompt: "a"}, {Name: "/LINT", Prompt: "b"}},
		{{Name: "long", Prompt: strings.Repeat("x", maxPromptChars+1)}},
	} {
		if _, err := validatePrompts(bad); err == nil {
			t.Errorf("kept %+v", bad)
		}
	}
}

// A project offers its own prompts and the built-ins it has not replaced,
// and a project it cannot find offers nothing.
func TestAProjectOffersItsSavedPrompts(t *testing.T) {
	T, serve := bundleRoutes(t)
	udb := UserDB(T.DB, "alice")
	T.saveProject(udb, Project{ID: "p1", Name: "P", Kind: KindClone, Owner: "alice", Source: sourceBundle, BundleName: "p.bundle",
		Prompts: []SavedPrompt{{Name: "test", Prompt: "Run make check."}, {Name: "lint", Prompt: "Run the linter."}}})
	list := func(user, project string) map[string]string {
		w := serve(user, "GET", "/api/commands?project_id="+project, nil, "")
		var cmds []slashCommand
		if err := json.Unmarshal(w.Body.Bytes(), &cmds); err != nil {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		out := map[string]string{}
		for _, c := range cmds {
			out[c.Name] = c.Prompt
		}
		return out
	}
	got := list("alice", "p1")
	if len(got) != 3 || got["test"] != "Run make check." || got["lint"] == "" || !strings.HasPrefix(got["review"], "Review the changes") {
		t.Errorf("alice is offered %v", got)
	}
	if got := list("alice", "nope"); len(got) != 0 {
		t.Errorf("no project offers %v", got)
	}
}
