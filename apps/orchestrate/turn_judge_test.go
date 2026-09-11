package orchestrate

import (
	"context"
	. "github.com/cmcoffee/gohort/core"
	"strings"
	"testing"
)

// The judge is asked to distinguish "a duration the assistant made up" from one
// it was given — a distinction that is unanswerable from the reply alone, since
// the sentence reads identically either way. The evidence never mentioned that
// a number had been supplied, so the exception was dead text: the framework
// said "This usually takes about 13 seconds; say so if it is worth knowing",
// the model said so, and the machinery guard retracted the reply for saying it.

func TestJudgeIsToldWhenTheWaitWasSupplied(t *testing.T) {
	ev := TurnClaimEvidence{
		Request:       "Can we edit this so the address is only on one pillar?",
		ToolCalls:     []string{"image"},
		Reply:         "That edit is running now, should be about 13 seconds, and I'll send it over when it lands.",
		Backgrounded:  true,
		GivenEstimate: "13 seconds",
	}
	msg := turnJudgeEvidenceMessage(ev)
	if !strings.Contains(msg, "13 seconds") {
		t.Fatal("the supplied wait never reaches the judge, so it cannot tell a given estimate from an invented one")
	}
	if !strings.Contains(msg, "NOT machinery") {
		t.Error("the evidence states the number but not that quoting it is allowed — the judge is left to guess")
	}

	// The system prompt's rule and the evidence have to line up, or the
	// exception is unreachable from either side.
	if !strings.Contains(turnJudgeSysPrompt, "made up rather than one it was given") {
		t.Error("the machinery rule no longer distinguishes an invented duration from a supplied one")
	}
}

func TestJudgeIsNotToldAboutAWaitThatWasNeverOffered(t *testing.T) {
	// No measured duration means detachedNotice tells the model to put NO time
	// on it. An estimate in the reply then really is invented, and must stay
	// flaggable — this is the case the machinery rule exists for.
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request: "make me a picture", ToolCalls: []string{"image"},
		Reply: "Should be about five minutes.", Backgrounded: true,
	})
	if strings.Contains(msg, "NOT machinery") {
		t.Error("the judge is excused an estimate the framework never gave — invented durations would stop being caught")
	}
	// And the backgrounded fact still lands, since the claim arm depends on it.
	if !strings.Contains(msg, "A BACKGROUND JOB WAS STARTED BY THIS TURN: yes") {
		t.Error("the backgrounded fact went missing from the evidence")
	}
}

// The judge's contract with the model: what it must be told, and what it does
// with each shape of answer.
//
// The parsing side is where a live judge actually misbehaves. A small model
// asked for JSON returns prose, or an UNKEPT with nothing quoted, or a verdict
// spelled differently — and each of those, read wrong, retracts a reply that
// was fine.
type judgeStubLLM struct {
	reply   string
	lastMsg string
	calls   int
}

func (s *judgeStubLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	s.calls++
	if len(messages) > 0 {
		s.lastMsg = messages[len(messages)-1].Content
	}
	return &Response{Content: s.reply}, nil
}

func (s *judgeStubLLM) ChatStream(ctx context.Context, messages []Message, h StreamHandler, opts ...ChatOption) (*Response, error) {
	return s.Chat(ctx, messages, opts...)
}

func judgeWith(t *testing.T, reply string) (*OrchestrateApp, *judgeStubLLM) {
	t.Helper()
	stub := &judgeStubLLM{reply: reply}
	return &OrchestrateApp{AppCore: AppCore{LLM: stub}}, stub
}

var garageTurn = TurnClaimEvidence{
	Request:       "Wiwee, add Alex to that picture of you in the garage",
	Reply:         "Here's you, wasting away in the garage like Craig ordered.",
	ToolCalls:     []string{"image", "generate_image"},
	ToolErrors:    2,
	LastToolError: `this backend composes SOURCE PHOTOS (2 image inputs) and was asked for a text-only render`,
	Delivered:     0,
}

func TestTheJudgeIsGivenTheWholeTurn(t *testing.T) {
	app, stub := judgeWith(t, `{"verdict":"KEPT"}`)
	app.judgeTurnClaims(context.Background(), garageTurn)

	// Without every one of these the judge is guessing at the same thing the
	// phrase lists guess at.
	for _, want := range []string{
		"add Alex to that picture", // what was asked
		"image, generate_image",    // what ran, in order, duplicates kept
		"FAILED: 2",                // that it failed
		"text-only render",         // and how, so a truthful report is recognizable
		"FILES BEING DELIVERED WITH THIS REPLY: 0",
		"wasting away in the garage", // and the words under judgement
	} {
		if !strings.Contains(stub.lastMsg, want) {
			t.Errorf("the judge was not told %q:\n%s", want, stub.lastMsg)
		}
	}
}

