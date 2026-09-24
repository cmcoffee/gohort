// The end-of-turn judge, and the filter that decides when it is worth paying
// for.
//
// The phrase-list guards must be precise, because they act directly: a false
// positive re-prompts an honest answer. The pre-filter can be sloppy, because
// all a false positive costs is one small model call that comes back KEPT.
// These tests pin that asymmetry — the filter is deliberately over-inclusive
// where the guards are careful, and evidence-shaped rather than wording-shaped
// so it does not simply re-select the turns the guards already catch.
package core

import (
	"context"
	"strings"
	"testing"
)

func TestTheJudgeLooksWhenActionsAndWordsCouldDisagree(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   TurnClaimEvidence
	}{
		{"said something, did nothing", TurnClaimEvidence{
			Reply: "On it — let me grab some reference photos and composite them in.",
		}},
		{"a bare answer with no tools is still looked at", TurnClaimEvidence{
			// The 22:41 turn: "Wiwee, try again" answered in 66 characters with
			// zero tool calls. No wording gate here on purpose — whatever it
			// said, nothing was tried, and only a reader can tell whether the
			// sentence admits that.
			Reply: "Yeah, that one's still not working out.",
		}},
		{"did something and it failed", TurnClaimEvidence{
			Reply: "Here's the picture you asked for.", ToolCalls: []string{"image"}, ToolErrors: 1,
		}},
		{"produced nothing and is delivering nothing", TurnClaimEvidence{
			Reply: "Here's you, wasting away in the garage like Craig ordered.",
			// The caption case. It slipped the noun rule entirely; the filter
			// never reads the words, only that an image tool ran and no file
			// is going out.
			ToolCalls: []string{"generate_image"}, Delivered: 0,
		}},
	} {
		if !turnClaimWorthJudging(c.ev) {
			t.Errorf("%s: must be judged", c.name)
		}
	}
}

func TestTheJudgeStaysOutOfTheWayOtherwise(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   TurnClaimEvidence
	}{
		{"nothing was said", TurnClaimEvidence{Reply: "   ", ToolCalls: nil}},
		{"work ran, worked, and shipped", TurnClaimEvidence{
			Reply: "Here's the render.", ToolCalls: []string{"image"}, Delivered: 1,
		}},
		{"work ran and nothing about it produces files", TurnClaimEvidence{
			Reply: "Tokyo is 14°C and raining.", ToolCalls: []string{"web_search"},
		}},
	} {
		if turnClaimWorthJudging(c.ev) {
			t.Errorf("%s: must not cost a model call", c.name)
		}
	}
}

func TestABackgroundedTurnIsJudgedForPlumbingNotForClaims(t *testing.T) {
	// This arm used to skip outright, because "I'll report back" is TRUE and
	// convicting it would flag the exact reply detachedNotice asks for. That
	// reasoning held for the claim and made the judge blind to the other thing
	// these turns do: a detach is where plumbing leaks, because the model has
	// just been handed a task id and a paragraph about how the work is run.
	//
	// Observed, delivered to a user: "The image edit task is still running in
	// the background (task a79c771f5f35a9f6ef0489d0)."
	ev := TurnClaimEvidence{
		Reply:        "The image edit task is still running in the background (task a79c771f5f35a9f6ef0489d0).",
		ToolCalls:    []string{"image"},
		Backgrounded: true,
	}
	if !turnClaimWorthJudging(ev) {
		t.Fatal("a backgrounded turn must be looked at — it is where plumbing leaks")
	}
	// Machinery alone convicts, with no claim attached.
	cfg := AgentLoopConfig{TurnClaimJudge: func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Machinery: "still running in the background"}, true
	}}
	v, convicted := judgeTurnClaim(cfg, ev)
	if !convicted {
		t.Fatal("a machinery finding must stand on its own")
	}
	if v.Unkept {
		t.Error("machinery is not a false claim — the reply was true")
	}
	// And a clean backgrounded turn still walks.
	cfg.TurnClaimJudge = func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{}, true
	}
	if _, convicted := judgeTurnClaim(cfg, ev); convicted {
		t.Error("a clean reply must not be convicted by either arm")
	}
}

