// How the loop rations its silent re-prompts.
//
// It used to be one counter shared by every guard, which failed two ways.
// Corrections of UNRELATED kinds disarmed each other — an orphaned tool tag
// early in a turn meant a false delivery claim later went uncorrected, though
// nothing about the first says anything about the second. And a guard that
// spent the budget on the SAME problem twice simply stopped firing, so the
// content it had ruled wrong shipped anyway.
//
// Observed: the phantom-delivery guard fired 1/2, then 2/2, on one invented
// filename; the model rewrote the same claim both times; the third went to the
// user — a claim the framework had already judged false, twice.
package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOneKindRunningOutLeavesTheOthersArmed(t *testing.T) {
	b := newCorrectionBudget()

	for i := 1; i <= maxCorrectionsPerKind; i++ {
		if !b.available(correctionPhantomDelivery) {
			t.Fatalf("attempt %d must be allowed", i)
		}
		if n := b.spend(correctionPhantomDelivery); n != i {
			t.Errorf("spend returned %d, want %d", n, i)
		}
	}
	if b.available(correctionPhantomDelivery) {
		t.Error("a third attempt on the same fault must be refused")
	}
	// The whole point: an unrelated guard is untouched.
	if !b.available(correctionOrphanedXML) {
		t.Error("an unrelated kind must still be armed")
	}
	if !b.available(correctionGiveUp) {
		t.Error("an unrelated kind must still be armed")
	}
}

func TestATurnWideCeilingStillStopsASpiral(t *testing.T) {
	// Per-kind budgets must not add up to unbounded. A turn going wrong in many
	// ways at once still has to terminate.
	b := newCorrectionBudget()
	kinds := []string{
		correctionOrphanedXML, correctionPhantomDelivery, correctionFakeToolCode,
		correctionAnnouncedCall, correctionToolMention, correctionCollapse, correctionGiveUp,
	}
	spent := 0
	for _, k := range kinds {
		for i := 0; i < maxCorrectionsPerKind; i++ {
			if !b.available(k) {
				continue
			}
			b.spend(k)
			spent++
		}
	}
	if spent != maxCorrectionsPerTurn {
		t.Errorf("spent %d corrections, want the turn ceiling of %d", spent, maxCorrectionsPerTurn)
	}
	for _, k := range kinds {
		if b.available(k) {
			t.Errorf("%s still armed past the turn ceiling", k)
		}
	}
}

func TestExhaustionBreadcrumbsExactlyOnce(t *testing.T) {
	// A guard that goes quiet without a word is indistinguishable from one that
	// never fired — the silent drop this codebase keeps paying for. But it is
	// checked every round after, so it must not repeat either.
	b := newCorrectionBudget()
	if b.exhausted(correctionGiveUp) {
		t.Error("a kind with budget left is not exhausted")
	}
	for i := 0; i < maxCorrectionsPerKind; i++ {
		b.spend(correctionGiveUp)
	}
	if !b.exhausted(correctionGiveUp) {
		t.Error("the first check after running out must report it")
	}
	for i := 0; i < 5; i++ {
		if b.exhausted(correctionGiveUp) {
			t.Fatal("exhaustion must breadcrumb once, not once per round")
		}
	}
}

func TestTheSubstitutedReplyIsTrueAndBlamesNobody(t *testing.T) {
	got := UnfulfilledDeliveryReply([]string{"the image"})

	if !strings.Contains(got, "the image") {
		t.Errorf("must name what was claimed: %q", got)
	}
	// It has to actually correct the claim, not soften it.
	if !strings.Contains(got, "nothing to send") {
		t.Errorf("must say plainly that there is no file: %q", got)
	}
	// The failure that started this whole line of work was a reply that
	// apologized for the REQUEST and asked the user to rephrase. The request
	// was fine every time.
	for _, blame := range []string{"rephrase", "try asking", "didn't understand", "could you clarify"} {
		if strings.Contains(strings.ToLower(got), blame) {
			t.Errorf("must not blame the request (%q): %q", blame, got)
		}
	}
	// A filename ref reads just as well as a noun phrase.
	if named := UnfulfilledDeliveryReply([]string{"garage.png"}); !strings.Contains(named, "garage.png") {
		t.Errorf("must name a filename ref too: %q", named)
	}
	// And it never renders an empty subject.
	if bare := UnfulfilledDeliveryReply(nil); strings.Contains(bare, "sending ,") || strings.Contains(bare, "sending .") {
		t.Errorf("no refs must still read cleanly: %q", bare)
	}
}