func TestAVerdictIsOnlyActedOnWhenItIsUsable(t *testing.T) {
	ev := garageTurn

	// A conviction with its quote: acted on.
	app, _ := judgeWith(t, `{"verdict":"UNKEPT","claim":"Here's you, wasting away in the garage","why":"both image calls failed and no file is attached"}`)
	v, ok := app.judgeTurnClaims(context.Background(), ev)
	if !ok || !v.Unkept {
		t.Fatalf("a clear conviction must stand, got %+v ok=%v", v, ok)
	}
	if !strings.Contains(v.Claim, "wasting away") {
		t.Errorf("the quote must survive verbatim, got %q", v.Claim)
	}

	// UNKEPT with nothing quoted: unusable. The correction would tell the model
	// `your reply says: ""`, so it is treated as no opinion rather than acted on.
	app, _ = judgeWith(t, `{"verdict":"UNKEPT","claim":"","why":"it lied"}`)
	if _, ok := app.judgeTurnClaims(context.Background(), ev); ok {
		t.Error("a conviction with nothing to quote must not stand")
	}

	// Prose instead of JSON: no opinion. Deliberately NOT the gatekeeper's
	// scan-for-YES fallback — there a wrong guess drops one message, here it
	// retracts a reply and burns a round.
	app, _ = judgeWith(t, "Honestly it seems fine to me, KEPT I guess")
	if _, ok := app.judgeTurnClaims(context.Background(), ev); ok {
		t.Error("an unparseable verdict must not stand")
	}

	// An acquittal is reported AS an acquittal — the loop needs to know the
	// judge ran and cleared it, which is different from it not answering.
	app, _ = judgeWith(t, `{"verdict":"KEPT","claim":"","why":""}`)
	v, ok = app.judgeTurnClaims(context.Background(), ev)
	if !ok {
		t.Error("a clean KEPT must be reported as an answer")
	}
	if v.Unkept {
		t.Error("KEPT must not convict")
	}
}

func TestAConvictionAlwaysCarriesAReason(t *testing.T) {
	// The correction quotes `why` into the nudge, so an empty one would produce
	// "That did not happen — ." Filled with something true instead.
	app, _ := judgeWith(t, `{"verdict":"UNKEPT","claim":"Here you go.","why":""}`)
	v, ok := app.judgeTurnClaims(context.Background(), garageTurn)
	if !ok || !v.Unkept {
		t.Fatal("precondition: convicted")
	}
	if strings.TrimSpace(v.Why) == "" {
		t.Error("a conviction with no reason must still read as a sentence")
	}
}

func TestNoModelMeansNoJudge(t *testing.T) {
	// A nil hook is how the loop skips the judge entirely, so an app with no
	// model must return nil rather than a func that always fails.
	if j := (&OrchestrateApp{}).turnClaimJudge(context.Background()); j != nil {
		t.Error("an app with no LLM must not offer a judge")
	}
}

func TestTheTriggerNamesTheArmThatFired(t *testing.T) {
	// The tuning signal. A run of acquittals all reading "no tools ran" says
	// that arm is too broad; the same counts spread across three says it is
	// working. Counts alone cannot tell those apart.
	//
	// Order must match turnClaimWorthJudging — first arm wins — or a turn with
	// both no tools AND errors would report the wrong reason it was selected.
	for _, c := range []struct {
		want string
		ev   TurnClaimEvidence
	}{
		{"no tools ran", TurnClaimEvidence{}},
		{"tool errors", TurnClaimEvidence{ToolCalls: []string{"image"}, ToolErrors: 1}},
		{"produced nothing", TurnClaimEvidence{ToolCalls: []string{"generate_image"}}},
	} {
		if got := judgeTrigger(c.ev); got != c.want {
			t.Errorf("trigger = %q, want %q for %+v", got, c.want, c.ev)
		}
	}
}

