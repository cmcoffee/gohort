package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// "Lead reviews" is its own stop: the turn stays on the worker and the lead
// only reads the result. Choosing anything else clears it, and the editor
// draws the choices as a scale in order of how much the lead does.
func TestLeadReviewIsAStopOnTheScale(t *testing.T) {
	pinRootDB(t)
	a := AgentRecord{ID: "a1", Name: "Helper"}
	applyLeadUse(RootDB, &a, leadUseHybrid)
	if !a.LeadReview || a.LeadModel || a.LeadPlans || leadUseOf(RootDB, a) != leadUseHybrid {
		t.Fatalf("review: %+v reads %s", a, leadUseOf(RootDB, a))
	}
	for _, other := range []string{leadUseLead, leadUsePlan, leadUseOff} {
		b := a
		applyLeadUse(RootDB, &b, other)
		if b.LeadReview {
			t.Errorf("%s kept the review on", other)
		}
	}
	f := leadUseField(true, AgentRecord{ID: "a1"})
	if f.Type != "scale" {
		t.Errorf("the field is a %q", f.Type)
	}
	var order []string
	for _, o := range f.Options {
		order = append(order, o.Value)
		if strings.TrimSpace(o.Help) == "" {
			t.Errorf("stop %s says nothing about itself", o.Value)
		}
	}
	at := func(v string) int { return slices.Index(order, v) }
	if !(at(leadUseOff) == 0 && at(leadUseOff) < at(leadUseHybrid) && at(leadUseHybrid) < at(leadUsePlan) && at(leadUsePlan) < at(leadUseLead) && at(leadUseLead) == len(order)-1) {
		t.Errorf("the scale is out of order: %v", order)
	}
	if c := at(leadUseConsult); c >= 0 && (c < at(leadUseOff) || c > at(leadUseHybrid)) {
		t.Errorf("ask when stuck is out of place: %v", order)
	}
}

// The reviewer exists only for an agent that asked for it and can reach the
// lead, reads the lead's verdict, and a verdict it cannot read lets the reply
// go rather than sending it back.
func TestTheLeadReviewerReadsTheVerdict(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	turn := func(a AgentRecord) *chatTurn {
		return &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background()}
	}
	if turn(AgentRecord{ID: "a1"}).leadReviewer(context.Background()) != nil {
		t.Error("an agent that did not ask for a review has one")
	}
	if turn(AgentRecord{ID: "a1", LeadReview: true, LeadModel: true}).leadReviewer(context.Background()) != nil {
		t.Error("an agent already on the lead is reviewed by it as well")
	}
	private := turn(AgentRecord{ID: "a1", LeadReview: true})
	private.privateMode = true
	if private.leadReviewer(context.Background()) != nil {
		t.Error("a Private turn is sent to the lead for review")
	}

	ev := TurnClaimEvidence{Request: "is my 9:40 running?", Reply: "Yes.", ToolCalls: []string{"search"},
		ToolOutputs: []string{"search: the 9:40 is cancelled"}}
	for _, c := range []struct {
		answer     string
		send, ok   bool
		changesHas string
	}{
		{`{"verdict":"SEND_BACK","changes":"1. The search says the 9:40 is cancelled."}`, true, true, "cancelled"},
		{`{"verdict":"APPROVE"}`, false, true, ""},
		{`{"verdict":"SEND_BACK","changes":""}`, false, true, ""},
		{`I think it is fine`, false, false, ""},
	} {
		fake := &FakeLLM{Turns: []FakeTurn{{Content: c.answer, Repeat: true}}}
		app.LLM, app.LeadLLM = fake, fake
		review := turn(AgentRecord{ID: "a1", LeadReview: true}).leadReviewer(context.Background())
		send, changes, ok := review(ev)
		if send != c.send || ok != c.ok || !strings.Contains(changes, c.changesHas) {
			t.Errorf("%q: send=%v ok=%v changes=%q", c.answer, send, ok, changes)
		}
		cfg := fake.Config(0)
		if cfg.RouteKey != leadReviewRouteKey || !strings.Contains(cfg.SystemPrompt, "When in doubt, approve") {
			t.Errorf("the review went out on %q without its brief", cfg.RouteKey)
		}
		if p := fake.Prompt(0); !strings.Contains(p, "9:40 is cancelled") || !strings.Contains(p, "THE REPLY:\nYes.") {
			t.Errorf("the reviewer was not shown the turn:\n%s", p)
		}
	}
}

