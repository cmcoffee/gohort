package orchestrate

// A correction must never hide what the model originally said. These drive the
// real loop through the chat turn's own hooks (stream, step, settle, retract,
// strike, label) and then persist the way handleSend does, so what they check
// is what a reload and the export would show.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// runCorrectedTurn runs one turn through planRun's hooks and persists it the
// way the directReply path in handleSend does.
func runCorrectedTurn(t *testing.T, turns []FakeTurn, cfg AgentLoopConfig) (ChatSession, string) {
	t.Helper()
	pr, buf := emitFixture()
	stub := &FakeLLM{Turns: turns}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	cfg.Stream = pr.streamHandler
	cfg.OnStep = pr.onStepHandler
	cfg.SettleRound = pr.settleRound
	cfg.RetractRound = pr.retractRound
	cfg.StrikeRound = pr.strikeRound
	cfg.LabelNextRound = pr.labelNextRound
	if cfg.MaxRounds == 0 {
		cfg.MaxRounds = 6
	}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hello"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pr.resp = resp
	_, _, direct, err := pr.finish()
	if err != nil {
		t.Fatal(err)
	}
	var sess ChatSession
	orphans := appendMidTurnBubbles(&sess, pr.t.drainMidTurnBubbles(), direct)
	sess.Messages = append(sess.Messages, ChatMessage{
		Role: "assistant", Content: direct, ToolCalls: orphans,
		Label: pr.t.finalReplyLabel(direct),
	})
	return sess, buf.String()
}

func TestAnUnkeptClaimStaysStruckInTheSavedTurn(t *testing.T) {
	const claim = "I emailed the summary to the team."
	judged := 0
	sess, live := runCorrectedTurn(t, []FakeTurn{
		{Content: claim},
		{Content: "I have not made the report yet. Want me to start it?", Repeat: true},
	}, AgentLoopConfig{
		TurnClaimJudge: func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
			judged++
			if judged == 1 {
				return TurnClaimVerdict{Unkept: true, Claim: claim, Why: "no email was sent"}, true
			}
			return TurnClaimVerdict{}, true
		},
	})
	if len(sess.Messages) != 2 {
		t.Fatalf("want the struck reply and the corrected one, got %d: %+v", len(sess.Messages), sess.Messages)
	}
	struck := sess.Messages[0]
	if struck.Content != claim || !strings.HasPrefix(struck.Retracted, "Retracted:") || !strings.Contains(struck.Retracted, "no email was sent") {
		t.Errorf("the original must be saved, marked retracted with its reason: %+v", struck)
	}
	if sess.Messages[1].Retracted != "" || !strings.HasPrefix(sess.Messages[1].Content, "I have not made") {
		t.Errorf("the corrected reply follows as an ordinary message: %+v", sess.Messages[1])
	}
	if !strings.Contains(live, `"kind":"chunk_strike"`) || !strings.Contains(live, "no email was sent") {
		t.Errorf("the page must be told to strike the bubble, with the reason:\n%s", live)
	}
	// The model's next turn reads it as retracted, never as its own fact.
	hist := toLLMMessages(sess.Messages)
	if !strings.Contains(hist[0].Content, "<gohort-meta>") || !strings.Contains(hist[0].Content, "retracted and does not stand") {
		t.Errorf("a retracted reply must reach the model with its retraction noted, got %q", hist[0].Content)
	}
	if strings.HasPrefix(hist[0].Content, claim) {
		t.Error("the claim must not open the history entry bare")
	}
	if hist[1].Content != sess.Messages[1].Content {
		t.Errorf("an ordinary reply is carried as written, got %q", hist[1].Content)
	}
}

// A struck reply is never deduplicated away against the reply that replaced
// it: the correction often says much the same, and the struck one is the
// record of what was taken back.
func TestAStruckBubbleSurvivesTheFinalReplyDedup(t *testing.T) {
	var sess ChatSession
	text := "The backup finished at 02:00 and every volume verified."
	appendMidTurnBubbles(&sess, []ChatMessage{{Role: "assistant", Content: text, Retracted: "Retracted: no backup ran."}}, text)
	if len(sess.Messages) != 1 || sess.Messages[0].Retracted == "" {
		t.Fatalf("the struck bubble must be kept, got %+v", sess.Messages)
	}
}

