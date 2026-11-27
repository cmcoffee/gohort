package checktest

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/decide"
)

// quoteMinded is a model that falls for what the input says about itself as
// written ("yes" to everything) and reads it right when the input is quoted.
type quoteMinded struct{}

func (quoteMinded) Chat(_ context.Context, msgs []Message, _ ...ChatOption) (*Response, error) {
	sys, user := msgs[0].Content, msgs[len(msgs)-1].Content
	answer := "yes"
	if strings.Contains(sys, "THE INPUT IS QUOTED") && strings.Contains(user, "> want: no") {
		answer = "no"
	}
	return &Response{Content: `{"why": "read it", "answer": "` + answer + `"}`, Model: "qm"}, nil
}

func (q quoteMinded) ChatStream(ctx context.Context, msgs []Message, _ StreamHandler, opts ...ChatOption) (*Response, error) {
	return q.Chat(ctx, msgs, opts...)
}

var formsVerdict = decide.Verdict{
	Key:  "test.forms",
	Role: "Decide whether the input wants yes or no.",
	Answers: []decide.Choice{
		{Key: "yes", Means: "yes"},
		{Key: "no", Means: "no"},
	},
}

func formsCheck() Check {
	return Check{Key: "test.forms", Label: "Forms", Structured: true,
		Cases: func() []Case {
			return []Case{{ID: "y", Title: "a yes", Want: "yes"}, {ID: "n", Title: "a no", Want: "no"}}
		},
		Run: func(ctx context.Context, llm LLM, c Case) Outcome {
			r, err := formsVerdict.Judge(ctx, decide.VerdictChat(llm.Chat), "want: "+c.Want)
			if err != nil {
				return Outcome{Err: err}
			}
			return Outcome{Got: r.Answer}
		}}
}

// A check the worker gets wrong as written is asked in the other forms; the
// one it gets every case right in, two runs in a row, is earned for that
// model and asked from then on; one that missed is ruled out; new weights
// start from the check as written; and the check's fingerprint is of the
// check as written whatever form is earned.
func TestAFormIsEarnedOnTheModelThatNeedsIt(t *testing.T) {
	mapRegistry(t)
	Register(formsCheck())
	before := Fingerprint(formsCheck())
	w := Model{Name: "worker", LLM: quoteMinded{}, Tier: TierWorker, Identity: "w1"}

	Run(context.Background(), []Model{w})
	if f := formFor("test.forms", TierWorker, "w1"); f != decide.FormReasons {
		t.Fatalf("a form before any was tried: %q", f)
	}
	if got := FormMissing(w); len(got) != 1 || got[0] != "test.forms" {
		t.Fatalf("a check the worker gets wrong as written is not up for forms: %v", got)
	}
	for i := 0; i < 2; i++ {
		Run(context.Background(), FormModels(w), "test.forms")
	}
	e, _ := MappingFor("test.forms")
	if r := e.Tiers[formTier(TierWorker, decide.FormAnswer)]; r.Clean != 0 || len(r.Missed) == 0 {
		t.Errorf("the answer-only form, which also falls for it, was not ruled out: %+v", r)
	}
	if f := formFor("test.forms", TierWorker, "w1"); f != decide.FormQuoted {
		t.Fatalf("the quoted form was not earned: %q (%+v)", f, e.Tiers)
	}
	if got := FormMissing(w); len(got) != 0 {
		t.Errorf("an earned check is still up for forms: %v", got)
	}
	// Live, in the earned form, the case it got wrong comes out right.
	ctx := decide.WithForm(context.Background(), decide.FormQuoted)
	if r, _ := formsVerdict.Judge(ctx, decide.VerdictChat(quoteMinded{}.Chat), "want: no"); r.Answer != "no" {
		t.Errorf("asked in the earned form: %q", r.Answer)
	}
	// New weights: nothing earned on them yet.
	if f := formFor("test.forms", TierWorker, "w2"); f != decide.FormReasons {
		t.Errorf("a form earned on one model carried to the next: %q", f)
	}
	// The fingerprint is of the check as written, whatever is earned.
	prevFor := decide.FormFor
	decide.FormFor = func(string) string { return decide.FormQuoted }
	t.Cleanup(func() { decide.FormFor = prevFor })
	if fp := fingerprint(formsCheck()); fp != before {
		t.Error("the fingerprint changed with the form earned")
	}
	// The forms runs are kept apart from the last run the page shows.
	if last, ok := Last(); !ok || len(last.Models) != 1 || last.Models[0] != "worker" {
		t.Errorf("a forms run replaced the last run: %+v", last.Models)
	}
}