func TestTheTriggerCarriesNoReplyText(t *testing.T) {
	// A Debug line outlives the turn and lands in a file, and this runs on
	// sessions carrying credentials. The shape of the turn is what is being
	// diagnosed, not its contents.
	ev := TurnClaimEvidence{
		Request: "the root password is hunter2",
		Reply:   "I've stored hunter2 for you.",
	}
	if got := judgeTrigger(ev); strings.Contains(got, "hunter2") {
		t.Errorf("the trigger must not carry turn content: %q", got)
	}
}

func TestTheJudgeIsToldWhetherAJobStarted(t *testing.T) {
	// The claim arm suppresses itself on this FACT rather than on a Go branch,
	// which is what leaves the machinery arm free to look at backgrounded turns.
	// Without it the judge would convict "I'll let you know when it's done" —
	// the exact reply detachedNotice asks the model to write.
	app, stub := judgeWith(t, `{"verdict":"KEPT"}`)
	app.judgeTurnClaims(context.Background(), TurnClaimEvidence{
		Reply: "I'll get that going and let you know when it's done.", Backgrounded: true,
	})
	if !strings.Contains(stub.lastMsg, "BACKGROUND JOB WAS STARTED BY THIS TURN: yes") {
		t.Errorf("the judge must be told a job started:\n%s", stub.lastMsg)
	}
	if !strings.Contains(stub.lastMsg, "IS TRUE") {
		t.Errorf("and what that fact means for the claim:\n%s", stub.lastMsg)
	}

	app, stub = judgeWith(t, `{"verdict":"KEPT"}`)
	app.judgeTurnClaims(context.Background(), TurnClaimEvidence{Reply: "Tokyo is raining."})
	if !strings.Contains(stub.lastMsg, "BACKGROUND JOB WAS STARTED BY THIS TURN: no") {
		t.Errorf("and told plainly when one did not:\n%s", stub.lastMsg)
	}
}

func TestMachineryStandsAloneFromTheClaim(t *testing.T) {
	backgrounded := TurnClaimEvidence{
		Reply:        "The image edit task is still running in the background (task a79c771f5f35a9f6ef0489d0).",
		ToolCalls:    []string{"image"},
		Backgrounded: true,
	}

	// True reply, plumbing leaked: convicted on machinery, NOT on the claim.
	// Getting this backwards would tell the model a true sentence "did not
	// happen", which is both wrong and unfixable from its side.
	app, _ := judgeWith(t, `{"verdict":"KEPT","claim":"","why":"","machinery":"The image edit task is still running in the background (task a79c771f5f35a9f6ef0489d0)."}`)
	v, ok := app.judgeTurnClaims(context.Background(), backgrounded)
	if !ok || v.Unkept {
		t.Fatalf("a true reply that leaks plumbing is not a false claim: %+v ok=%v", v, ok)
	}
	if !strings.Contains(v.Machinery, "a79c771f") {
		t.Errorf("the quote must survive verbatim for the rewrite: %q", v.Machinery)
	}

	// Clean on both arms: nothing to act on.
	app, _ = judgeWith(t, `{"verdict":"KEPT","claim":"","why":"","machinery":""}`)
	v, ok = app.judgeTurnClaims(context.Background(), backgrounded)
	if !ok {
		t.Error("a clean turn is still an answer")
	}
	if v.Unkept || v.Machinery != "" {
		t.Errorf("nothing should be flagged: %+v", v)
	}

	// Both at once: the claim wins the loop's attention, but the machinery
	// finding must survive rather than vanish — a verdict that silently loses
	// half its findings is one nobody can debug.
	app, _ = judgeWith(t, `{"verdict":"UNKEPT","claim":"Here you go.","why":"nothing attached","machinery":"running in the background"}`)
	v, _ = app.judgeTurnClaims(context.Background(), backgrounded)
	if !v.Unkept || v.Machinery == "" {
		t.Errorf("both findings must survive: %+v", v)
	}
}

