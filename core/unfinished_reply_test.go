package core

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/replyguard"
)

// A reply whose last line visibly stops is unfinished; a complete one is not,
// including a casual one with no full stop, and anything that ends a block by
// design.
func TestAReplyThatStopsMidSentenceIsCaught(t *testing.T) {
	stops := []string{
		"Okay, \"Briefing Bot\" it is!\n\nNow, to set up your Briefing Bot, I need a couple more details:",
		"The board is set up, and",
		"I checked the feed and found three stories, including",
		"Posting it to your",
		"Here are the options (",
		"It runs every morning,",
	}
	complete := []string{
		"lol that's great",
		"Sounds good",
		"Done.",
		"Which board should it post to?",
		"Paste the error message here:",
		"Tell me which one you want:",
		"Here are your options:\n- News\n- Guides",
		"```go\nfunc main() {}\n```",
		"## Summary",
		"https://example.com/post/1",
		"| name | value |",
		"I'll post it every morning at 8",
		"",
	}
	for _, r := range stops {
		if !replyEndsMidSentence(r) {
			t.Errorf("should be caught as unfinished: %q", r)
		}
	}
	for _, r := range complete {
		if replyEndsMidSentence(r) {
			t.Errorf("a complete reply was flagged: %q", r)
		}
	}
}

// The loop asks for the rest, and the finished reply is the answer.
func TestAnUnfinishedReplyIsAskedToFinish(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "The board is set up, and"},
		{Content: "The board is set up, and Briefing Bot posts to it every morning.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "set it up"}}, h.wire(AgentLoopConfig{MaxRounds: 6}))
	if err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 2 || !h.sawDiag("unfinished-reply-corrected") {
		t.Fatalf("one request for the rest: calls=%d diags=%v", stub.Calls(), h.diags)
	}
	if resp == nil || resp.Content != "The board is set up, and Briefing Bot posts to it every morning." {
		t.Errorf("the finished reply is the answer: %+v", resp)
	}
}

// guardStore is an in-memory replyguard store for loop tests.
type guardStore map[string][]byte

func (g guardStore) Get(table, key string, out interface{}) bool {
	b, ok := g[table+"/"+key]
	return ok && json.Unmarshal(b, out) == nil
}
func (g guardStore) Set(table, key string, v interface{}) {
	b, _ := json.Marshal(v)
	g[table+"/"+key] = b
}
func (g guardStore) Keys(table string) []string {
	var out []string
	for k := range g {
		if len(k) > len(table) && k[:len(table)+1] == table+"/" {
			out = append(out, k[len(table)+1:])
		}
	}
	return out
}

// A guard set to shadow on a model counts what it would have done and leaves
// the reply alone; set off, it does nothing at all. On, it corrects and counts.
func TestAReplyGuardInShadowOrOffLeavesTheReplyAlone(t *testing.T) {
	replyguard.SetStore(guardStore{})
	defer replyguard.SetStore(nil)
	run := func() (int, *Response, *correctionHooks) {
		stub := &FakeLLM{Turns: []FakeTurn{
			{Content: "The board is set up, and"},
			{Content: "The board is set up, and it posts every morning.", Repeat: true},
		}}
		app := &AppCore{LLM: stub, LeadLLM: stub}
		h := &correctionHooks{}
		resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "set it up"}}, h.wire(AgentLoopConfig{MaxRounds: 6}))
		if err != nil {
			t.Fatal(err)
		}
		return stub.Calls(), resp, h
	}
	count := func() (acted, shadowed int) {
		for _, st := range replyguard.Stats() {
			if st.ID == correctionUnfinished {
				acted, shadowed = acted+st.Acted, shadowed+st.Shadowed
			}
		}
		return
	}

	if err := replyguard.Put(replyguard.Setting{ID: correctionUnfinished, Scope: replyguard.AllTiers, Mode: replyguard.Shadow}); err != nil {
		t.Fatal(err)
	}
	calls, resp, h := run()
	if calls != 1 || h.sawDiag("unfinished-reply-corrected") || resp.Content != "The board is set up, and" {
		t.Errorf("shadow leaves the reply as it was: calls=%d reply=%q", calls, resp.Content)
	}
	if a, s := count(); a != 0 || s != 1 {
		t.Errorf("shadow counts what it would have done: acted=%d shadowed=%d", a, s)
	}

	replyguard.Put(replyguard.Setting{ID: correctionUnfinished, Scope: replyguard.AllTiers, Mode: replyguard.Off})
	if calls, _, _ := run(); calls != 1 {
		t.Errorf("off does nothing: calls=%d", calls)
	}
	if a, s := count(); a != 0 || s != 1 {
		t.Errorf("off counts nothing: acted=%d shadowed=%d", a, s)
	}

	replyguard.Clear(correctionUnfinished, replyguard.AllTiers)
	if calls, _, _ := run(); calls != 2 {
		t.Errorf("on again, it corrects: calls=%d", calls)
	}
	if a, _ := count(); a != 1 {
		t.Errorf("a correction is counted as one: acted=%d", a)
	}
}