func TestAJudgeThatCannotAnswerConvictsNobody(t *testing.T) {
	ev := TurnClaimEvidence{Reply: "On it — let me grab those."}

	// No hook at all is the shape every host had before this existed.
	if _, convicted := judgeTurnClaim(AgentLoopConfig{}, ev); convicted {
		t.Error("no judge must convict nobody")
	}
	// ok=false is "I could not answer", NOT "kept" and NOT "unkept". A judge
	// that errored has cleared nothing and accused nobody.
	cfg := AgentLoopConfig{TurnClaimJudge: func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Unkept: true, Claim: "x"}, false
	}}
	if _, convicted := judgeTurnClaim(cfg, ev); convicted {
		t.Error("a judge that could not answer must not convict")
	}
	// An acquittal is an acquittal.
	cfg.TurnClaimJudge = func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Unkept: false}, true
	}
	if _, convicted := judgeTurnClaim(cfg, ev); convicted {
		t.Error("KEPT must not convict")
	}
	// And a real conviction carries its quote through.
	cfg.TurnClaimJudge = func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Unkept: true, Claim: "On it", Why: "no tool ran"}, true
	}
	v, convicted := judgeTurnClaim(cfg, ev)
	if !convicted || v.Claim != "On it" || v.Why != "no tool ran" {
		t.Errorf("a conviction must carry its quote and reason, got %+v convicted=%v", v, convicted)
	}
}

func TestTheJudgeIsNeverConsultedWhenTheFilterSaysNo(t *testing.T) {
	// The filter is what keeps this from being a tax on every reply.
	called := false
	cfg := AgentLoopConfig{TurnClaimJudge: func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		called = true
		return TurnClaimVerdict{Unkept: true, Claim: "x"}, true
	}}
	judgeTurnClaim(cfg, TurnClaimEvidence{Reply: "Here's the render.", ToolCalls: []string{"image"}, Delivered: 1})
	if called {
		t.Error("a turn that delivered what it made must not cost a model call")
	}
}

func TestMissingEvidenceReadsTheSuspiciousWay(t *testing.T) {
	// A host that wires no DeliveredCount reports zero deliveries, which makes
	// a delivery claim MORE suspect rather than less. Absent evidence must
	// never quietly acquit.
	if n := (AgentLoopConfig{}).deliveredCount(); n != 0 {
		t.Errorf("absent delivery count = %d, want 0", n)
	}
	if (AgentLoopConfig{}).backgrounded() {
		t.Error("absent background signal must not excuse a promise")
	}
}

// The false positive this exists to stop: a machine step went and searched,
// the reply reported what it found, and the loop's own tool list was empty —
// because a step runs before the loop, on a session of its own. Every arm of
// the evidence said nothing happened, so the judge convicted a reply that was
// true and the turn retracted it.
func TestWorkDoneByAStepCountsAsTheTurnsWork(t *testing.T) {
	stepOnly := TurnClaimEvidence{
		Reply:     "Based on the Confluence research, here's the answer:",
		PriorWork: []string{"a step ran confluence_search"},
	}
	if !stepOnly.TurnDidWork() {
		t.Error("a turn whose step searched did work, whatever the loop saw")
	}
	if turnClaimWorthJudging(stepOnly) {
		t.Error("the \"said something, did nothing\" arm must not fire on a turn that did something")
	}

	// And the arm still fires when nothing ran anywhere, which is the class
	// it was written for.
	if !turnClaimWorthJudging(TurnClaimEvidence{Reply: "Here you go!"}) {
		t.Error("a reply with no work behind it anywhere is still worth a look")
	}

	// A step that FAILED is not work the reply may claim: the host records
	// only what succeeded, so an empty PriorWork keeps the arm live.
	if !turnClaimWorthJudging(TurnClaimEvidence{Reply: "Based on the research…", PriorWork: nil}) {
		t.Error("no recorded step work means the turn is still worth judging")
	}
}