// The evidence message has to SAY that work happened outside the loop, not
// merely omit the contradiction: the judge reads "TOOLS THE TURN RAN: none"
// two lines above and will weigh it.
func TestEvidenceNamesWorkDoneBeforeTheLoop(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request:   "what does the runbook say about failover?",
		Reply:     "Based on the Confluence research, here's the answer:",
		PriorWork: []string{"step research ran as a delegate (Confluence reader)", "a step ran confluence_search"},
	})
	for _, want := range []string{"BEFORE THE ASSISTANT ANSWERED", "confluence_search", "must be answered KEPT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the judge is not told about the step's work (%q missing):\n%s", want, msg)
		}
	}
	// A turn with nothing before it says nothing about it — an extra line
	// claiming no prior work would invite the judge to weigh an absence.
	plain := turnJudgeEvidenceMessage(TurnClaimEvidence{Request: "hi", Reply: "hello"})
	if strings.Contains(plain, "BEFORE THE ASSISTANT ANSWERED") {
		t.Errorf("nothing ran before this turn; the evidence should not mention it:\n%s", plain)
	}
	// And the trigger label stops calling it "no tools ran" when a step ran.
	if got := judgeTrigger(TurnClaimEvidence{PriorWork: []string{"a step ran confluence_search"}}); got == "no tools ran" {
		t.Error("a turn whose step ran should not be labelled as having run nothing")
	}
}

// The judge is told what RAN, and the unit has to be the action. A tool name
// alone cannot separate nine reads from three writes.
func TestEvidenceNamesTheActionNotJustTheTool(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request:   "post the daily comments",
		Reply:     "Total: 3 comments posted successfully.",
		ToolCalls: []string{"moltbook/get_feed", "moltbook/get_message"},
	})
	if !strings.Contains(msg, "moltbook/get_feed") {
		t.Fatalf("the action never reached the judge:\n%s", msg)
	}
	if !strings.Contains(msg, "ACTIONS") {
		t.Fatalf("the list is not labelled as actions:\n%s", msg)
	}
}

// The prompt has to say an absent action did not run, or the judge is free to
// read "moltbook ran" as "the post went out".
func TestPromptRulesOutActionsThatAreNotListed(t *testing.T) {
	for _, want := range []string{"DID NOT RUN", "Nine reads do not add up to one write"} {
		if !strings.Contains(turnJudgeSysPrompt, want) {
			t.Fatalf("the judge is never told %q", want)
		}
	}
}

// Both halves of the inability rule, which have to travel together. The judge
// convicted a truthful "knowledge_search is not in my tool set" because nothing
// in the evidence carried the catalog. The first fix said never convict such a
// claim, which then shielded the same agent telling a user a tool was
// unavailable while it sat in the catalog, refusing to call it, and disavowing
// the real documents a forced call returned.
func TestPromptSeparatesAnHonestInabilityFromAFalseOne(t *testing.T) {
	for _, want := range []string{
		"is NOT in the available list, or when no available list was given",
		"Never convict it for having no tool call behind it",
		"AND that tool appears in the available list",
	} {
		if !strings.Contains(turnJudgeSysPrompt, want) {
			t.Fatalf("the judge is never told %q", want)
		}
	}
}

// The judge cannot apply either half without being told what was callable.
func TestEvidenceNamesWhatTheTurnCouldHaveCalled(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request:      "why are my license counts off",
		Reply:        "knowledge_search is not in my callable tool set this turn.",
		CatalogTools: []string{"fetch_url", "knowledge_search", "web_search"},
	})
	if !strings.Contains(msg, "TOOLS THIS TURN COULD CALL, COMPLETE: fetch_url, knowledge_search, web_search") {
		t.Fatalf("the judge is not shown the catalog:\n%s", msg)
	}
	// A host that supplies none must not produce a line the judge could read
	// as an empty catalog, which would convict every honest report at once.
	bare := turnJudgeEvidenceMessage(TurnClaimEvidence{Request: "x", Reply: "y"})
	if strings.Contains(bare, "TOOLS THIS TURN COULD CALL") {
		t.Fatalf("an absent catalog must say nothing:\n%s", bare)
	}
}

