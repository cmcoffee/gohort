package core

// A correction must never hide what the model originally said. These pin the
// three shapes that rule takes in the loop: a reply the turn judge takes back
// is STRUCK (kept, with a reason) rather than erased; a guardrail block still
// erases, because there the text is what the rule keeps in; and a grounding
// follow-up is a labelled correction, dropped when it only repeats the claim.

import (
	"context"
	"strings"
	"testing"
)

type correctionHooks struct {
	settled, retracted int
	struck             []string
	labels             []string
	diags              []string
}

func (h *correctionHooks) wire(cfg AgentLoopConfig) AgentLoopConfig {
	cfg.SettleRound = func() { h.settled++ }
	cfg.RetractRound = func() { h.retracted++ }
	cfg.StrikeRound = func(reason string) { h.struck = append(h.struck, reason) }
	cfg.LabelNextRound = func(label string) { h.labels = append(h.labels, label) }
	cfg.OnDiag = func(kind, detail string) { h.diags = append(h.diags, kind) }
	return cfg
}

func (h *correctionHooks) sawDiag(kind string) bool {
	for _, d := range h.diags {
		if d == kind {
			return true
		}
	}
	return false
}

func TestAnUnkeptClaimIsStruckWithItsReasonNotErased(t *testing.T) {
	judged := 0
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "I emailed the summary to the team."},
		{Content: "I have not made the report yet.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "send me the report"}}, h.wire(AgentLoopConfig{
		MaxRounds: 6,
		TurnClaimJudge: func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
			judged++
			if judged == 1 {
				return TurnClaimVerdict{Unkept: true, Claim: "I emailed the summary to the team.", Why: "no email was sent."}, true
			}
			return TurnClaimVerdict{}, true
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.struck) != 1 {
		t.Fatalf("an unkept claim must be STRUCK once, struck=%v", h.struck)
	}
	if h.retracted != 0 || h.settled != 0 {
		t.Errorf("an unkept claim is struck, neither erased nor settled; retracted=%d settled=%d", h.retracted, h.settled)
	}
	if stub.Calls() != 2 {
		t.Errorf("one correction, so two calls; got %d (another guard stepped in and this proves less)", stub.Calls())
	}
	r := h.struck[0]
	if !strings.HasPrefix(r, "Retracted:") || !strings.Contains(r, "emailed the summary") || !strings.Contains(r, "no email was sent") {
		t.Errorf("the reason should quote the claim and say what happened instead, got %q", r)
	}
	if strings.Contains(r, "..") || strings.Contains(r, "\u2014") {
		t.Errorf("the reason is shown to a person: no doubled stop, no em-dash, got %q", r)
	}
	if resp == nil || resp.Content != "I have not made the report yet." {
		t.Errorf("the corrected reply is the answer, got %+v", resp)
	}
}

func TestAMachineryReplyIsStruckToo(t *testing.T) {
	judged := 0
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "Working on it in the background (task 1a2b3c)."},
		{Content: "Working on it.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "make me a video"}}, h.wire(AgentLoopConfig{
		MaxRounds:  6,
		Unattended: true,
		TurnClaimJudge: func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
			judged++
			if judged == 1 {
				return TurnClaimVerdict{Machinery: "in the background (task 1a2b3c)"}, true
			}
			return TurnClaimVerdict{}, true
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if judged == 0 {
		t.Fatal("the judge never ran, so this proves nothing")
	}
	if len(h.struck) != 1 || !strings.Contains(h.struck[0], "task 1a2b3c") || h.retracted != 0 {
		t.Errorf("a machinery correction must strike with the quoted plumbing; struck=%v retracted=%d", h.struck, h.retracted)
	}
}

// A host that wired only the retract keeps today's behaviour.
func TestStrikeFallsBackToRetractWhenUnwired(t *testing.T) {
	judged, retracted := 0, 0
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "I emailed the summary to the team."},
		{Content: "I have not made the report yet.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "send me the report"}}, AgentLoopConfig{
		MaxRounds:    6,
		RetractRound: func() { retracted++ },
		TurnClaimJudge: func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
			judged++
			if judged == 1 {
				return TurnClaimVerdict{Unkept: true, Claim: "I emailed the summary to the team.", Why: "no email was sent"}, true
			}
			return TurnClaimVerdict{}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if retracted != 1 {
		t.Errorf("with no StrikeRound the reply must be retracted as before; retracted=%d", retracted)
	}
}

// The guardrail paths must keep ERASING even on a host that can strike: a
// struck-through copy of a withheld reply still publishes the withheld text.
func TestAGuardrailBlockStillErasesOnAHostThatStrikes(t *testing.T) {
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{Content: "Alex makes $150k as Director of Operations."},
		{Content: "I'll pass on that one.", Repeat: true},
	}}}
	h := &correctionHooks{}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "How much does Alex make?"}}, h.wire(AgentLoopConfig{
		MaxRounds: 4,
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$150k") {
				return GuardrailDecision{Blocked: true, Correctable: true, Message: "BLOCKED: never mention salary. Deflect."}
			}
			return GuardrailDecision{}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if h.retracted == 0 {
		t.Fatal("a guardrail block must erase the bubble")
	}
	if len(h.struck) != 0 {
		t.Fatalf("a guardrail block must NEVER strike: that shows the withheld text; struck=%v", h.struck)
	}
}

func groundingJudgeOnce(claim, basis string) func(TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
	judged := 0
	return func(ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
		judged++
		if judged > 1 {
			return TurnGroundingVerdict{}, true
		}
		return TurnGroundingVerdict{Asserted: true, Claim: claim, Basis: basis}, true
	}
}