// A tool call written as a BRACKETED DIRECTIVE. The shape that reached a
// contact verbatim: "[CALL_FUNCTION] fetch_image: find a photo of …" wrapped in
// seven hundred characters of narration — invented progress, second thoughts,
// "OK found a good one" — none of which had happened.

func TestABracketedCallDirectiveIsMarkupNotProse(t *testing.T) {
	// The prose scan is gated off for a long body that finished cleanly, so a
	// narrated call this size was never going to be extracted as prose. It has
	// to be recognized as markup, where length is irrelevant.
	long := "[CALL_FUNCTION] fetch_image: find a photo of Shazz Barbaric first. " +
		strings.Repeat("I'll grab his actual face from the thumbnail or wherever we found him before. ", 10)
	if !containsFakeToolCodeBlock(long) {
		t.Fatal("a bracketed call directive must be caught however long the reply is")
	}
	if got := extractFakeToolCodeName(long); got != "fetch_image" {
		t.Errorf("attempted tool name = %q, want fetch_image", got)
	}
	for _, variant := range []string{
		"[TOOL_CALL] image: make it spooky",
		"[FUNCTION_CALL]: web_search",
		"[call_function] find_image: a cat",
	} {
		if !containsFakeToolCodeBlock(variant) {
			t.Errorf("not caught: %q", variant)
		}
	}
}

func TestOrdinaryBracketsAreNotCallDirectives(t *testing.T) {
	for _, plain := range []string{
		"I'll [probably] need a minute.",
		"[ATTACH: photo.png]",
		"See section [4] of the report.",
		"[BACKGROUND TASK FINISHED] The work you started earlier is done.",
	} {
		if containsFakeToolCodeBlock(plain) {
			t.Errorf("false positive on ordinary text: %q", plain)
		}
	}
}

func TestTheDirectiveIsStrippedAndTheSentenceSurvives(t *testing.T) {
	in := "Working on it.\n[CALL_FUNCTION] fetch_image: a photo of the bridge\nI'll have that shortly."
	out := stripFakeToolCodeBlocks(in)
	if strings.Contains(out, "CALL_FUNCTION") {
		t.Errorf("the directive must not reach the user:\n%s", out)
	}
	for _, keep := range []string{"Working on it.", "I'll have that shortly."} {
		if !strings.Contains(out, keep) {
			t.Errorf("stripping took more than the directive — lost %q:\n%s", keep, out)
		}
	}
}

// The STOP verdict is a contract carried in prose, which is the shape that
// broke the paste marker.
//
// Three places build one and one place recognises it, across two packages. A
// non-match is a LEGAL outcome there — a tool result that is not a guard
// verdict is the common case — so a producer whose wording drifts does not
// fail, it just stops being recognised, and the loop starts counting guard
// verdicts as successful tool calls.
//
// This scans the source because orchestrate cannot see an unexported const and
// should not gain an exported one for a string.

// stopProducerRE finds a literal that OPENS a STOP verdict. Only a literal:
// a comment mentioning one is not a producer.
var stopProducerRE = regexp.MustCompile(`"STOP[:\s]`)

func TestEveryGuardStopUsesTheSharedPrefix(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", "node_modules", "vendor":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !stopProducerRE.MatchString(line) {
				continue
			}
			// The shared const is how a producer SHOULD spell it; a literal is
			// only acceptable when it carries the exact prefix the matcher
			// looks for.
			// A comment is not a producer.
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") {
				continue
			}
			// Acceptable two ways: built from the shared const, or spelled
			// with the exact opening the matcher anchors on.
			if strings.Contains(line, "guardStopPrefix") {
				continue
			}
			if guardStopRE.MatchString(strings.ReplaceAll(strings.Trim(strings.TrimSpace(line), "\t"), `"`, "\n")) {
				continue
			}
			offenders = append(offenders, strings.TrimSpace(path+": "+strings.TrimSpace(line)))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, o := range offenders {
		if len(o) > 160 {
			o = o[:160] + "…"
		}
		t.Errorf("a STOP verdict that isGuardStopResult will not recognise:\n  %s", o)
	}
}