// A whole chat turn on "Lead reviews": the worker answers from a search, the
// lead sends the reply back with what the search said, the worker answers
// again, and that revision is what the second review reads and lets go.
func TestAReviewedChatTurnEndToEnd(t *testing.T) {
	RegisterReferenceSource(nightlyLogs{})
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "s1", Name: "search_nightly", Args: map[string]any{"query": "failure"}}}},
		{Content: "Everything ran fine last night."},
		{Content: `{"verdict":"SEND_BACK","changes":"1. The search returned a failure; the reply says everything ran fine."}`},
		{Content: "Last night's build failed."},
		{Content: `{"verdict":"APPROVE"}`, Repeat: true},
	}}
	T.LLM, T.LeadLLM = fake, fake
	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Investigator", OrchestratorPrompt: "Explain failures.",
		LeadReview: true, AttachedSources: []ReferenceSelection{{Kind: "nightlylogs", ItemID: "nightly"}}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"message": "What failed last night?"})
	w := httptest.NewRecorder()
	T.handleSend(w, httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+a.ID, strings.NewReader(string(body))), udb, user, a)
	var reviews []int
	for i := 0; i < fake.Calls(); i++ {
		if fake.Config(i).RouteKey == leadReviewRouteKey {
			reviews = append(reviews, i)
		}
	}
	if len(reviews) != 2 {
		t.Fatalf("reviews at calls %v of %d: %s", reviews, fake.Calls(), w.Body.String())
	}
	if p := fake.Prompt(reviews[0]); !strings.Contains(p, "Everything ran fine") || !strings.Contains(p, "disk full") {
		t.Errorf("the first review did not read the draft and the search result:\n%s", p)
	}
	if !strings.Contains(fake.Prompt(reviews[1]), "THE REPLY:\nLast night's build failed.") {
		t.Errorf("the second review did not read the revision:\n%s", fake.Prompt(reviews[1]))
	}
	revised := false
	for _, m := range fake.Sent(reviews[0] + 1) {
		if strings.Contains(m.Content, "sends it back") && strings.Contains(m.Content, "returned a failure") {
			revised = true
		}
	}
	if !revised {
		t.Error("the worker was not given the review's changes")
	}
}

// nightlyLogs is a source whose search answers, for a turn that has to read a
// real result.
type nightlyLogs struct{}

func (nightlyLogs) Kind() string  { return "nightlylogs" }
func (nightlyLogs) Label() string { return "Nightly logs" }
func (nightlyLogs) List(user string) []ReferenceItem {
	return []ReferenceItem{{ID: "nightly", Name: "Nightly", Desc: "Nightly build logs."}}
}
func (nightlyLogs) Fetch(ctx context.Context, user, itemID, query string) string { return "" }
func (nightlyLogs) ItemTools(user, itemID string) []AgentToolDef {
	return []AgentToolDef{{
		Tool: Tool{Name: "search_nightly", Description: "Search the nightly logs.", Parameters: map[string]ToolParam{"query": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "02:14 build #812 FAILED: disk full on /var", nil
		},
	}}
}

// plannedReplyRun is a planned turn whose steps searched, about to write its
// reply, on an agent with the review on or off. The model first writes the
// reply below, then answers as lead says.
func plannedReplyRun(t *testing.T, review bool, lead ...FakeTurn) (*sendRun, *FakeLLM, *syncBuf) {
	t.Helper()
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: append([]FakeTurn{{Content: "Everything ran fine last night."}}, lead...)}
	T.LLM, T.LeadLLM = fake, fake
	buf := &syncBuf{}
	turn := &chatTurn{app: T, ctx: context.Background(), user: user, udb: udb, sse: &sseWriter{live: buf},
		agent: AgentRecord{ID: "a1", Name: "Helper", LeadReview: review}}
	turn.toolCalls = []toolCallRecord{{Name: "search_nightly", Result: "02:14 build #812 FAILED: disk full on /var"}}
	steps := []PlanStep{{ID: 1, Title: "Read the logs", Output: "Build #812 failed: disk full."}}
	sr := &sendRun{turn: turn, sse: turn.sse, ctx: context.Background(), steps: steps,
		req: sendRequest{Message: "What failed last night?"}}
	return sr, fake, buf
}

