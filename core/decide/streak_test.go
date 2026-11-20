package decide

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checkstreak"
	"github.com/cmcoffee/gohort/core/llmcalls"
	"github.com/cmcoffee/gohort/core/notices"
)

// reply is a chat that answers content as model.
func reply(model, content string) VerdictChat {
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		return &Response{Content: content, Model: model, StopReason: "refusal", StopDetail: "category cyber"}, nil
	}
}

// A check whose model keeps giving no verdict tells each admin, on their
// bell, once per run: which check, which model, and how the last reply
// ended. A verdict from that model ends the run.
func TestAVerdictThatKeepsFailingTellsTheAdmins(t *testing.T) {
	adb := privateWorld(t)
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	v := testVerdict
	v.Key = "memory.gate" // a registered check, so the notice has its name
	bad, good := reply("model-a", ""), reply("model-a", `{"subject": "x", "why": "y", "answer": "on"}`)
	ctx := context.Background()
	for i := 1; i < checkstreak.After; i++ {
		v.Judge(ctx, bad, "state")
	}
	if len(notices.List(RootDB, "root")) != 0 {
		t.Fatal("told before a run")
	}
	v.Judge(ctx, bad, "state")
	list := notices.List(RootDB, "root")
	if len(list) != 1 {
		t.Fatalf("the admin's notices: %+v", list)
	}
	n := list[0]
	for _, want := range []string{"Memory gate", "model-a"} {
		if !strings.Contains(n.Title, want) {
			t.Errorf("the title lacks %q: %q", want, n.Title)
		}
	}
	for _, want := range []string{"memory.gate", "The last reply was empty (stopped: refusal, category cyber).", "Check tests"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("the body lacks %q: %q", want, n.Body)
		}
	}
	if len(notices.List(RootDB, "bob")) != 0 {
		t.Error("a non-admin was told")
	}
	// A verdict ends the run; the next run is told again (folded into the
	// same notice, counted).
	if _, err := v.Judge(ctx, good, "state"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < checkstreak.After; i++ {
		v.Judge(ctx, bad, "state")
	}
	if list := notices.List(RootDB, "root"); len(list) != 1 || list[0].Count != 2 {
		t.Errorf("the second run: %+v", list)
	}
}

// The fingerprint stand-in, and a check test's own asks, are no model
// failing live: neither makes a run.
func TestAStandInOrACheckTestMakesNoRun(t *testing.T) {
	adb := privateWorld(t)
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	v := testVerdict
	v.Key = "test.streak.standin"
	watched, _ := llmcalls.Watch(context.Background())
	for i := 0; i < checkstreak.After+1; i++ {
		v.Judge(context.Background(), reply(StandInModel, ""), "state")
		v.Judge(watched, reply("model-b", ""), "state")
	}
	if list := notices.List(RootDB, "root"); len(list) != 0 {
		t.Errorf("told: %+v", list)
	}
	// And a stand-in's reply ends no live run.
	v.Key = "test.streak.live"
	for i := 0; i < checkstreak.After-1; i++ {
		v.Judge(context.Background(), reply("model-c", ""), "state")
	}
	sent := 0
	standInGood := func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		sent++
		return &Response{Content: `{"subject": "x", "why": "y", "answer": "on"}`, Model: StandInModel}, nil
	}
	v.Judge(context.Background(), standInGood, "state")
	v.Judge(context.Background(), reply("model-c", ""), "state")
	if list := notices.List(RootDB, "root"); len(list) != 1 || sent != 1 {
		t.Errorf("a stand-in's reply ended a live run: %+v", list)
	}
}

// The notice names a check by its key when it has no name, and a reply with
// no model as its model.
func TestTheNoticeSaysWhatItKnows(t *testing.T) {
	n := streakNotice("test.unregistered", " ", "why", 4)
	if n.Title != "test.unregistered is giving no verdict on its model" || !strings.Contains(n.Body, "The last 4 replies") || n.Kind != notices.KindStopped {
		t.Errorf("%+v", n)
	}
	if checkLabel("memory.gate") != "Memory gate" {
		t.Error("checkLabel")
	}
}

// With no store, or no user store, nobody is told and nothing breaks.
func TestNoStoreTellsNobody(t *testing.T) {
	privateWorld(t)
	root := RootDB
	RootDB = nil
	tellStreak("memory.gate", "m", "r", 3)
	RootDB = root
	auth := AuthDB
	AuthDB = func() Database { return nil }
	tellStreak("memory.gate", "m", "r", 3)
	AuthDB = nil
	tellStreak("memory.gate", "m", "r", 3)
	AuthDB = auth
}
