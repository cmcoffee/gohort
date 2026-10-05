package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every app agent is listed, hidden ones included, under its app. One the
// user changed says what changed and offers Reset, which puts it back as the
// app registered it and refuses a second time.
func TestAppAgentsAreListedAndReset(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-listed", Name: "Listed", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	find := func() appAgentRow {
		for _, r := range appAgentRows(udb) {
			if r.ID == "app-test-listed" {
				return r
			}
		}
		t.Fatal("a hidden app agent is not listed")
		return appAgentRow{}
	}
	if r := find(); r.Customized || r.Section != "Zz Test" || r.Name != "Listed" {
		t.Fatalf("row = %+v", r)
	}
	rec, ok := loadAgent(udb, "app-test-listed")
	if !ok {
		t.Fatal("the app agent does not load")
	}
	rec.MaxWorkerRounds = 40
	rec.Owner = "u"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	if r := find(); !r.Customized || !strings.Contains(r.Changed, "max worker rounds") {
		t.Fatalf("customized row = %+v", r)
	}
	if err := resetAppAgent(udb, "app-test-listed"); err != nil {
		t.Fatal(err)
	}
	if back, _ := loadAgent(udb, "app-test-listed"); back.MaxWorkerRounds != 0 {
		t.Fatalf("after reset, max worker rounds = %d", back.MaxWorkerRounds)
	}
	if r := find(); r.Customized {
		t.Fatalf("after reset = %+v", r)
	}
	if err := resetAppAgent(udb, "app-test-listed"); err == nil {
		t.Fatal("a reset at defaults did not say so")
	}
	if err := resetAppAgent(udb, "seed-builder"); err == nil {
		t.Fatal("reset reached an agent that is not an app agent")
	}
}

// The editor's back arrow goes only to a path on this server, and an app's
// chat names the agent's editor with its own way back.
func TestAppAgentSettingsLink(t *testing.T) {
	for b, want := range map[string]bool{"/scribe": true, "//evil.example": false, "https://evil.example": false, "/\\evil": false, "": false} {
		if localBackPath(b) != want {
			t.Errorf("localBackPath(%q) = %v", b, !want)
		}
	}
	p := AppChat{Prefix: "chat/", AgentID: "app-guides-author", Back: "/scribe"}.Panel(ui.AgentLoopPanel{})
	if len(p.Actions) != 1 || p.Actions[0].Method != "redirect" || p.Actions[0].URL != "/orchestrate/agent/app-guides-author?back=%2Fscribe" {
		t.Fatalf("actions = %+v", p.Actions)
	}
	if p := (AppChat{Prefix: "chat/"}).Panel(ui.AgentLoopPanel{}); len(p.Actions) != 0 {
		t.Fatalf("no agent, yet actions = %+v", p.Actions)
	}
}
