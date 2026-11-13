package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// Verdict is a judgement declared once: who the model is and the test it
// applies (Role), the answers it may give, and what it writes down before it
// answers (Weigh). The reply's shape is the Verdict's, not the caller's: the
// reasons come first and the answer last, so a model works its way to an
// answer rather than writing one and defending it. A small model asked for
// the answer first wrote reasons that named a different answer from the one
// it gave.
//
// The same declaration is the System One form (Question), so a judgement
// written this way can be asked either way.
type Verdict struct {
	// Key is the judgement's route key (its JudgeCall key where it has one),
	// carried by every call.
	Key string
	// Role is the system prompt without the reply's shape: who the model is,
	// what it judges, and how.
	Role string
	// Answers are the answers it may give, in order.
	Answers []Choice
	// Weigh is what the reply says before its answer, in order, one field
	// each. The last is the answer's reason. Empty is one field, "why".
	Weigh []Step
	// Each names the things judged when there are several in one ask
	// ("rule"): the caller numbers them in the state, and the reply has one
	// verdict per thing, each with its own Weigh and answer.
	Each string
	// Sure is the confidence System One needs for an answer to stand alone,
	// by its key. An answer not in it never stands on System One.
	Sure map[string]float64
}

// Choice is one answer a Verdict may give: its key, as the reply writes it,
// and what it means.
type Choice struct {
	Key   string
	Means string
}

// Step is one thing a Verdict's reply says before its answer: the field's
// key and what goes in it.
type Step struct {
	Key string
	Ask string
}

// Ruling is a Verdict's answer: the answer's key ("" when the reply gave
// none of the Verdict's answers), what was written for each Weigh step, and
// the reason (the last step).
type Ruling struct {
	Answer string
	Notes  map[string]string
	Reason string
}

// VerdictChat is how a Verdict reaches its model: an app's worker call live,
// a test's model in a check test.
type VerdictChat func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error)

// defaultWeigh is the reason a Verdict with no Weigh steps writes.
var defaultWeigh = []Step{{Key: "why", Ask: "one sentence: what in front of you decides the answer"}}

func (v Verdict) weigh() []Step {
	if len(v.Weigh) == 0 {
		return defaultWeigh
	}
	return v.Weigh
}

// Keys are the answers' keys, in order.
func (v Verdict) Keys() []string {
	keys := make([]string, len(v.Answers))
	for i, a := range v.Answers {
		keys[i] = a.Key
	}
	return keys
}

// Index is answer key's place in Answers, or -1.
func (v Verdict) Index(key string) int {
	for i, a := range v.Answers {
		if a.Key == key {
			return i
		}
	}
	return -1
}

// SureList is Sure in Answers' order, for a Decision.
func (v Verdict) SureList() []float64 {
	out := make([]float64, len(v.Answers))
	for i, a := range v.Answers {
		out[i] = v.Sure[a.Key]
	}
	return out
}

// Stands reports whether System One's answer choice, chosen as firmly as
// firm (Answer.Firm), would stand alone live: false is an answer the full
// question would be asked for.
func (v Verdict) Stands(choice int, firm float64) bool {
	if choice < 0 || choice >= len(v.Answers) {
		return false
	}
	bar, ok := v.Sure[v.Answers[choice].Key]
	return ok && bar > 0 && firm >= bar
}

// Format is the reply's shape as the model is told it.
func (v Verdict) Format() string {
	var fields strings.Builder
	for _, s := range v.weigh() {
		fmt.Fprintf(&fields, "%q: %q, ", s.Key, "<"+s.Ask+">")
	}
	fmt.Fprintf(&fields, "%q: %q", "answer", "<one of: "+strings.Join(v.Keys(), ", ")+">")
	var b strings.Builder
	b.WriteString("ANSWERS:\n")
	for _, a := range v.Answers {
		fmt.Fprintf(&b, "- %s: %s\n", a.Key, a.Means)
	}
	b.WriteString("\nReply with ONLY a JSON object, no other text. Write the fields in the order shown: what you weighed FIRST, the answer LAST, so the answer follows from what you wrote.\n")
	if v.Each == "" {
		fmt.Fprintf(&b, "{%s}", fields.String())
		return b.String()
	}
	fmt.Fprintf(&b, "One entry for EVERY %s, in order, naming it by its number:\n{%q: [{%q: <its number>, %s}]}", v.Each, v.Each+"s", v.Each, fields.String())
	return b.String()
}

// System is the system prompt: the Role and the reply's shape.
func (v Verdict) System() string {
	return strings.TrimSpace(v.Role) + "\n\n" + v.Format()
}

// Judge asks the Verdict about state, once more when the reply could not be
// read. A reply with none of the Verdict's answers is a Ruling with no
// Answer, not an error: the caller decides what an unanswered judgement
// means.
func (v Verdict) Judge(ctx context.Context, chat VerdictChat, state string, opts ...ChatOption) (Ruling, error) {
	var out Ruling
	err := v.ask(ctx, chat, state, opts, func(obj map[string]json.RawMessage) bool {
		out = v.ruling(obj)
		return true
	})
	return out, err
}

// JudgeEach asks the Verdict about n things numbered in state, once more when
// the reply could not be read. The rulings are in order, one per thing; one
// the reply left out has no Answer.
func (v Verdict) JudgeEach(ctx context.Context, chat VerdictChat, state string, n int, opts ...ChatOption) ([]Ruling, error) {
	if v.Each == "" {
		return nil, fmt.Errorf("verdict %s judges one thing, not each", v.Key)
	}
	var out []Ruling
	err := v.ask(ctx, chat, state, opts, func(obj map[string]json.RawMessage) bool {
		var ok bool
		out, ok = v.rulings(obj, n)
		return ok
	})
	return out, err
}