// A grouped tool's read and its write share a name. If the evidence carries
// only names, "moltbook ran nine times" is consistent with a reply claiming
// three posts — which is how a fire reported three comments it never made.
func TestProducerMatchReadsTheToolHalfOfALabel(t *testing.T) {
	if !turnRanProducer([]string{"image/edit"}) {
		t.Fatal("a labelled producer call stopped counting as one")
	}
	if !turnRanProducer([]string{"moltbook/get_feed", "download_video"}) {
		t.Fatal("a bare producer name alongside labels stopped counting")
	}
	if turnRanProducer([]string{"videoconference/join"}) {
		t.Fatal("matched a tool whose name merely starts with a producer's")
	}
	if turnRanProducer([]string{"moltbook/get_feed", "workspace/head"}) {
		t.Fatal("non-producers counted as producers")
	}
}

// The turn that went unjudged: tools ran, none failed, nothing was expected to
// be delivered. Every evidence arm says there is nothing to look at, and on an
// interactive turn that is right — a person reads the reply. Unattended, it is
// how a false report becomes an undisputed transcript.
func TestUnattendedTurnIsJudgedEvenWhenTheEvidenceLooksFine(t *testing.T) {
	clean := TurnClaimEvidence{
		Request:   "post the daily comments",
		Reply:     "Total: 3 comments posted successfully.",
		ToolCalls: []string{"moltbook/get_feed", "moltbook/get_feed", "moltbook/get_message"},
	}
	if turnClaimWorthJudging(clean) {
		t.Fatal("an attended clean turn should stay unjudged; the filter is meant to be narrow there")
	}
	clean.Unattended = true
	if !turnClaimWorthJudging(clean) {
		t.Fatal("an unattended turn went unjudged — the fire that reported three posts it never made")
	}
}

// Unattended widens the pre-filter, and only that. An empty reply is still not
// judged: there is no claim in it to be false.
func TestUnattendedStillNeedsAReply(t *testing.T) {
	if turnClaimWorthJudging(TurnClaimEvidence{Reply: "   ", Unattended: true}) {
		t.Fatal("judged a turn with no reply")
	}
}

// Earlier turns' work is context for the VERDICT, never a reason to skip the
// judge. A turn that ran nothing itself is still the turn worth looking at:
// the point of showing the judge what came before is that it can then tell a
// true recap from an invented one, which it cannot do if it is never asked.
func TestEarlierTurnWorkDoesNotSkipTheJudge(t *testing.T) {
	ev := TurnClaimEvidence{
		Request:       "write that up as an email",
		Reply:         "We traced this in the diagnostic bundle.",
		PriorTurnWork: []string{"fetch_doc"},
	}
	if ev.TurnDidWork() {
		t.Error("work done in an EARLIER turn is not work this turn did")
	}
	if !turnClaimWorthJudging(ev) {
		t.Error("a turn that ran nothing must still reach the judge")
	}
}

// A failed call now carries its outcome in the label. turnRanProducer matches
// on the tool half, so it has to cut the annotation off first — otherwise a
// failed image call stops counting as a producer and the pre-filter goes quiet
// on exactly the turn most likely to claim a picture it never made.
func TestProducerDetectionSurvivesTheFailureAnnotation(t *testing.T) {
	if !turnRanProducer([]string{`image/edit [FAILED: backend needs two source images]`}) {
		t.Error("a failed image call is still a producer that ran")
	}
	if !turnRanProducer([]string{`generate_image [FAILED: no provider configured]`}) {
		t.Error("a bare-name producer must survive the annotation too")
	}
	if !turnRanProducer([]string{"image/edit"}) {
		t.Error("an unannotated label must keep working")
	}
	if turnRanProducer([]string{`web_search [FAILED: timeout]`, "moltbook/get_feed"}) {
		t.Error("a non-producer must not be promoted by the annotation")
	}
	// The exact-name rule still holds: no prefix matching.
	if turnRanProducer([]string{"videoconference/join"}) {
		t.Error("videoconference is not video")
	}
}

