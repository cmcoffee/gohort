package scribe

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/oddjob/apps/orchestrate"
	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A section a model writes is stored without the tool-call markup it leaked
// into the text; one that is nothing but markup is refused, not saved blank.
func TestModelWrittenSectionsAreCleaned(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	saveGuide(udb, Guide{ID: "g1", Owner: "u", Title: "G", Sections: []Section{{ID: "s1", Title: "Setup", Markdown: "old", Order: 1}}})
	udb.Set(activeTable, "current", "g1")
	T := &Scribe{AppCore: AppCore{DB: root}}
	orch := &orchestrate.OrchestrateApp{AppCore: AppCore{DB: root}}
	tools := map[string]AgentToolDef{}
	for _, td := range T.coauthorTools(coauthorScope{Ctx: context.Background(), UDB: udb, Orch: orch, User: "u", CanEdit: true}) {
		tools[td.Tool.Name] = td
	}
	leaked := "Run the installer.\n\n<tool_call>\n{\"name\": \"list_sections\", \"arguments\": {}}\n</tool_call>"
	run := func(name string, args map[string]any) error {
		td, ok := tools[name]
		if !ok {
			t.Fatalf("no %s tool", name)
		}
		_, err := td.Handler(context.Background(), args)
		return err
	}
	if err := run("add_section", map[string]any{"section_title": "Install", "markdown": leaked}); err != nil {
		t.Fatal(err)
	}
	if err := run("edit_section", map[string]any{"section_title": "Setup", "markdown": leaked}); err != nil {
		t.Fatal(err)
	}
	g, _ := loadGuide(udb, "g1")
	for _, s := range g.Sections {
		if strings.Contains(s.Markdown, "tool_call") || s.Markdown != "Run the installer." {
			t.Fatalf("section %q = %q", s.Title, s.Markdown)
		}
	}
	if err := run("edit_section", map[string]any{"section_title": "Setup", "markdown": "<tool_call>{}</tool_call>"}); err == nil {
		t.Fatal("a body of nothing but markup blanked the section")
	}
}
