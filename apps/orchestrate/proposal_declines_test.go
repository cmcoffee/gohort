package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// fakeProposalEmbedder answers with a fixed vector per text, so how alike two
// proposals are is set by the test: a text it does not know is unrelated to
// every other.
func fakeProposalEmbedder(t *testing.T, vecs map[string][]float32) {
	t.Helper()
	savedSvc, savedCfg := media.EmbedService, GetEmbeddingConfig()
	t.Cleanup(func() { media.EmbedService = savedSvc; SetEmbeddingConfig(savedCfg) })
	media.EmbedService = func(_ context.Context, _, _ string, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, text := range texts {
			if v, ok := vecs[text]; ok {
				out[i] = v
			} else {
				out[i] = []float32{0, 0, 0, 1}
			}
		}
		return out, nil
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "vec", Model: "m1"})
}

const (
	reworded = "Check a thread against ENGAGED in your notes before you reply; never comment again on a listed one."
	alike    = "Keep a list of the threads you have replied to, and read it before replying."
)

func declineVectors() map[string][]float32 {
	return map[string][]float32{
		engagedRule: {1, 0, 0, 0},
		reworded:    {0.95, 0.31, 0, 0}, // cosine 0.95: the same rule
		alike:       {0.8, 0.6, 0, 0},   // cosine 0.80: like it, not it
	}
}

// The owner's No is kept. The same rule is not put to them again, however it
// is spaced or cased, nor reworded, by this agent or by one it owns, whether
// the owner is there to ask or not; the agent is told when they said no.
func TestADeclinedRuleIsNotProposedAgain(t *testing.T) {
	udb, turn := proposeRuleFixture(t)
	fakeProposalEmbedder(t, declineVectors())
	turn.askVia = func(prompt, detail string, yes []string) string { return "No" }
	if out, _ := turn.proposeRule(engagedRule, "I re-commented."); !strings.Contains(out, "your owner declined this") {
		t.Fatalf("declined: %q", out)
	}

	asked := 0
	turn.askVia = func(prompt, detail string, yes []string) string { asked++; return yes[0] }
	for _, again := range []string{"  before replying ON a thread, check it against ENGAGED in your notes; a listed thread gets no new comment  ", reworded} {
		if out, _ := turn.proposeRule(again, "again"); !strings.Contains(out, "Not proposed: your owner declined this") {
			t.Errorf("%q came back: %q", again, out)
		}
	}
	if asked != 0 {
		t.Errorf("the owner was asked %d times about a rule they declined", asked)
	}

	// Its sub-agent, with nobody watching: not queued either.
	if _, err := saveAgent(udb, AgentRecord{ID: "s1", Name: "Helper", Owner: "u", OwnedBy: "a1", OrchestratorPrompt: "You help."}); err != nil {
		t.Fatal(err)
	}
	sub := &chatTurn{agent: AgentRecord{ID: "s1", Name: "Helper"}, user: "u", udb: udb}
	if out, _ := sub.proposeRule(reworded, "my parent wanted it"); !strings.Contains(out, "Not proposed") {
		t.Errorf("a sub-agent re-raised its parent's declined rule: %q", out)
	}
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Action == proposeRuleAction {
			t.Errorf("a declined rule was queued: %+v", a)
		}
	}
	if rec, _ := loadAgent(udb, "a1"); rec.Rules != "Be brief." {
		t.Errorf("rules changed: %q", rec.Rules)
	}
}

// One only like a declined rule reaches the owner, marked with the one they
// declined, and the owner decides.
func TestARuleLikeADeclinedOneIsShownMarked(t *testing.T) {
	_, turn := proposeRuleFixture(t)
	fakeProposalEmbedder(t, declineVectors())
	turn.askVia = func(prompt, detail string, yes []string) string { return "No" }
	turn.proposeRule(engagedRule, "I re-commented.")

	var detail string
	turn.askVia = func(prompt, d string, yes []string) string { detail = d; return yes[0] }
	if out, _ := turn.proposeRule(alike, "a different idea"); !strings.Contains(out, "Added") {
		t.Fatalf("a different rule should reach the owner: %q", out)
	}
	if !strings.Contains(detail, "looks like one you declined") || !strings.Contains(detail, "ENGAGED") {
		t.Errorf("the card should show the declined one beside it:\n%s", detail)
	}
}

