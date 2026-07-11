// Router category-gate — the PRE-answer complement to the grounding gate.
//
// The grounding gate (grounding_gate.go) fires AFTER the model answers, catching
// an unsourced money figure and re-prompting. This gate fires BEFORE the model
// commits, on the shape of the QUESTION: a request for a current real-world value
// (a price, a version, a current office-holder, availability, standings, live
// status) is the exact setup for a confident-but-stale answer from the model's
// weights — the "the 5090 costs $1,600" failure, where the model is MOST certain
// of exactly the prior it should trust least. Confidence is therefore the wrong
// signal; the question's SUBJECT is the right one. When the agent has a lookup
// tool, the gate injects a directive to search first.
//
// Deterministic and category-based (no extra LLM round-trip). Opt-in via
// tune_lookup_gate. The injection is done by the caller onto the user turn (the
// cache-safe spot), so this file only supplies detection + directive text.

package core

import (
	"regexp"
	"strings"
)

const tuneLookupGate = "tune_lookup_gate"

func init() {
	RegisterTunable(TunableSpec{
		Key:      tuneLookupGate,
		Category: "Limits",
		Label:    "Lookup gate: force search on volatile questions",
		Help:     "When on, a user question asking for a current real-world value (a price, a version, a current office-holder, availability, standings, or live status) injects a directive telling the agent to look it up before answering — pre-empting a confident-but-stale answer from the model's training. Fires only when the agent has a search/fetch tool. Independent of the post-answer grounding gate.",
		Kind:     KindBool, Default: 0, Min: 0, Max: 1,
	})
}

// LookupGateEnabled reports whether the router category-gate is active.
func LookupGateEnabled() bool { return TuneBool(tuneLookupGate) }

// lookupCategories maps a human-readable category to a word-boundary regex of the
// question phrasings that signal it. Word boundaries avoid the substring traps
// ("cost" must not fire on "costume", "price" not on "priceless"). Kept focused on
// high-signal cues: a false "needs lookup" only injects a directive the model can
// ignore, so recall is favored over precision, but not recklessly.
var lookupCategories = []struct {
	name string
	re   *regexp.Regexp
}{
	{"price", regexp.MustCompile(`(?i)\b(price|prices|pricing|cost|costs|msrp|how much|how expensive)\b`)},
	{"software version", regexp.MustCompile(`(?i)\b(latest version|current version|what version|newest version|latest release|which version|most recent version)\b`)},
	{"availability", regexp.MustCompile(`(?i)\b(in stock|out of stock|sold out|still available|can i buy|on sale|back in stock)\b`)},
	{"standings or score", regexp.MustCompile(`(?i)\b(who won|final score|the score|standings|leaderboard|currently ranked|who is winning|who's winning|top of the table)\b`)},
	{"current office-holder", regexp.MustCompile(`(?i)\b(who is the ceo|current ceo|who is the president|who's the president|prime minister|who runs|who is the current|who leads)\b`)},
	{"current status or news", regexp.MustCompile(`(?i)\b(latest news|what's the latest|current status|most recent|as of today|what's happening with)\b`)},
}

// LookupCategory reports whether a user message asks for a current real-world
// value worth looking up, returning the matched category label. First category
// wins. Deterministic — no LLM call.
func LookupCategory(userMsg string) (string, bool) {
	for _, c := range lookupCategories {
		if c.re.MatchString(userMsg) {
			return c.name, true
		}
	}
	return "", false
}

// HasLookupTool reports whether any tool is a web search / fetch / browse tool —
// the gate only fires when the agent can actually look something up (injecting
// "search first" with no search tool would be worse than nothing).
func HasLookupTool(tools []AgentToolDef) bool {
	for _, t := range tools {
		n := strings.ToLower(t.Tool.Name)
		if strings.Contains(n, "search") || strings.Contains(n, "fetch") || strings.Contains(n, "browse") {
			return true
		}
	}
	return false
}

// LatestUserContent returns the content of the most recent user message, or ""
// when there is none.
func LatestUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// LookupDirective is the turn directive injected when the gate fires. It names
// the category so the model knows precisely what to verify, and it forbids
// answering the specific from memory.
func LookupDirective(category string) string {
	return "[Lookup required: the user is asking about a current " + category +
		". This is exactly the kind of value that changes over time and that you tend to state wrong from memory. Before giving any specific figure, name, or version, use a search or fetch tool to get the CURRENT value and quote what it returns. If no such tool is available or the lookup fails, say plainly you'd need to look it up rather than answering from memory. Do NOT state a specific from your training.]"
}