// The note is what the judge reads to tell one failure from another, so it has
// to survive the trip: one line, no Error: prefix, bounded.
func TestFailureNoteIsOneShortLine(t *testing.T) {
	if got := toolFailureNote(`Error: missing required arg "content"`); got != `missing required arg "content"` {
		t.Errorf("note = %q", got)
	}
	if got := toolFailureNote("first line\nsecond line"); got != "first line" {
		t.Errorf("a multi-line error must be cut to its first line; got %q", got)
	}
	if got := toolFailureNote("   "); got != "no detail" {
		t.Errorf("an empty error must still say something; got %q", got)
	}
	long := toolFailureNote(strings.Repeat("x", 500))
	if len([]rune(long)) > 95 {
		t.Errorf("note not bounded: %d chars", len([]rune(long)))
	}
}

// The judges were shown WHICH tools ran and never what came back, so a reply
// quoting framework output — the most reliable sentence it can write — read
// exactly like an invention. These cover the excerpting that closes that, and
// the one property that keeps the fix from becoming a new failure mode: the
// excerpt is evidence FOR a reply, never against it.

// A framework tool result leads with the outcome and appends the notes that
// matter: what it dropped, what did not resolve, what already-open sessions
// will do. Head-only truncation cuts exactly those. Both sentences that would
// have acquitted the replies in the motivating session sat past a 300-char
// head.
func TestToolResultExcerptKeepsTheTail(t *testing.T) {
	result := "AGENT_UPDATED ok. id=6d23ece1 name=\"Wren\". " +
		strings.Repeat("NOTE: some middle detail nobody needs. ", 60) +
		"WARNING: these allowed_tools entries match no known tool and were dropped: recall, remember."
	ex := toolResultExcerpt(result, toolOutputExcerptMax)
	if len(ex) > toolOutputExcerptMax+64 {
		t.Errorf("excerpt ran past its budget: %d chars", len(ex))
	}
	if !strings.Contains(ex, "AGENT_UPDATED ok") {
		t.Error("the head has to say WHICH call this was")
	}
	if !strings.Contains(ex, "dropped: recall, remember") {
		t.Error("the tail is the whole point: that warning is what a reply quotes, and convicting the quote is the bug this closes")
	}
	if !strings.Contains(ex, "elided") {
		t.Error("an abridged result must say it was abridged, or a judge reads a gap as an absence")
	}
}

// A short result is passed through whole — no elision marker on something that
// never needed cutting.
func TestShortToolResultIsNotAbridged(t *testing.T) {
	if got := toolResultExcerpt("Updated machine \"acme_investigation\" with 5 phases.", toolOutputExcerptMax); got != "Updated machine \"acme_investigation\" with 5 phases." {
		t.Errorf("a short result must pass through verbatim, got %q", got)
	}
}

// Over budget, the OLDEST go: a reply's claims rest on the last things that
// happened. And the judge is told how many were left out, because a silent gap
// is indistinguishable from a call that returned nothing — which is the exact
// inference that convicts a true reply.
func TestToolOutputEvidenceDropsOldestAndSaysSo(t *testing.T) {
	var outs []string
	for i := 0; i < 80; i++ {
		outs = append(outs, "tool/act: "+strings.Repeat("x", 400))
	}
	outs = append(outs, "update_agent: the newest result, which the reply is about")
	block := TurnClaimEvidence{ToolOutputs: outs}.ReturnsBlock()
	if !strings.Contains(block, "the newest result, which the reply is about") {
		t.Error("the newest result must survive the budget; it is what the reply reports on")
	}
	if !strings.Contains(block, "omitted for length") || !strings.Contains(block, "not evidence they returned nothing") {
		t.Error("an omission must be declared AND disarmed, or the judge convicts on the gap")
	}
	if len(block) > toolOutputEvidenceBudget*2 {
		t.Errorf("evidence block blew its budget: %d chars", len(block))
	}
}

