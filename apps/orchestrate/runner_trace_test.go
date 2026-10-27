package orchestrate

import (
	"bytes"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/silent"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestBareAskIsNeverDeduped — a bare ask_user (no options) renders as prose
// through the same emitter replies use, and that emitter suppresses a
// near-duplicate of the last streamed bubble.
//
// For a reply that is right; a repeat is noise. For an ASK it is fatal. The
// model routinely streams a lead-in ("Several things are ambiguous:") and then
// captures a question that BEGINS with those same words, which scores 1.0 on
// isNearDuplicate's longest-common-prefix test and drops the whole ask.
// AwaitingUserConfirm is still set, so the turn ends parked on a question the
// user never saw: a lead-in, a colon, and nothing. Seen live in Guides.
//
// runPlan is far too large to drive from a unit test, so this pins the wiring
// at the source: the ask path must reach for the raw emitter.
func TestBareAskIsNeverDeduped(t *testing.T) {
	src := readOwnSource(t, "runner.go")
	const marker = "if len(pr.capturedOptions) == 0 {"
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatal("the bare-ask branch moved; re-point this test rather than deleting it")
	}
	// The branch is short; look at the block that follows it.
	block := src[i : i+900]
	if !strings.Contains(block, "pr.emitBubble(pr.capturedQuest)") {
		t.Error("the bare-ask path must emit through emitBubble (no dedup) — emitCapturedAsBubble drops an ask whose text repeats the streamed lead-in")
	}
	if strings.Contains(block, "pr.emitCapturedAsBubble(pr.capturedQuest)") {
		t.Error("the bare-ask path is going through the near-duplicate guard again; a swallowed question leaves the turn parked with nothing on screen")
	}
}

// TestNearDuplicateCatchesAPrefixLeadIn documents the exact scoring that made
// the bug possible, so a future change to isNearDuplicate cannot quietly make
// this test's premise false while it still passes.
func TestNearDuplicateCatchesAPrefixLeadIn(t *testing.T) {
	leadIn := "Let me get your attention on what this guide should cover before I start writing. Several things are ambiguous:"
	ask := leadIn + " 1) Who is the audience? 2) Which product? 3) How long should it be?"
	if !isNearDuplicate(ask, leadIn) {
		t.Skip("isNearDuplicate no longer scores a streamed lead-in as a duplicate of the ask that repeats it; the ask path's guard-free emit is now belt-and-braces")
	}
}

// A retry that changes only punctuation is the reply already on screen. The
// loop retracted the retry's stream as the same reply, and the host showed it
// anyway: its prefix test stopped at the dash. The pair is the 2026-10-25
// goodnight, word for word.
func TestARetryThatOnlyChangesPunctuationIsTheShownReply(t *testing.T) {
	shown := "Night. Lights out — I'll be here when you wake up."
	retry := "Night. Lights out, I'll be here when you wake up."
	if !repeatsWithoutAdding(retry, shown) {
		t.Errorf("a dash-for-comma retry was shown as a second bubble")
	}
	if repeatsWithoutAdding("Night. Lights out, I'll be here when you wake up. One thing before you go: your 9am with the dentist moved to 10:30, and the reminder is set for 9:45.", shown) {
		t.Errorf("a retry that says more must still be shown")
	}
}

// A turn the model closed with stay_silent is saved as having stayed quiet,
// and why. It was saved as "This reply didn't finish, the turn finished
// without producing a reply", which says the turn failed.
func TestAQuietTurnIsSavedAsQuietWithItsReason(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	sess := ChatSession{ID: "s1", AgentID: "wren", Messages: []ChatMessage{{Role: "user", Content: "ok thanks"}}}
	turn := &chatTurn{turnClosed: true, silenceReason: "they were only saying thanks"}
	turn.recordToolCall(toolCallRecord{Name: "stay_silent", Args: map[string]any{"reason": "they were only saying thanks"}, Result: "Silence acknowledged."})
	persistQuietTurn(&sess, udb, turn)

	got, ok := loadChatSession(udb, "wren", "s1")
	if !ok || len(got.Messages) != 2 {
		t.Fatalf("the quiet turn should be saved after the request: %+v", got.Messages)
	}
	last := got.Messages[1]
	if last.Role != "assistant" || last.Content != "" {
		t.Errorf("a quiet turn has no reply text: %+v", last)
	}
	if last.Label != "Stayed quiet: they were only saying thanks" {
		t.Errorf("label = %q", last.Label)
	}
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].Label != last.Label {
		t.Errorf("the silence should be labelled in the tool runs too: %+v", last.ToolCalls)
	}
	if strings.Contains(last.Content+last.Label, "didn't finish") {
		t.Error("a quiet turn is not an unfinished one")
	}
	if quietLabel("  ") != "Stayed quiet (no reason given)" {
		t.Errorf("no reason = %q", quietLabel(""))
	}
}

// The page is told about the silence as it happens. The label went to the
// turn's current bubble, which the round had already cleared by the time the
// turn was saved, so it reached the page only on a reload.
func TestAQuietTurnIsLabelledLive(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	sess := ChatSession{ID: "s1", AgentID: "wren", Messages: []ChatMessage{{Role: "user", Content: "ok thanks"}}}
	var live bytes.Buffer
	turn := &chatTurn{turnClosed: true, silenceReason: "they were only saying thanks", sse: &sseWriter{live: &live}}
	turn.setCurrentMsgID("") // the round that called stay_silent has settled
	persistQuietTurn(&sess, udb, turn)

	out := live.String()
	if !strings.Contains(out, `"kind":"message"`) || !strings.Contains(out, `"role":"assistant"`) {
		t.Errorf("the silence should open its own message live:\n%s", out)
	}
	if !strings.Contains(out, `"kind":"message_label"`) || !strings.Contains(out, `"label":"Stayed quiet: they were only saying thanks"`) {
		t.Errorf("the page must be told to label it:\n%s", out)
	}
}

// stay_silent records the reason it was given, for that label.
func TestStaySilentKeepsItsReason(t *testing.T) {
	sess := &ToolSession{}
	if _, err := new(silent.StaySilentTool).RunWithSession(map[string]any{"reason": " the picture is the reply "}, sess); err != nil {
		t.Fatal(err)
	}
	if !sess.Silenced || sess.SilencedReason != "the picture is the reply" {
		t.Errorf("silenced=%v reason=%q", sess.Silenced, sess.SilencedReason)
	}
}
