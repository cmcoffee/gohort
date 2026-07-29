package core

// The live ribbon is global and untenanted by design — every user sees every
// other user's running work. Its Label, though, is user content: the chat
// message, the research question, the debate topic. MaskedLabel is what keeps
// the ribbon's reach without the ribbon's disclosure.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskedLabel_OwnerSeesTheRealThing(t *testing.T) {
	e := LiveEntry{Label: "how do I tell my boss I'm leaving", App: "Gohort", Owner: "craig"}
	if got := e.MaskedLabel("craig"); got != e.Label {
		t.Errorf("owner must see the real label, got %q", got)
	}
}

func TestMaskedLabel_EveryoneElseGetsGeneric(t *testing.T) {
	e := LiveEntry{Label: "how do I tell my boss I'm leaving", App: "Gohort", Owner: "craig"}
	for _, viewer := range []string{"dana", "", "admin"} {
		got := e.MaskedLabel(viewer)
		if got == e.Label {
			t.Errorf("viewer %q must not see the label", viewer)
		}
		if got != "Gohort · craig" {
			t.Errorf("viewer %q: got %q", viewer, got)
		}
	}
}

func TestMaskedLabel_UnknownOwnerFailsClosed(t *testing.T) {
	// A provider that hasn't been taught to set Owner must not leak by
	// default — including to a viewer who happens to be the real owner,
	// since nothing here can tell.
	e := LiveEntry{Label: "quarterly layoff modeling", App: "Deep Research"}
	for _, viewer := range []string{"craig", ""} {
		if got := e.MaskedLabel(viewer); got != "Deep Research · another user" {
			t.Errorf("viewer %q: got %q", viewer, got)
		}
	}
}

func TestMaskedLabel_NoOwnerMatchOnEmptyViewer(t *testing.T) {
	// Both empty must NOT count as a match — an unauthenticated viewer is
	// not the owner of an untagged session.
	e := LiveEntry{Label: "secret", App: "X"}
	if got := e.MaskedLabel(""); got == "secret" {
		t.Error("empty owner and empty viewer must not read as ownership")
	}
}

func TestMaskedLabel_PreservesTreeIndent(t *testing.T) {
	// The nested run view renders depth from the label's own prefix; masking
	// that away would flatten the tree.
	cases := []struct{ label, want string }{
		{"↳ sub-question about severance", "↳ Gohort · craig"},
		{"  ↳ deeper", "  ↳ Gohort · craig"},
		{"    ↳ deeper still", "    ↳ Gohort · craig"},
		{"top level", "Gohort · craig"},
	}
	for _, c := range cases {
		e := LiveEntry{Label: c.label, App: "Gohort", Owner: "craig"}
		if got := e.MaskedLabel("dana"); got != c.want {
			t.Errorf("label %q → %q, want %q", c.label, got, c.want)
		}
	}
}

func TestMaskedLabel_NoAppStillMasks(t *testing.T) {
	e := LiveEntry{Label: "sensitive", Owner: "craig"}
	if got := e.MaskedLabel("dana"); got != "Active session · craig" {
		t.Errorf("got %q", got)
	}
}

func TestSplitLiveIndent(t *testing.T) {
	cases := []struct{ in, indent, rest string }{
		{"plain", "", "plain"},
		{"↳ child", "↳ ", "child"},
		{"  ↳ grandchild", "  ↳ ", "grandchild"},
		{"   leading spaces only", "   ", "leading spaces only"},
		{"", "", ""},
	}
	for _, c := range cases {
		gi, gr := splitLiveIndent(c.in)
		if gi != c.indent || gr != c.rest {
			t.Errorf("splitLiveIndent(%q) = (%q, %q), want (%q, %q)", c.in, gi, gr, c.indent, c.rest)
		}
	}
}

func TestLiveEntry_OwnerNeverSerialized(t *testing.T) {
	// Owner is a server-side masking input, not something the browser needs.
	// If it ever ships in the JSON it becomes a second disclosure channel.
	if !jsonOmitsField(LiveEntry{Owner: "craig", Label: "x"}, "craig") {
		t.Error("Owner must not appear in the serialized entry")
	}
}

func TestSetOwner_NilSafe(t *testing.T) {
	// Register returns nil at the concurrency cap; chaining must not panic.
	var s *LiveSession[string]
	if got := s.SetOwner("craig"); got != nil {
		t.Error("SetOwner on nil must return nil")
	}
}

// jsonOmitsField reports whether v's JSON encoding excludes the given value.
func jsonOmitsField(v any, value string) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return !strings.Contains(string(b), value)
}