// Nothing ran, nothing rendered: an empty block rather than a heading over an
// empty list, which reads as a positive claim that the calls returned nothing.
func TestNoToolOutputsRendersNothing(t *testing.T) {
	if (TurnClaimEvidence{}).ReturnsBlock() != "" {
		t.Error("no outputs must render no block at all")
	}
}

// A turn that ran nothing is told to REWRITE, never to act. The offer to "do it
// NOW with a real tool call" is how a scheduled greeting convicted for a
// well-wish ended up texting the owner's phone: on a turn that did nothing, the
// only way to make a sentence true is to go and do something.
func TestACorrectionOnATurnThatRanNothingOnlyAsksForARewrite(t *testing.T) {
	v := TurnClaimVerdict{Unkept: true, Claim: "Wishing you a pleasant evening.", Why: "the turn did not do it"}
	idle := unkeptClaimCorrection(v, false)
	if strings.Contains(idle, "do it NOW") || strings.Contains(idle, "real tool call") {
		t.Errorf("a turn that ran nothing must not be invited to act:\n%s", idle)
	}
	if !strings.Contains(idle, "Do not call a tool") || !strings.Contains(idle, v.Claim) {
		t.Errorf("the rewrite correction should forbid acting and quote the claim:\n%s", idle)
	}
	// A turn that did work keeps the offer: the claim there is usually its
	// last step left undone.
	if busy := unkeptClaimCorrection(v, true); !strings.Contains(busy, "do it NOW with a real tool call") {
		t.Errorf("a turn that did work should still be offered the tool call:\n%s", busy)
	}
}

// The hard half of the rewrite-only rule: after a correction on a turn that ran
// nothing, no tool is OFFERED, and a call that arrives anyway is refused before
// it runs. The live case: a scheduled greeting was convicted, and the retry
// called notify_owner.
func TestARewriteOnlyTurnRunsNoTools(t *testing.T) {
	fired := 0
	notify := AgentToolDef{
		Tool: Tool{Name: "notify_owner", Description: "text the owner", Parameters: map[string]ToolParam{"text": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			fired++
			return "Sent.", nil
		},
	}
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "Wishing you a pleasant evening."},
		{ToolCalls: []ToolCall{{ID: "c1", Name: "notify_owner", Args: map[string]any{"text": "Good evening!"}}}},
		{Content: "Good evening!", Repeat: true},
	}}
	judged := 0
	app := &AppCore{LLM: stub, LeadLLM: stub}
	resp, _, err := app.RunAgentLoop(context.Background(),
		[]Message{{Role: "user", Content: "Say hello to the user."}},
		AgentLoopConfig{
			SystemPrompt: "Be warm.",
			Tools:        []AgentToolDef{notify},
			MaxRounds:    6,
			TurnClaimJudge: func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
				judged++
				if judged == 1 {
					return TurnClaimVerdict{Unkept: true, Claim: "Wishing you a pleasant evening.", Why: "the turn did not do it"}, true
				}
				return TurnClaimVerdict{}, true
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if fired != 0 {
		t.Fatalf("a correction on a turn that ran nothing must not run a tool; notify_owner fired %d time(s)", fired)
	}
	if n := len(stub.Config(0).Tools); n == 0 {
		t.Fatal("the first round should have been offered the tool, or this test proves nothing")
	}
	for i := 1; i < stub.Calls(); i++ {
		if n := len(stub.Config(i).Tools); n != 0 {
			t.Errorf("call %d after the correction was offered %d tool(s)", i, n)
		}
	}
	refused := false
	for _, m := range stub.Sent(2) {
		for _, r := range m.ToolResults {
			if r.ID == "c1" && r.IsError && strings.Contains(r.Content, "Not run: this turn is only rewriting its reply") {
				refused = true
			}
		}
	}
	if !refused {
		t.Error("the refused call should be answered as not run")
	}
	if resp == nil || resp.Content != "Good evening!" {
		t.Errorf("the rewritten reply should be what goes out, got %+v", resp)
	}
}