// Every guard that spends a correction goes through guardActs, so none can
// escape the admin's mode for it, and every correction kind is registered
// with a name and a description. The finish check is the host's own rule and
// the tool-mention guard gates on its else branch.
func TestEveryReplyGuardAsksItsMode(t *testing.T) {
	for _, file := range []string{"agent_loop.go", "agent_loop_replyguards.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "lr.corrections.available(correction") || strings.Contains(line, "correctionFinishCheck") ||
				strings.Contains(line, "!(lr.corrections.available(correctionToolMention)") {
				continue
			}
			if !strings.Contains(line, "guardActs(") {
				t.Errorf("%s:%d spends a correction without asking the guard's mode: %s", file, i+1, strings.TrimSpace(line))
			}
		}
	}
	known := map[string]bool{}
	for _, g := range replyguard.Guards() {
		known[g.ID] = g.Name != "" && g.Desc != ""
	}
	for _, k := range []string{correctionOrphanedXML, correctionPhantomDelivery, correctionFakeToolCode, correctionActionPromise,
		correctionAnnouncedCall, correctionToolMention, correctionCollapse, correctionGiveUp, correctionUnkeptClaim,
		correctionUngrounded, correctionMachinery, correctionTruncated, correctionRoleBreak, correctionMalformedCall, correctionUnfinished} {
		if !known[k] {
			t.Errorf("guard %q is not registered with a name and a description", k)
		}
	}
}

// A guard drafted from flagged replies runs like a built-in one: on, it takes
// the reply back and sends the model its note; off, it does nothing.
func TestAnAuthoredGuardCorrectsTheReply(t *testing.T) {
	replyguard.SetStore(guardStore{})
	defer replyguard.SetStore(nil)
	g := replyguard.Authored{ID: "authored-please-wait", Name: "Asks the person to wait", Status: replyguard.StatusActive,
		Checks:     []replyguard.Check{{Kind: "last_line_ends_with", Params: map[string]string{"endings": "please wait."}}},
		Correction: "Your reply asks the person to wait for work you did not start. Do the work now, or say plainly what you cannot do."}
	replyguard.SaveAuthored(g)
	run := func() (int, *Response, *correctionHooks) {
		stub := &FakeLLM{Turns: []FakeTurn{
			{Content: "Looking into it now, please wait."},
			{Content: "Here is what I found: the build passed.", Repeat: true},
		}}
		app := &AppCore{LLM: stub, LeadLLM: stub}
		h := &correctionHooks{}
		resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "check the build"}}, h.wire(AgentLoopConfig{MaxRounds: 6}))
		if err != nil {
			t.Fatal(err)
		}
		return stub.Calls(), resp, h
	}
	calls, resp, h := run()
	if calls != 2 || !h.sawDiag("authored-guard-corrected") || resp.Content != "Here is what I found: the build passed." {
		t.Errorf("on, it corrects: calls=%d reply=%q diags=%v", calls, resp.Content, h.diags)
	}
	replyguard.Put(replyguard.Setting{ID: g.ID, Scope: replyguard.AllTiers, Mode: replyguard.Off})
	if calls, _, _ := run(); calls != 1 {
		t.Errorf("off, it does nothing: calls=%d", calls)
	}
}

// A guard's note and its retries can be set per tier: the model reads the
// admin's note in place of the shipped one, and a guard allowed one retry
// asks once.
func TestAGuardsNoteAndRetriesFollowItsSettings(t *testing.T) {
	replyguard.SetStore(guardStore{})
	defer replyguard.SetStore(nil)
	if err := replyguard.Put(replyguard.Setting{ID: correctionAnnouncedCall, Scope: replyguard.AllTiers, Note: "Run the thing you announced, now."}); err != nil {
		t.Fatal(err)
	}
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "I'll check the logs now:"},
		{Content: "The logs are clean."},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "check the logs"}}, h.wire(AgentLoopConfig{MaxRounds: 6})); err != nil {
		t.Fatal(err)
	}
	sent := stub.LastSent()
	if last := sent[len(sent)-1].Content; !strings.Contains(last, "Run the thing you announced, now.") || strings.Contains(last, noteAnnouncedCall) {
		t.Errorf("the admin's note replaces the shipped one: %q", last)
	}

	if err := replyguard.Put(replyguard.Setting{ID: correctionUnfinished, Scope: replyguard.AllTiers, Retries: 1}); err != nil {
		t.Fatal(err)
	}
	stub = &FakeLLM{Turns: []FakeTurn{{Content: "The board is set up, and", Repeat: true}}}
	app = &AppCore{LLM: stub, LeadLLM: stub}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "set it up"}}, (&correctionHooks{}).wire(AgentLoopConfig{MaxRounds: 6})); err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 2 {
		t.Errorf("one retry allowed, one taken: calls=%d", stub.Calls())
	}
}
