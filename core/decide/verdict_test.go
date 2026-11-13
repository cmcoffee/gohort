package decide

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

var testVerdict = Verdict{
	Key:  "test.verdict",
	Role: "You judge whether a message is on topic.",
	Answers: []Choice{
		{Key: "on", Means: "On topic: it is about the subject"},
		{Key: "off", Means: "Off topic: it is about something else"},
	},
	Weigh: []Step{
		{Key: "subject", Ask: "what the message is about"},
		{Key: "why", Ask: "one sentence: why that is or is not the subject"},
	},
	Sure: map[string]float64{"on": 0.7},
}

// replies is a chat that answers with each reply in turn and records what it
// was sent.
func replies(sent *[][]Message, out ...string) VerdictChat {
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		*sent = append(*sent, msgs)
		r := out[0]
		if len(out) > 1 {
			out = out[1:]
		}
		return &Response{Content: r}, nil
	}
}

// The reply's shape puts what is weighed before the answer, in the order
// declared, and names every answer with what it means.
func TestAVerdictAsksForReasonsFirst(t *testing.T) {
	f := testVerdict.Format()
	subject, why, answer := strings.Index(f, `"subject"`), strings.Index(f, `"why"`), strings.Index(f, `"answer"`)
	if subject < 0 || !(subject < why && why < answer) {
		t.Errorf("fields out of order:\n%s", f)
	}
	for _, want := range []string{"- on: On topic", "- off: Off topic", "<one of: on, off>", "answer LAST"} {
		if !strings.Contains(f, want) {
			t.Errorf("format lacks %q:\n%s", want, f)
		}
	}
	if !strings.HasPrefix(testVerdict.System(), testVerdict.Role) {
		t.Error("the system prompt does not open with the role")
	}
	if f2 := (Verdict{Answers: testVerdict.Answers}).Format(); !strings.Contains(f2, `"why"`) {
		t.Errorf("a verdict with no steps writes no reason:\n%s", f2)
	}
}

// A reply is read for its answer and notes; an answer that is not one of the
// verdict's is no answer; a reply that could not be read is asked once more,
// then is an error.
func TestAVerdictReadsItsReply(t *testing.T) {
	var sent [][]Message
	r, err := testVerdict.Judge(context.Background(), replies(&sent,
		"Sure: "+`{"subject":"the weather","why":"the subject is trains","answer":"OFF"}`), "state")
	if err != nil || r.Answer != "off" || r.Notes["subject"] != "the weather" || r.Reason != "the subject is trains" {
		t.Errorf("read %+v, %v", r, err)
	}
	if len(sent) != 1 || sent[0][0].Content != testVerdict.System() || sent[0][1].Content != "state" {
		t.Errorf("sent %+v", sent)
	}

	sent = nil
	r, err = testVerdict.Judge(context.Background(), replies(&sent, "no idea", `{"why":"w","answer":"on"}`), "state")
	if err != nil || r.Answer != "on" || len(sent) != 2 {
		t.Errorf("after one unreadable reply: %+v, %v, %d asks", r, err, len(sent))
	}

	sent = nil
	if _, err = testVerdict.Judge(context.Background(), replies(&sent, "no idea"), "state"); !errors.Is(err, ErrUnreadable) || len(sent) != 2 {
		t.Errorf("two unreadable replies: %v after %d asks", err, len(sent))
	}

	if r, _ = testVerdict.Judge(context.Background(), replies(&sent, `{"why":"w","answer":"maybe"}`), "s"); r.Answer != "" {
		t.Errorf("an answer outside the verdict's read as %q", r.Answer)
	}
}

// Several things in one ask: each ruling lands at its number, a number sent
// as text counts, and one the reply left out has no answer.
func TestAVerdictReadsOneRulingPerThing(t *testing.T) {
	v := testVerdict
	v.Each = "message"
	if f := v.Format(); !strings.Contains(f, `{"messages": [{"message": <its number>, "subject"`) {
		t.Errorf("list shape:\n%s", f)
	}
	var sent [][]Message
	out, err := v.JudgeEach(context.Background(), replies(&sent,
		`{"messages":[{"message":"3","why":"c","answer":"off"},{"message":1,"why":"a","answer":"on"},{"message":9,"answer":"on"}]}`), "s", 3)
	if err != nil || len(out) != 3 {
		t.Fatalf("%v, %d rulings", err, len(out))
	}
	if out[0].Answer != "on" || out[0].Reason != "a" || out[1].Answer != "" || out[2].Answer != "off" {
		t.Errorf("rulings %+v", out)
	}
	sent = nil
	if _, err := v.JudgeEach(context.Background(), replies(&sent, `{"answer":"on"}`), "s", 1); !errors.Is(err, ErrUnreadable) || len(sent) != 2 {
		t.Errorf("a reply with no list: %v after %d asks", err, len(sent))
	}
	if _, err := testVerdict.JudgeEach(context.Background(), replies(&sent, "{}"), "s", 1); err == nil {
		t.Error("a verdict about one thing was asked about each")
	}
}