// A planned turn's reply is reviewed too: the review sat in the agent loop,
// which a planned turn leaves at plan_set, so every planned reply went out
// unread. Sent back, it is written again with the changes and shown as a
// revision, and the first version stays in the conversation.
func TestAPlannedTurnsReplyIsReviewed(t *testing.T) {
	sr, fake, buf := plannedReplyRun(t, true,
		FakeTurn{Content: `{"verdict":"SEND_BACK","changes":"1. The step found build #812 failed; the reply says everything ran fine."}`},
		FakeTurn{Content: "Build #812 failed last night: the disk filled up."})
	if !sr.synthesize() {
		t.Fatal("the reply was not written")
	}
	if fake.Calls() != 3 || fake.Config(1).RouteKey != leadReviewRouteKey {
		t.Fatalf("expected the reply, a review, then a rewrite, got %d calls", fake.Calls())
	}
	if p := fake.Prompt(1); !strings.Contains(p, "Everything ran fine") || !strings.Contains(p, "disk full") || !strings.Contains(p, "Read the logs") {
		t.Errorf("the review must read the reply, what the steps ran, and their findings:\n%s", p)
	}
	told := false
	for _, m := range fake.Sent(2) {
		if strings.Contains(m.Content, "sends it back") && strings.Contains(m.Content, "build #812 failed") {
			told = true
		}
	}
	if !told {
		t.Error("the rewrite was not given the review's changes")
	}
	if sr.reply != "Build #812 failed last night: the disk filled up." || sr.replyLabel != reviewedReplyLabel {
		t.Errorf("the revision must replace the reply and be labelled: %q (%q)", sr.reply, sr.replyLabel)
	}
	if !strings.Contains(buf.String(), reviewedReplyLabel) {
		t.Error("the page was not told the revision is one")
	}
	if kept := sr.turn.drainMidTurnBubbles(); len(kept) != 1 || kept[0].Content != "Everything ran fine last night." {
		t.Errorf("the first version must stay in the conversation: %+v", kept)
	}
}

// Approved, the reply stands; and an agent not on Hybrid is not reviewed.
func TestAnApprovedOrUnreviewedPlannedReplyStands(t *testing.T) {
	sr, fake, _ := plannedReplyRun(t, true, FakeTurn{Content: `{"verdict":"APPROVE"}`})
	sr.synthesize()
	if fake.Calls() != 2 || sr.reply != "Everything ran fine last night." || sr.replyLabel != "" {
		t.Errorf("an approved reply must stand: %d calls, %q", fake.Calls(), sr.reply)
	}
	sr, fake, _ = plannedReplyRun(t, false)
	sr.synthesize()
	if fake.Calls() != 1 {
		t.Errorf("an agent not on Hybrid must not be reviewed: %d calls", fake.Calls())
	}
}

// The written reply is the turn's current bubble, so a file the delivery
// backstop sends afterwards lands under it: it was sent with no bubble, and the
// page dropped a planned turn's Word document.
func TestAPlannedReplyIsTheBubbleItsFilesLandIn(t *testing.T) {
	sr, _, buf := plannedReplyRun(t, false)
	sr.synthesize()
	id := sr.turn.getCurrentMsgID()
	if !strings.HasPrefix(id, "synth-") || !strings.Contains(buf.String(), id) {
		t.Errorf("the reply's bubble must be the current one, got %q", id)
	}
}
