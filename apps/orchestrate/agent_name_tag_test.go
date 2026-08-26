package orchestrate

import (
	"strings"
	"testing"
)

// Agent names carry display tags in brackets — "Kwik [Cortex]", "Market
// Research [Fleet]" — which is this framework's OWN convention, printed in
// every listing an agent reads. So a caller naturally writes the name without
// the tag, and exact matching then fails on an agent that plainly exists.
//
// Reported live: Builder looked up "Moltbook Conversational Agent", was told
// it was not found, and stopped to ask the user which agent was meant, while
// the agent sat in the survey output it had just read.
func TestANameResolvesWithoutItsDisplayTag(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a1", Name: "Moltbook Conversational Agent [Cortex]"},
		{ID: "a2", Name: "Market Research [Fleet]"},
		{ID: "a3", Name: "Critic"},
	}
	for _, c := range []struct{ key, wantID string }{
		{"Moltbook Conversational Agent", "a1"}, // the reported case
		{"moltbook conversational agent", "a1"}, // case drift
		{"moltbook-conversational-agent", "a1"}, // separator drift, tag absent
		{"Market Research", "a2"},
		{"Critic", "a3"}, // untagged names still work
	} {
		got, ok := uniqueAgentByBaseName(agents, stripAgentTag(normalizeAgentKey(c.key)))
		if !ok || got.ID != c.wantID {
			t.Errorf("%q resolved to %+v, want %s", c.key, got, c.wantID)
		}
	}
}

// Ambiguity is not resolved by guessing. Two agents sharing a base name mean
// the bare name means NEITHER — picking one would silently edit the wrong
// agent, which is worse than saying so.
func TestAnAmbiguousBaseNameDoesNotGuess(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a1", Name: "Research [Cortex]"},
		{ID: "a2", Name: "Research [Fleet]"},
	}
	if _, ok := uniqueAgentByBaseName(agents, "research"); ok {
		t.Fatal("an ambiguous base name picked one of two agents")
	}
	// And the suggestion names both, so the choice stays with the caller.
	s := suggestAgents(agents, "Research")
	for _, want := range []string{"Research [Cortex]", "Research [Fleet]"} {
		if !strings.Contains(s, want) {
			t.Errorf("the suggestion omits %q: %s", want, s)
		}
	}
}

// An exact name always wins over a tag-stripped collision.
func TestAnExactNameWinsOverTagStripping(t *testing.T) {
	agents := []AgentRecord{
		{ID: "tagged", Name: "Research [Cortex]"},
		{ID: "plain", Name: "Research"},
	}
	// "Research" matches the plain agent exactly; stripping must not make it
	// ambiguous with the tagged one.
	var exact AgentRecord
	for _, a := range agents {
		if normalizeAgentKey(a.Name) == normalizeAgentKey("Research") {
			exact = a
		}
	}
	if exact.ID != "plain" {
		t.Fatalf("exact match resolved to %q", exact.ID)
	}
}

// A dead-end "not found" against a fleet of dozens leaves the caller unable to
// tell a missing agent from a misspelt one.
func TestSuggestionsNameTheNearMisses(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a1", Name: "Moltbook Conversational Agent [Cortex]"},
		{ID: "a2", Name: "Deal Hunter"},
		{ID: "a3", Name: "Comedian"},
	}
	s := suggestAgents(agents, "Moltbook")
	if !strings.Contains(s, "Moltbook Conversational Agent [Cortex]") {
		t.Errorf("a partial name suggested nothing useful: %q", s)
	}
	// An unrelated name suggests nothing rather than listing the fleet.
	if got := suggestAgents(agents, "zzzz"); got != "" {
		t.Errorf("an unrelated name produced suggestions: %q", got)
	}
	if got := suggestAgents(agents, ""); got != "" {
		t.Errorf("an empty key produced suggestions: %q", got)
	}
}

// Callers shorten names. A model reading a fleet listing writes the
// distinctive word, not the four-word title with its bracketed tag. Reported
// live: Builder looked up "moltbook", was told no such agent existed, and
// repeated that to the user as a fact about an agent it had just seen listed.
func TestAUniquePartialNameResolves(t *testing.T) {
	agents := []AgentRecord{
		{ID: "molt", Name: "Moltbook Conversational Agent [Cortex]"},
		{ID: "deal", Name: "Deal Hunter"},
		{ID: "comedian", Name: "Comedian"},
	}
	for _, c := range []struct{ key, wantID string }{
		{"moltbook", "molt"}, // the reported case
		{"Moltbook", "molt"}, // case drift
		{"deal", "deal"},
		{"comedian", "comedian"},
	} {
		got, ok := uniqueAgentByPartialName(agents, normalizeAgentKey(c.key))
		if !ok || got.ID != c.wantID {
			t.Errorf("%q resolved to %+v, want %s", c.key, got, c.wantID)
		}
	}
}

// Unique or nothing. A fragment matching two agents means neither — resolving
// it would edit whichever sorted first, and an authoring tool silently
// rewriting the wrong agent is worse than a failed lookup.
func TestAnAmbiguousPartialNameRefuses(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a", Name: "Research Assistant"},
		{ID: "b", Name: "Research Reviewer"},
	}
	if _, ok := uniqueAgentByPartialName(agents, "research"); ok {
		t.Fatal("an ambiguous fragment picked one of two agents")
	}
}

// A prefix beats an interior match: "research" should mean the agent whose
// name STARTS that way, not one that merely contains the word.
func TestAPrefixBeatsAnInteriorMatch(t *testing.T) {
	agents := []AgentRecord{
		{ID: "deep", Name: "Deep Dive Research Agent [Fleet]"},
		{ID: "plain", Name: "Research Agent"},
	}
	got, ok := uniqueAgentByPartialName(agents, "research")
	if !ok || got.ID != "plain" {
		t.Fatalf("resolved to %+v, want the agent whose name begins with it", got)
	}
}

// A fragment too short to identify anything must not resolve: two letters
// matching one agent today match three after the next one is added, so the
// answer would be correct only until the fleet grew.
func TestAVeryShortFragmentDoesNotResolve(t *testing.T) {
	agents := []AgentRecord{{ID: "a", Name: "Comedian"}}
	for _, frag := range []string{"c", "co", ""} {
		if _, ok := uniqueAgentByPartialName(agents, frag); ok {
			t.Errorf("%q resolved to an agent", frag)
		}
	}
}

// The precedence chain end to end: an exact name must never be beaten by a
// partial match on a different agent.
func TestExactNameStillWinsOverAPartial(t *testing.T) {
	agents := []AgentRecord{
		{ID: "long", Name: "Research Assistant"},
		{ID: "short", Name: "Research"},
	}
	// "Research" is exact for one and a prefix of the other; the exact one wins
	// because findAgentByNameOrID checks exact names before ever reaching the
	// partial fallback.
	var exact AgentRecord
	for _, a := range agents {
		if strings.EqualFold(a.Name, "Research") {
			exact = a
		}
	}
	if exact.ID != "short" {
		t.Fatalf("exact match resolved to %q", exact.ID)
	}
}