// Not now holds for a while and then lets the rule be proposed again; an
// unanswered card is not a no at all.
func TestNotNowLapses(t *testing.T) {
	udb, turn := proposeRuleFixture(t)
	fakeProposalEmbedder(t, declineVectors())
	turn.askVia = func(prompt, detail string, yes []string) string { return "Not now" }
	if out, _ := turn.proposeRule(engagedRule, "x"); !strings.Contains(out, "said not now") || !strings.Contains(out, "proposed again after") {
		t.Fatalf("not now: %q", out)
	}
	if out, _ := turn.proposeRule(engagedRule, "x"); !strings.Contains(out, "Not proposed") {
		t.Fatalf("a Not now should hold for now: %q", out)
	}
	for _, k := range udb.Keys(proposalDeclinesTable) {
		var d proposalDecline
		udb.Get(proposalDeclinesTable, k, &d)
		d.Until = time.Now().Add(-time.Minute)
		udb.Set(proposalDeclinesTable, k, d)
	}
	turn.askVia = func(prompt, detail string, yes []string) string { return yes[0] }
	if out, _ := turn.proposeRule(engagedRule, "x"); !strings.Contains(out, "Added") {
		t.Errorf("after Not now lapsed the rule should reach the owner: %q", out)
	}
}

// A proposal answered from the queue keeps its no too: No on the deny
// endpoint, Not now on its own; anything else denied is not a proposal and
// keeps nothing.
func TestAQueuedProposalsNoIsKept(t *testing.T) {
	app, req, udb := authedApp(t)
	pinRootDB(t)
	modes := map[string]string{}
	for path, mode := range map[string]string{"deny": declineNo, "not-now": declineNotNow} {
		held := SaveAuthorization(RootDB, Authorization{Owner: "alice", Action: proposeRuleAction, Agent: "a-" + mode, Brief: "Rule for " + mode + "."})
		w := httptest.NewRecorder()
		routed(app)(w, req(http.MethodPost, "/api/console/approvals/"+path+"?id="+held.ID, nil))
		if w.Code >= 300 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		modes["a-"+mode] = mode
	}
	SaveAuthorization(RootDB, Authorization{Owner: "alice", Agent: "Scout", Brief: "a handoff"})
	for _, a := range ListAuthorizations(RootDB, "alice") {
		if a.Action == "" {
			routed(app)(httptest.NewRecorder(), req(http.MethodPost, "/api/console/approvals/deny?id="+a.ID, nil))
		}
	}
	got := map[string]string{}
	for _, k := range udb.Keys(proposalDeclinesTable) {
		var d proposalDecline
		udb.Get(proposalDeclinesTable, k, &d)
		got[d.Agent] = d.Mode
	}
	if len(got) != 2 || got["a-no"] != declineNo || got["a-not_now"] != declineNotNow {
		t.Errorf("declines kept = %v, want %v", got, modes)
	}
}

// Queued proposals are answered Add it / Not now / No, and requests keep
// Deny / Allow once; both still count as waiting.
func TestAProposalRowIsAnsweredAsAProposal(t *testing.T) {
	app, req, _ := authedApp(t)
	pinRootDB(t)
	SaveAuthorization(RootDB, Authorization{Owner: "alice", Action: proposeRuleAction, Agent: "a1", Brief: "A rule."})
	SaveAuthorization(RootDB, Authorization{Owner: "alice", Agent: "Scout", Brief: "a handoff"})
	w := httptest.NewRecorder()
	routed(app)(w, req(http.MethodGet, "/api/console/permissions", nil))
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	seen := 0
	for _, r := range rows {
		if r["_pending"] != true {
			continue
		}
		seen++
		proposal, request := r["_proposal"] == true, r["_request"] == true
		if strings.Contains(fmt.Sprint(r["Detail"]), "rule") != proposal || proposal == request {
			t.Errorf("row %v: _proposal=%v _request=%v", r["Detail"], proposal, request)
		}
	}
	if seen != 2 {
		t.Errorf("both should be waiting, %d were: %v", seen, rows)
	}
}