// A held stream (an agent with an output guardrail) never showed the text, and
// the warden has not passed it: striking it would publish it. It is erased.
func TestAHeldReplyTheWardenBlocksIsErasedNotStruck(t *testing.T) {
	pr, buf := emitFixture()
	pr.holdStream = true
	pr.t.guardrails = &guardrailEnforcement{Check: func(hook, text string) GuardrailDecision {
		return GuardrailDecision{Blocked: true}
	}}
	pr.streamHandler("The salary band is 150k.")
	pr.strikeRound("Retracted: said something that did not happen.")
	if strings.Contains(buf.String(), "chunk_strike") || strings.Contains(buf.String(), "150k") {
		t.Fatalf("a blocked held reply must not be shown struck:\n%s", buf.String())
	}
	if got := pr.t.drainMidTurnBubbles(); len(got) != 0 {
		t.Fatalf("a blocked held reply must not be saved, got %+v", got)
	}
	if pr.streamMsgID != "" || pr.streamedBuf.Len() != 0 {
		t.Error("the stream state must be reset so the retry opens a fresh bubble")
	}
}

func groundingOnce(claim, basis string) func(TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
	judged := 0
	return func(ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
		judged++
		if judged > 1 {
			return TurnGroundingVerdict{}, true
		}
		return TurnGroundingVerdict{Asserted: true, Claim: claim, Basis: basis}, true
	}
}

func TestAGroundingCorrectionIsSavedUnderItsLabel(t *testing.T) {
	const original = "The staging server runs Ubuntu 22.04, so that package will work."
	const correction = "That is going by my note from last month; I have not checked the server since."
	sess, live := runCorrectedTurn(t, []FakeTurn{
		{Content: original},
		{Content: correction, Repeat: true},
	}, AgentLoopConfig{
		UncheckedClaims:    []string{"the staging server runs Ubuntu 22.04"},
		TurnGroundingJudge: groundingOnce(original, "the staging server runs Ubuntu 22.04"),
	})
	if len(sess.Messages) != 2 {
		t.Fatalf("want the original and the correction, got %+v", sess.Messages)
	}
	if sess.Messages[0].Content != original || sess.Messages[0].Label != "" || sess.Messages[0].Retracted != "" {
		t.Errorf("the original stands as it was: %+v", sess.Messages[0])
	}
	if sess.Messages[1].Content != correction || sess.Messages[1].Label != "Correction" {
		t.Errorf("the follow-up must be saved labelled a correction: %+v", sess.Messages[1])
	}
	if !strings.Contains(live, `"kind":"message_label"`) || !strings.Contains(live, `"label":"Correction"`) {
		t.Errorf("the page must be told to label the follow-up:\n%s", live)
	}
}

// The live failure: the follow-up said "All cleared" again. It is dropped, and
// the saved turn is the original alone: not an empty message, not the repeat.
func TestAGroundingRetryThatRepeatsTheClaimLeavesTheOriginalAsTheReply(t *testing.T) {
	const original = "All cleared. The backlog is empty and nothing is waiting on you."
	sess, live := runCorrectedTurn(t, []FakeTurn{
		{Content: original},
		{Content: "Good news: the backlog is empty and nothing is waiting on you.", Repeat: true},
	}, AgentLoopConfig{
		UncheckedClaims:    []string{"the backlog was cleared"},
		TurnGroundingJudge: groundingOnce("The backlog is empty and nothing is waiting on you.", "the backlog was cleared"),
	})
	if len(sess.Messages) != 1 {
		t.Fatalf("want the original alone, got %d: %+v", len(sess.Messages), sess.Messages)
	}
	if got := sess.Messages[0]; got.Content != original || got.Label != "" {
		t.Errorf("the saved reply must be the original, unlabelled: %+v", got)
	}
	if strings.Contains(live, "message_label") {
		t.Errorf("a dropped follow-up must not be labelled on the page:\n%s", live)
	}
}

func TestTheExportShowsStruckAndLabelledReplies(t *testing.T) {
	sess := ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "send me the report"},
		{Role: "assistant", Content: "I emailed the summary to the team.\n\nAnything else?", Retracted: "Retracted: said \"I emailed the summary to the team.\", but no email was sent."},
		{Role: "assistant", Content: "I have not made the report yet."},
		{Role: "assistant", Content: "That is going by my note from last month.", Label: "Correction"},
	}}
	md := renderSessionMarkdown(AgentRecord{Name: "Helper"}, sess, true)
	for _, want := range []string{
		"## Assistant (retracted)",
		"> Retracted: said \"I emailed the summary to the team.\", but no email was sent.",
		"~~I emailed the summary to the team.~~",
		"~~Anything else?~~",
		"## Assistant [Correction]",
		"That is going by my note from last month.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("export is missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "~~I have not made") {
		t.Error("only the retracted reply is struck")
	}
}