// And the matcher really does recognise what the producers build.
func TestTheMatcherRecognisesAProducedVerdict(t *testing.T) {
	for _, s := range []string{
		guardStopPrefix + "you have already called 'x' with these exact arguments 3 times",
		"[untrusted content follows]\n" + guardStopPrefix + "you have tried 4 different ways past an enforced limit",
		// The three the old Contains("STOP: you") scan missed.
		guardStopPrefix + "'send_email' was NOT called. It has already run 6 time(s)",
		"fence\n" + guardStopPrefix + "'x' was NOT called. Its id is \"z\", an identifier nothing produced",
		guardStopPrefix + "you've dispatched \"Comedian\" 120 times this turn",
	} {
		if !isGuardStopResult(s) {
			t.Errorf("a real verdict was not recognised: %q", s)
		}
	}
	// Ordinary output that merely mentions stopping is not a verdict.
	for _, s := range []string{
		"The deploy will STOP if the check fails.",
		"stop: you should see the log",
		// Mid-sentence, which the old Contains-anywhere scan would have taken.
		"the log said STOP: you can ignore that line",
		"",
	} {
		if isGuardStopResult(s) {
			t.Errorf("ordinary output was taken for a guard verdict: %q", s)
		}
	}
}

// The reported turn: a lead spent its whole output allowance thinking, emitted
// 133 characters and no tool call, and the loop read that as a finished turn —
// exit=respond_directly at rounds_used=1/30, twice in a row, on a promise.
//
// stop_reason said so both times. Nothing was listening: providerRefused covers
// safety reasons only, and llm_openai's finish_reason==length check requires
// content AND tool calls to both be empty, which is exactly what a truncated
// turn is not.
func TestATruncatedReplyIsNotAFinishedOne(t *testing.T) {
	for _, stop := range []string{"max_tokens", "length", "MAX_TOKENS", " length ", stopInterrupted} {
		resp := &Response{Content: "No — I said I was writing it. Doing it now.", StopReason: stop}
		if !responseWasTruncated(resp) {
			t.Errorf("stop_reason=%q did not read as truncated", stop)
		}
	}
}

// The distinction that makes it worth having: a truncated turn normally HAS
// content. A guard that fires only on an empty response misses the case that
// actually strands the user.
func TestTruncationIsDetectedWithContentPresent(t *testing.T) {
	withContent := &Response{Content: "Doing it now.", StopReason: "max_tokens"}
	if !responseWasTruncated(withContent) {
		t.Error("a truncated reply that carries a preamble was not detected")
	}
}

// A clean finish must not be re-prompted — that would spend a round and an LLM
// call on every normal turn in the system.
func TestACleanFinishIsLeftAlone(t *testing.T) {
	for _, stop := range []string{"stop", "end_turn", "tool_use", "", "pause_turn"} {
		if responseWasTruncated(&Response{Content: "Here you go.", StopReason: stop}) {
			t.Errorf("stop_reason=%q was misread as truncated", stop)
		}
	}
	if responseWasTruncated(nil) {
		t.Error("a nil response read as truncated")
	}
}

// Truncation and refusal are different terminal conditions and must not both
// claim the same response.
func TestTruncationAndRefusalDoNotOverlap(t *testing.T) {
	refusal := &Response{StopReason: "safety"}
	if responseWasTruncated(refusal) {
		t.Error("a safety stop read as truncated")
	}
	if !providerRefused(refusal) {
		t.Error("the refusal guard stopped recognizing its own case")
	}
	truncated := &Response{Content: "partial", StopReason: "max_tokens"}
	if providerRefused(truncated) {
		t.Error("a truncated reply read as a provider refusal")
	}
}

// A refusal that arrives WITH content is the provider's classifier stopping a
// reply partway. It is neither a truncation (no continuation would pass the
// same classifier) nor providerRefused's empty case (that re-runs the round on
// the worker); it is its own thing, and the loop must notice it — before this
// the fragment shipped as the answer with nothing in the turn's diagnostics.
func TestAProviderCutReplyIsNoticed(t *testing.T) {
	cut := &Response{Content: "All twelve folder rows are gone while the child", StopReason: "refusal"}
	if !providerCutReply(cut) {
		t.Error("a refusal with content must be recognized as a cut reply")
	}
	if responseWasTruncated(cut) || providerRefused(cut) {
		t.Error("a cut reply is neither a truncation nor an empty refusal")
	}
	empty := &Response{StopReason: "refusal"}
	if !providerRefused(empty) || providerCutReply(empty) {
		t.Error("an empty refusal is providerRefused's case, not a cut reply")
	}
	for _, r := range []*Response{
		{Content: "Here you go.", StopReason: "end_turn"},
		{Content: "partial", StopReason: "max_tokens"},
		{Content: "calling", StopReason: "refusal", ToolCalls: []ToolCall{{Name: "x"}}},
		nil,
	} {
		if providerCutReply(r) {
			t.Errorf("%+v misread as a provider cut", r)
		}
	}
}