// An answer stands on System One only at or above its own bar; one with no
// bar never does.
func TestAVerdictSaysWhenSystemOneStands(t *testing.T) {
	if !testVerdict.Stands(0, 0.7) || testVerdict.Stands(0, 0.69) || testVerdict.Stands(1, 0.99) || testVerdict.Stands(5, 1) {
		t.Error("Stands")
	}
	if s := testVerdict.SureList(); len(s) != 2 || s[0] != 0.7 || s[1] != 0 {
		t.Errorf("SureList %v", s)
	}
	q := testVerdict.Question("state", "Is it on topic?")
	if q.Options[1] != "Off topic: it is about something else" || !q.Both || q.System != testVerdict.Role {
		t.Errorf("question %+v", q)
	}
}

// The fingerprint stand-in (core/checktest) answers "{}" on purpose, which a
// check judging each of several things cannot read. It is asked once more
// like any unreadable reply, so the prompts it records are
// the same, and the error reads the same, but it is known for the stand-in's
// and nothing is logged: it fired every check's "could not be read" lines a
// few minutes after each restart. A model's unreadable reply still logs.
func TestTheStandInsEmptyReplyIsNotLogged(t *testing.T) {
	var logged []string
	prev := Debug
	Debug = func(v ...any) {
		if f, ok := v[0].(string); ok {
			logged = append(logged, fmt.Sprintf(f, v[1:]...))
		}
	}
	t.Cleanup(func() { Debug = prev })

	calls := 0
	standIn := func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		calls++
		return &Response{Content: "{}", Model: StandInModel}, nil
	}
	each := testVerdict
	each.Each = "message"
	_, err := each.JudgeEach(context.Background(), standIn, "state", 1)
	if !errors.Is(err, ErrUnreadable) || !FromStandIn(err) || calls != 2 || err.Error() != `the reply could not be read as the verdict asked: the reply was "{}"` {
		t.Errorf("stand-in: %d calls, %v", calls, err)
	}
	if len(logged) != 0 {
		t.Errorf("the stand-in's reply was logged: %q", logged)
	}

	var sent [][]Message
	_, err = each.JudgeEach(context.Background(), replies(&sent, "{}"), "state", 1)
	if !errors.Is(err, ErrUnreadable) || FromStandIn(err) || len(logged) != 2 {
		t.Errorf("a model's unreadable reply: %v, logged %q", err, logged)
	}
}

// A reply that is not the verdict's shape, twice, says what came back: a
// model answering in prose, or with nothing, reads as that, not only as
// "could not be read".
func TestAnUnreadableReplySaysWhatCameBack(t *testing.T) {
	var sent [][]Message
	_, err := testVerdict.Judge(context.Background(), replies(&sent, "I would rather not judge this."), "state")
	if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), `"I would rather not judge this."`) {
		t.Errorf("prose: %v", err)
	}
	_, err = testVerdict.Judge(context.Background(), replies(&sent, "  "), "state")
	if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), "the reply was empty") {
		t.Errorf("empty: %v", err)
	}
}

// An unreadable reply also says how it ended: why the model stopped and what
// it spent, thinking included. An empty reply said nothing else, and whether
// the model ran out of room or stopped on its own after thinking could not be
// told apart without debug logging.
func TestAnUnreadableReplySaysHowItEnded(t *testing.T) {
	empty := func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		return &Response{Content: "", StopReason: "end_turn", OutputTokens: 312, ReasoningTokens: 300}, nil
	}
	_, err := testVerdict.Judge(context.Background(), empty, "state")
	if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), "the reply was empty (stopped: end_turn; 312 output tokens, 300 of them thinking)") {
		t.Errorf("an empty reply: %v", err)
	}
	cut := func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		return &Response{Content: `{"subject": "the weather", "why": "it is about`, StopReason: "max_tokens", OutputTokens: 600}, nil
	}
	_, err = testVerdict.Judge(context.Background(), cut, "state")
	if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), "(stopped: max_tokens; 600 output tokens)") {
		t.Errorf("a cut-off reply: %v", err)
	}
}
