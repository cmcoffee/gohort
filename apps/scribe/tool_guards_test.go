package scribe

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The guards orchestrate applies to a tool read what the tool declares. A
// tool that declares nothing is never fenced or scanned, is not dropped from
// a Private turn, and does not count as having changed anything.
func TestScribeToolsDeclareWhatTheyDo(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	T := &Scribe{AppCore: AppCore{DB: root}}
	orch := &orchestrate.OrchestrateApp{AppCore: AppCore{DB: root}}
	tools := map[string]Tool{}
	for _, td := range T.coauthorTools(coauthorScope{Ctx: context.Background(), UDB: udb, Orch: orch, User: "u", CanEdit: true}) {
		tools[td.Tool.Name] = td.Tool
	}
	has := func(tl Tool, c Capability) bool {
		for _, x := range tl.Caps {
			if x == c {
				return true
			}
		}
		return false
	}
	if !has(tools["research"], CapNetwork) {
		t.Error("research fetches the web but does not say so: Private mode keeps it and its pages are not fenced")
	}
	for _, n := range []string{"search_knowledge", "pull_reference"} {
		if !tools[n].FetchesExternal && !has(tools[n], CapNetwork) {
			t.Errorf("%s returns documents but is not fenced as outside content", n)
		}
	}
	for _, n := range []string{"add_section", "edit_section", "draft_section", "delete_section", "rename_section", "move_section"} {
		if !has(tools[n], CapWrite) {
			t.Errorf("%s changes the document but does not declare a write", n)
		}
	}
	src, _ := os.ReadFile("coauthor.go")
	if strings.Contains(string(src), "RunAgentSync(context.Background()") {
		t.Error("research runs detached from the turn: Private mode and Stop do not reach it")
	}
}
