package orchestrate

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A dated weekday as the resolver writes it: "Monday (Mon Oct 12, 2026)".
var datedWeekday = regexp.MustCompile(`Monday \(Mon [A-Z][a-z]{2} \d{1,2}, ` + strconv.Itoa(time.Now().Year()) + `|` + strconv.Itoa(time.Now().Year()+1) + `\)`)

// Each place an agent writes for later dates its relative days, and hands
// back a phrase that could mean two days without saving anything.
func TestWhatAnAgentKeepsGetsItsDays(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "NEW", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	db := &DBase{Store: kvlite.MemStore()}
	ct := driftTurn(db)
	ct.app = app
	// "Monday" is ambiguous on a Monday; every other day it resolves ahead.
	monday := time.Now().In(UserLocation("u")).Weekday() == time.Monday

	_, err := ct.rememberToolDef().Handler(context.Background(), map[string]any{"pin": true, "content": "Craig's free ARC Raiders window closes Monday."})
	facts := ListMemoryFacts(db, factsNamespace("ag"))
	if monday {
		if err == nil || len(facts) != 0 {
			t.Fatalf("on a Monday, \"closes Monday\" was saved: %v", err)
		}
	} else if err != nil || len(facts) != 1 || !datedWeekday.MatchString(facts[0].Note) {
		t.Fatalf("the pinned note was not dated: err=%v facts=%+v", err, facts)
	}

	_, err = ct.rememberToolDef().Handler(context.Background(), map[string]any{"pin": true, "content": "Lunch with Dana next Tuesday."})
	if err == nil || !strings.Contains(err.Error(), "NOT saved") || !strings.Contains(err.Error(), "\"next Tuesday\" could be") || !strings.Contains(err.Error(), "Today is") {
		t.Fatalf("a phrase with two readings was not handed back: %v", err)
	}
	if got := len(ListMemoryFacts(db, factsNamespace("ag"))); got != len(facts) {
		t.Error("the refused note was saved anyway")
	}

	// Working notes and cortex pointers the same way.
	notes := ct.updateNotesToolDef()
	if _, err := notes.Handler(context.Background(), map[string]any{"text": "Report due tomorrow."}); err != nil {
		t.Fatal(err)
	}
	if got := LoadOperatingNotes(db, factsNamespace("ag")).Text; !strings.Contains(got, "tomorrow (") {
		t.Errorf("working notes were not dated: %q", got)
	}
	if _, err := notes.Handler(context.Background(), map[string]any{"text": "Call back next Friday."}); err == nil {
		t.Error("working notes took a phrase with two readings")
	}
	for _, td := range cortexDeliverableTools(db, "ag", ct.datedText) {
		if td.Tool.Name != "note_to_cortex" {
			continue
		}
		if out, err := td.Handler(context.Background(), map[string]any{"note": "Texted Mom her flight info yesterday."}); err != nil || !strings.Contains(out, "yesterday (") {
			t.Errorf("the cortex pointer was not dated: %q %v", out, err)
		}
	}
}

// A board post is dated before followers read it for days.
func TestABulletinPostGetsItsDays(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saveBulletin(db, bulletinBoard{Name: "radar", Posters: []string{"ag"}})
	ct := driftTurn(db)
	def, ok := ct.bulletinBoardToolDef()
	if !ok {
		t.Skip("this agent is offered no board tool")
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "radar", "action": "post", "text": "Fog clears tomorrow."}); err != nil {
		t.Fatal(err)
	}
	if b, _ := loadBulletin(db, "radar"); !strings.Contains(b.Text, "tomorrow (") {
		t.Errorf("the post was not dated: %q", b.Text)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "radar", "action": "post", "text": "Market next Saturday."}); err == nil {
		t.Error("a post with two readings went up")
	}
}
