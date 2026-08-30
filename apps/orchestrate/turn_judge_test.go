// The judge's contract with the model: what it must be told, and what it does
// with each shape of answer.
//
// The parsing side is where a live judge actually misbehaves. A small model
// asked for JSON returns prose, or an UNKEPT with nothing quoted, or a verdict
// spelled differently — and each of those, read wrong, retracts a reply that
// was fine.
package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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