// The Cortex shape of the PriorWork gap. A standing thread exists so that
// scheduled work reports into it, so the ordinary thing to say there is a recap
// — and a recap reaches the judge with an empty action list, because the runs
// it describes happened in earlier turns.
//
// Reported live: "Just wrapped today's engagement cycle — three comments landed
// across Tech, Philosophy, and the Palantir thread" was convicted as fabricated,
// and the correction sent the agent to redo work it had already done.
func TestReportsAlreadyFiledAreEvidence(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request:      "how did today go?",
		Reply:        "Just wrapped today's engagement cycle — three comments landed.",
		PriorReports: []string{"Engagement cycle — posted 3 comments (Tech, Philosophy, Palantir)"},
	})
	if !strings.Contains(msg, "Engagement cycle — posted 3 comments") {
		t.Error("the judge must be shown the reports the reply is recapping")
	}
	if !strings.Contains(msg, "EARLIER turns") || !strings.Contains(msg, "KEPT") {
		t.Error("and told why they are absent from the action list, or it convicts on the absence")
	}
	// Silent when there are none: an evidence line about an empty list is a
	// prompt the judge has to reason past on every ordinary turn.
	plain := turnJudgeEvidenceMessage(TurnClaimEvidence{Request: "hi", Reply: "hello"})
	if strings.Contains(plain, "SCHEDULED RUNS") {
		t.Error("a turn with no prior reports must not mention them")
	}
	// The rule has to be in the judge's instructions too — the evidence line
	// alone leaves it to infer what an unexplained section means.
	if !strings.Contains(turnJudgeSysPrompt, "scheduled runs already reported") {
		t.Error("the KEPT list must name recaps of the agent's own standing work")
	}
}

// Producer and opening line only. The bodies of these reports are what make a
// standing thread enormous; the judge needs to know the work happened.
func TestPriorReportsAreNamedNotQuoted(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "morning"},
		{Role: "assistant", Content: "Engagement cycle done.\nPosted 3 comments.\nAll 201.",
			ReportFrom: "Daily engagement"},
		{Role: "assistant", Content: "an ordinary reply with no producer"},
	}}}
	got := turn.priorReportsForJudge()
	if len(got) != 1 {
		t.Fatalf("expected the one report, got %d: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "Daily engagement — Engagement cycle done.") {
		t.Errorf("want producer and opening line, got %q", got[0])
	}
	if strings.Contains(got[0], "All 201") {
		t.Error("the body is what makes these threads enormous; the judge gets the opening")
	}
	if (&chatTurn{}).priorReportsForJudge() != nil {
		t.Error("no session, no reports")
	}
}

// The plain-chat shape of the same gap, and the one a person hit: "we traced
// this in the diagnostic bundle" inside an email the user had just asked for.
// The tracing was real and several turns old; the turn writing it up called
// nothing, so every line of the evidence said nothing happened and the
// correction sent the agent back to redo finished work.
func TestEarlierTurnWorkIsEvidence(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request:       "write that up as an email to the team",
		Reply:         "We traced this in the diagnostic bundle.",
		PriorTurnWork: []string{"fetch_doc", "agents/dispatch"},
	})
	if !strings.Contains(msg, "fetch_doc") || !strings.Contains(msg, "agents/dispatch") {
		t.Error("the judge must be shown what earlier turns ran")
	}
	if !strings.Contains(msg, "BEFORE the turn in front of you") || !strings.Contains(msg, "KEPT") {
		t.Error("and told why they are absent from the action list, or it convicts on the absence")
	}
	plain := turnJudgeEvidenceMessage(TurnClaimEvidence{Request: "hi", Reply: "hello"})
	if strings.Contains(plain, "EARLIER TURNS OF THIS SAME CONVERSATION") {
		t.Error("a turn with no earlier work must not mention it")
	}
	if !strings.Contains(turnJudgeSysPrompt, "already did in earlier turns") {
		t.Error("the KEPT list must name recaps of the conversation's own earlier work")
	}
	// The other half of the reported case: the reply IS the email. What it
	// narrates is the content that was asked for, not a report of this turn.
	if !strings.Contains(turnJudgeSysPrompt, "document the user asked the assistant to write") {
		t.Error("the KEPT list must exempt a requested draft's own narration")
	}
}

// Distinct labels off the persisted trace, successes only: a failed call is not
// work a later reply may claim, on the same reasoning as the step ledger.
func TestPriorTurnWorkReadsThePersistedTrace(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "look at the bundle"},
		{Role: "assistant", Content: "traced it", ToolCalls: []PersistedToolCall{
			{Name: "fetch_doc", Result: "ok"},
			{Name: "fetch_doc", Result: "ok again"},
			{Name: "agents", Args: map[string]any{"action": "dispatch"}, Result: "ok"},
			{Name: "workspace", Args: map[string]any{"action": "write"}, Err: "permission denied"},
		}},
		{Role: "user", Content: "write it up"},
	}}}
	got := turn.priorTurnWorkForJudge()
	want := []string{"fetch_doc", "agents/dispatch"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
	if (&chatTurn{}).priorTurnWorkForJudge() != nil {
		t.Error("no session, no earlier work")
	}
}