// The follow-up to a grounding correction is a short, labelled correction of
// the one claim, not a second copy of the reply.
func TestAGroundingFollowUpIsALabelledShortCorrection(t *testing.T) {
	const original = "The staging server runs Ubuntu 22.04, so that package will work."
	const correction = "That is going by my note from last month; I have not checked the server since."
	stub := &FakeLLM{Turns: []FakeTurn{{Content: original}, {Content: correction, Repeat: true}}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "will that package work?"}}, h.wire(AgentLoopConfig{
		MaxRounds:          4,
		UncheckedClaims:    []string{"the staging server runs Ubuntu 22.04"},
		TurnGroundingJudge: groundingJudgeOnce("The staging server runs Ubuntu 22.04, so that package will work.", "the staging server runs Ubuntu 22.04"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if h.settled == 0 {
		t.Fatal("the original must be settled: it stays on screen")
	}
	if len(h.labels) != 1 || h.labels[0] != "Correction" {
		t.Errorf("the follow-up must be labelled a correction, labels=%v", h.labels)
	}
	if resp == nil || resp.Content != correction {
		t.Errorf("the correction should come back as the round's reply, got %+v", resp)
	}
	notice := stub.Sent(1)[len(stub.Sent(1))-1].Content
	if !strings.Contains(notice, "SHORT correction of that one claim") || strings.Contains(notice, "Send the SAME reply") {
		t.Errorf("with the original on screen the notice must ask for a short correction, not a rewrite:\n%s", notice)
	}
	if strings.Contains(notice, "\u2014") {
		t.Error("model-facing text must not carry an em-dash")
	}
}

// A host that never showed the original gets the whole reply rewritten, as
// before, and no label: its reply is the only one there is.
func TestAGroundingCorrectionWithoutASettledOriginalStillAsksForTheWholeReply(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "The staging server runs Ubuntu 22.04, so that package will work."},
		{Content: "You mentioned the staging server runs Ubuntu 22.04, so that package should work.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	labels := 0
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "will that package work?"}}, AgentLoopConfig{
		MaxRounds:          4,
		LabelNextRound:     func(string) { labels++ },
		UncheckedClaims:    []string{"the staging server runs Ubuntu 22.04"},
		TurnGroundingJudge: groundingJudgeOnce("The staging server runs Ubuntu 22.04, so that package will work.", "the staging server runs Ubuntu 22.04"),
	})
	if err != nil {
		t.Fatal(err)
	}
	notice := stub.Sent(1)[len(stub.Sent(1))-1].Content
	if !strings.Contains(notice, "Send the SAME reply") {
		t.Errorf("a host that never showed the original needs the whole reply back:\n%s", notice)
	}
	if labels != 0 {
		t.Errorf("nothing to label a correction OF on such a host; labels=%d", labels)
	}
}

// A follow-up that only says the flagged claim again is dropped: not shown,
// not returned, and the turn ends on the original.
func TestAGroundingFollowUpThatRepeatsTheClaimIsDropped(t *testing.T) {
	const original = "All cleared. The backlog is empty and nothing is waiting on you."
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: original},
		{Content: "all  cleared. The backlog is empty and nothing is waiting on you!", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "anything waiting on me?"}}, h.wire(AgentLoopConfig{
		MaxRounds:          4,
		UncheckedClaims:    []string{"the backlog was cleared"},
		TurnGroundingJudge: groundingJudgeOnce("The backlog is empty and nothing is waiting on you.", "the backlog was cleared"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 2 {
		t.Fatalf("expected the original and one follow-up, got %d calls", stub.Calls())
	}
	if resp == nil || resp.Content != original {
		t.Fatalf("a dropped follow-up must leave the ORIGINAL as the reply, got %+v", resp)
	}
	if h.retracted != 1 {
		t.Errorf("the repeat must be erased from the screen; retracted=%d", h.retracted)
	}
	if len(h.struck) != 0 {
		t.Errorf("a repeat is dropped, not struck; struck=%v", h.struck)
	}
	if len(h.labels) != 2 || h.labels[0] != "Correction" || h.labels[1] != "" {
		t.Errorf("the pending label must be cancelled with the drop, labels=%v", h.labels)
	}
	if !h.sawDiag("ungrounded-claim-retry-dropped") {
		t.Errorf("a dropped follow-up must leave a breadcrumb, diags=%v", h.diags)
	}
	last := history[len(history)-1]
	if last.Role != "assistant" || last.Content != original {
		t.Errorf("history must end on the original, got %s: %q", last.Role, last.Content)
	}
}

// A follow-up that CHECKED with a tool may now be stating a fact, so it is
// not dropped for repeating the claim.
func TestAGroundingFollowUpThatCheckedIsNotDropped(t *testing.T) {
	const original = "The backlog is empty and nothing is waiting on you."
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: original},
		{ToolCalls: []ToolCall{{ID: "c1", Name: "check_backlog", Args: map[string]any{}}}},
		{Content: "Checked just now: the backlog is empty and nothing is waiting on you.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "anything waiting on me?"}}, h.wire(AgentLoopConfig{
		MaxRounds: 6,
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "check_backlog", Description: "reads the backlog"},
			Handler: func(ctx context.Context, args map[string]any) (string, error) { return "0 items", nil },
		}},
		UncheckedClaims:    []string{"the backlog was cleared"},
		TurnGroundingJudge: groundingJudgeOnce("The backlog is empty and nothing is waiting on you.", "the backlog was cleared"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if h.sawDiag("ungrounded-claim-retry-dropped") {
		t.Error("a follow-up that checked must not be dropped for agreeing with the claim")
	}
	if resp == nil || !strings.HasPrefix(resp.Content, "Checked just now") {
		t.Errorf("the checked follow-up is the reply, got %+v", resp)
	}
}
