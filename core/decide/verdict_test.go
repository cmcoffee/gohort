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
	stopped := func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		return &Response{Content: "", StopReason: "refusal", StopDetail: "Bedrock said guardrail_intervened", OutputTokens: 44}, nil
	}
	_, err = testVerdict.Judge(context.Background(), stopped, "state")
	if !errors.Is(err, ErrUnreadable) || !strings.HasSuffix(err.Error(), "(stopped: refusal, Bedrock said guardrail_intervened; 44 output tokens)") {
		t.Errorf("a reply its provider stopped: %v", err)
	}
}

// refusingThen is a chat whose first reply is a refusal of category cat from
// model, then answers with out in turn, from answerModel; it records what it
// was sent.
func refusingThen(sent *[][]Message, cat, model, answerModel string, out ...string) VerdictChat {
	refused := false
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		*sent = append(*sent, msgs)
		if !refused {
			refused = true
			return &Response{Model: model, StopReason: "refusal", StopCategory: cat}, nil
		}
		r := out[0]
		if len(out) > 1 {
			out = out[1:]
		}
		return &Response{Model: answerModel, Content: r}, nil
	}
}

// A model that refuses to write the reasons a verdict asks for (Sonnet's
// reasoning_extraction) is asked again at once for the answer alone, and
// asked that way from then on; the rulings say why they carry no reason.
func TestAModelThatWillNotWriteItsReasoningGivesTheAnswerAlone(t *testing.T) {
	t.Cleanup(func() { answersOnly.Delete(testVerdict.Key) })
	var sent [][]Message
	chat := refusingThen(&sent, declinesReasoning, "sonnet", "sonnet", `{"answer": "on"}`)
	r, err := testVerdict.Judge(context.Background(), chat, "a message")
	if err != nil || r.Answer != "on" || r.Reason != "(answer only: sonnet declines to write its reasoning)" {
		t.Fatalf("after the refusal: %+v, %v", r, err)
	}
	if len(sent) != 2 {
		t.Fatalf("asked %d times, want the refused ask and one for the answer alone", len(sent))
	}
	full, bare := sent[0][0].Content, sent[1][0].Content
	if full != testVerdict.System() || !strings.Contains(full, `"why"`) {
		t.Errorf("the first ask was not the verdict as declared:\n%s", full)
	}
	if strings.Contains(bare, `"why"`) || strings.Contains(bare, `"subject"`) || strings.Contains(bare, "answer LAST") ||
		!strings.Contains(bare, "the answer alone") || !strings.Contains(bare, "<one of: on, off>") || !strings.HasPrefix(bare, testVerdict.Role) {
		t.Errorf("the second ask still asks for reasons, or lost the answers:\n%s", bare)
	}

	// From then on the answer alone is asked first: no refusal paid for.
	sent = nil
	r, err = testVerdict.Judge(context.Background(), replies(&sent, `{"answer": "off"}`), "another")
	if err != nil || r.Answer != "off" || len(sent) != 1 || strings.Contains(sent[0][0].Content, `"why"`) {
		t.Errorf("the next ask: %+v, %v after %d asks", r, err, len(sent))
	}

	// The refusal is not one of the two tries: an answer-alone reply that
	// could not be read is still asked once more.
	answersOnly.Delete(testVerdict.Key)
	sent = nil
	r, err = testVerdict.Judge(context.Background(), refusingThen(&sent, declinesReasoning, "sonnet", "sonnet", `not json`, `{"answer": "on"}`), "a third")
	if err != nil || r.Answer != "on" || len(sent) != 3 {
		t.Errorf("refused, then unreadable, then answered: %+v, %v after %d asks, want an answer after 3", r, err, len(sent))
	}

	// Several things at once: one answer per thing, no reasons.
	each := testVerdict
	each.Key, each.Each = "test.verdict.each", "item"
	t.Cleanup(func() { answersOnly.Delete(each.Key) })
	sent = nil
	rs, err := each.JudgeEach(context.Background(), refusingThen(&sent, declinesReasoning, "sonnet", "sonnet",
		`{"items": [{"item": 1, "answer": "on"}, {"item": 2, "answer": "off"}]}`), "1. a\n2. b", 2)
	if err != nil || len(rs) != 2 || rs[0].Answer != "on" || rs[1].Answer != "off" || !strings.Contains(rs[1].Reason, "answer only") {
		t.Fatalf("each, answer alone: %+v, %v", rs, err)
	}
	if bare := sent[1][0].Content; strings.Contains(bare, `"why"`) || !strings.Contains(bare, `"items"`) {
		t.Errorf("each, the second ask:\n%s", bare)
	}
}

// Asked for the answer alone and answered by another model, the check has
// moved off the model that declined: the next ask is the verdict as declared.
func TestAnotherModelForgetsTheAnswerAloneAsk(t *testing.T) {
	t.Cleanup(func() { answersOnly.Delete(testVerdict.Key) })
	var sent [][]Message
	if _, err := testVerdict.Judge(context.Background(), refusingThen(&sent, declinesReasoning, "sonnet", "local", `{"answer": "on"}`), "x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := answersOnly.Load(testVerdict.Key); ok {
		t.Error("an answer from another model kept the answer-alone ask")
	}
	sent = nil
	r, err := testVerdict.Judge(context.Background(), replies(&sent, `{"subject": "s", "why": "w", "answer": "on"}`), "y")
	if err != nil || r.Reason != "w" || !strings.Contains(sent[0][0].Content, `"why"`) {
		t.Errorf("the next ask was not the full verdict: %+v, %v", r, err)
	}
	// The same model answering keeps it.
	sent = nil
	testVerdict.Judge(context.Background(), refusingThen(&sent, declinesReasoning, "sonnet", "sonnet", `{"answer": "on"}`), "z")
	if m, ok := answersOnly.Load(testVerdict.Key); !ok || m != "sonnet" {
		t.Errorf("the declining model was not remembered: %v %v", m, ok)
	}
}

// Only a refusal of the reasoning itself switches the ask: any other refusal
// is a reply that could not be read, asked once more as declared.
func TestOtherRefusalsKeepTheReasons(t *testing.T) {
	t.Cleanup(func() { answersOnly.Delete(testVerdict.Key) })
	var sent [][]Message
	_, err := testVerdict.Judge(context.Background(), refusingThen(&sent, "cyber", "sonnet", "sonnet", `{"subject": "s", "why": "w", "answer": "on"}`), "x")
	if err != nil || len(sent) != 2 || !strings.Contains(sent[1][0].Content, `"why"`) {
		t.Errorf("a cyber refusal: %v after %d asks; the retry must keep the reasons", err, len(sent))
	}
	if _, ok := answersOnly.Load(testVerdict.Key); ok {
		t.Error("a cyber refusal was remembered as declining its reasoning")
	}
	// A refused ask for the answer alone is not switched again: it is a
	// reply that could not be read, and the second try is the last.
	answersOnly.Store(testVerdict.Key, "sonnet")
	sent = nil
	_, err = testVerdict.Judge(context.Background(), func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		sent = append(sent, msgs)
		return &Response{Model: "sonnet", StopReason: "refusal", StopCategory: declinesReasoning}, nil
	}, "x")
	if !errors.Is(err, ErrUnreadable) || len(sent) != 2 {
		t.Errorf("refusing the answer alone too: %v after %d asks, want unreadable after 2", err, len(sent))
	}
}
