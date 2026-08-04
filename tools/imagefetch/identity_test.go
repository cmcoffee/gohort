package imagefetch

// A search for a named person. The vision screen answers "is this the right
// SORT of picture", which is not the question, and returns a confident number
// for the half it can judge. Provenance — the source page actually naming the
// subject — is the only evidence available, and it is a text check, so it holds
// on a deployment whose model cannot see at all.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)


// stubLLM stands in for a model that can see; showToModel only checks that one
// is bound, never calls it.
type stubLLM struct{}

func (stubLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return &Response{}, nil
}
func (stubLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return &Response{}, nil
}

func TestAQueryNamingSomeoneIsRecognized(t *testing.T) {
	for _, q := range []string{
		"Shazz Barbaric real estate",
		"Shazz Barbaric man portrait real estate ranch owner Texas",
		"Becky Barrick agent",
	} {
		if len(namedSubjectTokens(q)) == 0 {
			t.Errorf("%q names a subject that pixels cannot confirm", q)
		}
	}
	// A description of a THING names nobody — these keep working exactly as
	// they do today, vision score and all.
	for _, q := range []string{
		"dragon mythical creature",
		"exhausted office worker at desk",
		"dog wearing sunglasses",
	} {
		if got := namedSubjectTokens(q); len(got) != 0 {
			t.Errorf("%q is a description, not a name; got %v", q, got)
		}
	}
}

func TestTheNameIsRequiredNotCounted(t *testing.T) {
	// The exact miss from the log. Eight tokens, five of them generic, the page
	// carries every generic one and never says "Shazz" — a comfortable 60%
	// majority, and text=true on a page about somebody else entirely.
	const query = "Shazz Barbaric man portrait real estate ranch owner Texas"
	page := strings.ToLower("Ranch Brokers and Real Estate Agents | Farm & Ranch — meet our team of Texas ranch owners, portrait gallery")
	if pageMentionsSubject(page, query) {
		t.Error("a page that never names the subject must not text-match, however many generic words it shares")
	}
	// The same page, for the same person, once it actually mentions him.
	if !pageMentionsSubject(page+" shazz barbaric joined us in 2019", query) {
		t.Error("a page that DOES name him must match")
	}
}

func TestANamedSubjectCannotBeCarriedByAVisionScoreAlone(t *testing.T) {
	// Every candidate scored 85-95 and not one page mentioned him. The old exit
	// took the best score; the refusal has to say what it actually knows.
	err := identityUnverifiableError("Shazz Barbaric real estate", 5)
	msg := err.Error()
	for _, want := range []string{"mentions them by name", "Do NOT retry", "ask for one"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must carry %q:\n%v", want, err)
		}
	}
	// It must not read as a near-miss, or the model just searches again — six
	// times in ninety seconds, a different stranger each time. The other
	// rejection path ends with "refine the query"; this one must not, because
	// here rewording cannot help.
	if strings.Contains(strings.ToLower(msg), "refine the query") {
		t.Errorf("the refusal must not invite a retry:\n%v", err)
	}
}

func TestADescriptiveQueryKeepsTheMajorityRule(t *testing.T) {
	// The tightening is scoped to names. A descriptive query still tolerates a
	// page that doesn't happen to use every adjective.
	const query = "exhausted office worker at desk"
	if !pageMentionsSubject("a tired worker slumped at an office desk", query) {
		t.Error("a descriptive query must still match on a majority")
	}
}

func TestShowingTheModelDegradesWhenItCannotLook(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")

	// No LLM bound: nowhere to send the bytes, and no sentence claiming a
	// picture the model will never receive.
	if got := showToModel(&ToolSession{}, png, "caveat"); got != "" {
		t.Errorf("with no LLM there is nothing to show: %q", got)
	}
	// Detached: the turn that asked ended, so there is no next round to inject
	// into. Telling a model to look at something that will not arrive is how it
	// ends up describing an image it never saw.
	if got := showToModel(&ToolSession{Detached: true}, png, "caveat"); got != "" {
		t.Errorf("a detached call has no round to show into: %q", got)
	}
	if got := showToModel(nil, png, "caveat"); got != "" {
		t.Errorf("no session, nothing to show: %q", got)
	}
}

func TestLookingIsNeverAskedToConfirmWhoSomeoneIs(t *testing.T) {
	// A search for a named person returned his photo from his own site: name in
	// the page title, first candidate, 95/100. The agent was shown it, was told
	// to "check it really shows what was asked for", did not recognize the
	// face, and sent nothing. The right picture, refused — because identity is
	// not something looking can settle, for the agent any more than for the
	// screen that scored it.
	sess := &ToolSession{LLM: stubLLM{}}
	msg := showToModel(sess, []byte("\x89PNG\r\n\x1a\nfake"), "It is a search result")
	if msg == "" {
		t.Fatal("a session that can see must be shown the picture")
	}
	if !strings.Contains(msg, "does NOT settle WHO someone is") {
		t.Errorf("the instruction must put identity out of scope:\n%s", msg)
	}
	if !strings.Contains(msg, "not evidence of a wrong picture") {
		t.Errorf("an unfamiliar face must be named as a non-signal:\n%s", msg)
	}
	// Delivering has to be the DEFAULT, or every uncertain look ends in silence.
	if !strings.Contains(msg, "Deliver it unless") {
		t.Errorf("the default must be to deliver:\n%s", msg)
	}
	// And the bytes actually went where the model will see them.
	if got := sess.DrainViewImages(); len(got) != 1 {
		t.Errorf("queued %d image(s) for the model, want 1", len(got))
	}
}