// ErrUnreadable is a reply that was not the Verdict's shape, twice. The
// error returned wraps it with what the last reply was: test with errors.Is.
const ErrUnreadable = Error("the reply could not be read as the verdict asked")

// unreadable is ErrUnreadable with what the last reply was, and whether the
// fingerprint stand-in (StandInModel) wrote it rather than a model.
type unreadable struct {
	clip    string
	standIn bool
}

func (e unreadable) Error() string        { return ErrUnreadable.Error() + ": the reply was " + e.clip }
func (e unreadable) Is(target error) bool { return target == ErrUnreadable }

// FromStandIn reports whether err is a reply the fingerprint stand-in wrote
// (core/checktest), which no model failed to give: a caller that logs a
// check's error leaves this one out.
func FromStandIn(err error) bool {
	var u unreadable
	return errors.As(err, &u) && u.standIn
}

// ask sends the Verdict and hands the reply's object to read; a reply with
// no object, or one read rejects, is asked again once.
func (v Verdict) ask(ctx context.Context, chat VerdictChat, state string, opts []ChatOption, read func(map[string]json.RawMessage) bool) error {
	msgs := []Message{{Role: "system", Content: v.System()}, {Role: "user", Content: state}}
	call := append([]ChatOption{WithRouteKey(v.Key)}, opts...)
	last, standIn := "", false
	var lastResp *Response
	for try := 1; try <= 2; try++ {
		resp, err := chat(ctx, msgs, call...)
		if err != nil {
			return err
		}
		if resp != nil {
			last, standIn, lastResp = resp.Content, resp.Model == StandInModel, resp
			if obj := readObject(resp.Content); obj != nil && read(obj) {
				return nil
			}
		}
		// The fingerprint stand-in answers nothing on purpose: no model
		// failed, so nothing is logged for it.
		if try == 1 && !standIn {
			Debug("[decide] %s: the reply could not be read, asking once more", v.Key)
		}
	}
	// What came back, so a model that keeps answering in another shape can
	// be seen doing it (a check test's row, the log) instead of only "could
	// not be read".
	clip := textutil.ReplyClip(last, unreadableClip) + replyEnding(lastResp)
	if !standIn {
		Debug("[decide] %s: the reply could not be read twice; the last was %s", v.Key, clip)
	}
	return unreadable{clip: clip, standIn: standIn}
}

// replyEnding is how a reply ended, for an unreadable one: why the model
// stopped and what it spent, thinking included. An empty reply from a model
// that says it stops thinking (Sonnet's between_tools) said nothing else, with
// debug off: whether it ran out of room, stopped on its own after thinking,
// or wrote its answer somewhere else could not be told apart.
func replyEnding(resp *Response) string {
	if resp == nil || resp.Model == StandInModel || (resp.StopReason == "" && resp.OutputTokens == 0) {
		return ""
	}
	var parts []string
	if resp.StopReason != "" {
		parts = append(parts, "stopped: "+resp.StopReason)
	}
	if resp.OutputTokens > 0 {
		out := fmt.Sprintf("%d output tokens", resp.OutputTokens)
		if resp.ReasoningTokens > 0 {
			out += fmt.Sprintf(", %d of them thinking", resp.ReasoningTokens)
		}
		parts = append(parts, out)
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// unreadableClip is how much of an unreadable reply its error quotes.
const unreadableClip = 160

// readObject is the first JSON object in a reply, by field.
func readObject(content string) map[string]json.RawMessage {
	raw := textutil.FirstJSONObject(content)
	if raw == "" {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return nil
	}
	return obj
}

// ruling reads one verdict's fields.
func (v Verdict) ruling(obj map[string]json.RawMessage) Ruling {
	r := Ruling{Notes: map[string]string{}}
	steps := v.weigh()
	for _, s := range steps {
		r.Notes[s.Key] = textField(obj[s.Key])
	}
	r.Reason = r.Notes[steps[len(steps)-1].Key]
	got := strings.ToLower(strings.TrimSpace(textField(obj["answer"])))
	for _, k := range v.Keys() {
		if got == strings.ToLower(k) {
			r.Answer = k
		}
	}
	return r
}

// rulings reads one verdict per thing judged; ok false is a reply with no
// list of them at all.
func (v Verdict) rulings(obj map[string]json.RawMessage, n int) ([]Ruling, bool) {
	var list []map[string]json.RawMessage
	if json.Unmarshal(obj[v.Each+"s"], &list) != nil || len(list) == 0 {
		return nil, false
	}
	out := make([]Ruling, n)
	for _, item := range list {
		num := numberField(item[v.Each])
		if num < 1 || num > n || out[num-1].Notes != nil {
			continue
		}
		out[num-1] = v.ruling(item)
	}
	return out, true
}

// textField is a field as text: a string as written, anything else as its
// JSON.
func textField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(raw))
}

// numberField is a thing's number, written as a number or as text ("2");
// 0 when it is neither.
func numberField(raw json.RawMessage) int {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	}
	return n
}

// Question is the Verdict as a System One decision about state: ask, with
// the answers' meanings as the options.
func (v Verdict) Question(state, ask string) Question {
	opts := make([]string, len(v.Answers))
	for i, a := range v.Answers {
		opts[i] = a.Means
	}
	return Question{System: strings.TrimSpace(v.Role), State: state, Ask: ask, Options: opts, Both: true}
}
