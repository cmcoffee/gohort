package decide

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Decision is a question with fixed answers, settled the cheapest way that
// is sure: by System One when it is on and confident enough in its answer,
// by a full answer with a reason when it is not, and by a safe answer when
// neither can answer at all.
//
// The confidence an answer needs is set per option (Sure), because being
// wrong costs differently each way: a gate that lets a message through
// wrongly costs a reply, one that keeps a message out wrongly drops what a
// person said. The safe answer is the one that is cheap to be wrong about.
type Decision struct {
	// Name labels the decision's log lines ("gatekeeper"); About says what
	// it was about in them ("chat=...").
	Name, About string
	Question
	// Sure[i] is how firmly System One must choose option i for it to stand
	// alone (Answer.Firm: every ask chose it, the weakest at least this). An
	// option with none set never stands on System One.
	Sure []float64
	// Full answers in full, with a reason: the worker, an agent. Nil is no
	// full answer, so an unsure System One gives the safe answer.
	Full func(ctx context.Context) (choice int, reason string, err error)
	// Safe is the option taken when nothing answered.
	Safe int
	// Timeout bounds System One's answer; 0 is ten seconds.
	Timeout time.Duration
	// Opts go on System One's call (a route key).
	Opts []ChatOption
}

// How a Decision was settled.
const (
	BySystemOne = "system one"
	ByFull      = "full answer"
	BySafe      = "safe answer"
)

// Resolution is how a Decision was settled: the option, by what (BySystemOne,
// ByFull, BySafe), why, and System One's confidence when it was its answer.
type Resolution struct {
	Choice     int
	By         string
	Reason     string
	Confidence float64
}

// Resolve settles d. Each hand-off from one answer to the next is logged, so
// an answer that went the long way says why.
func Resolve(ctx context.Context, d Decision) Resolution {
	about := ""
	if a := strings.TrimSpace(d.About); a != "" {
		about = " (" + a + ")"
	}
	why := "System One is off"
	key := d.key()
	if m := Model(); m != nil && !Mapped(key) {
		why = "System One is not mapped for this check"
	} else if m != nil {
		timeout := d.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		sctx, cancel := context.WithTimeout(ctx, timeout)
		a, err := Ask(sctx, m, d.Question, d.Opts...)
		cancel()
		switch {
		case err != nil:
			Rest(err)
			why = fmt.Sprintf("System One gave no decision (%v)", err)
		case !a.OK:
			why = "System One gave no decision (neither option was a likely first token)"
		case a.Choice < len(d.Sure) && d.Sure[a.Choice] > 0 && a.Firm() >= d.Sure[a.Choice]:
			return Resolution{Choice: a.Choice, By: BySystemOne, Reason: a.line(d.Options), Confidence: a.Confidence}
		default:
			why = "System One unsure (" + a.line(d.Options) + ")"
		}
		if d.Full != nil {
			Log("[%s] %s: asking the full question%s", d.Name, why, about)
		}
	}
	if d.Full != nil {
		c, reason, err := d.Full(ctx)
		if err == nil && c >= 0 && c < len(d.Options) {
			return Resolution{Choice: c, By: ByFull, Reason: reason}
		}
		if err == nil {
			err = fmt.Errorf("an answer outside the options (%d)", c)
		}
		why = fmt.Sprintf("the full answer failed (%v)", err)
	}
	return Resolution{Choice: d.Safe, By: BySafe, Reason: why}
}

// key is the check the decision is for: its route key, from its options.
func (d Decision) key() string {
	var probe ChatConfig
	for _, opt := range d.Opts {
		opt(&probe)
	}
	return strings.TrimSpace(probe.RouteKey)
}

// Mapped reports whether System One is mapped for check key: its answers
// that would have stood on the check's cases were all right, on the model
// System One is now. Unmapped, a decision goes to its full answer.
func Mapped(key string) bool { return MappedTo(key, MapSystemOne) }

// line is an answer as a log line: the chosen option's probability, the
// confidence, how it was read and how long it took.
func (a Answer) line(options []string) string {
	how := "first-token"
	if a.Native {
		how = fmt.Sprintf("native, label mass %.2f", a.LabelMass)
	}
	label := "?"
	if a.Choice < len(options) {
		label, _, _ = strings.Cut(options[a.Choice], ":")
		label = strings.ToLower(strings.TrimSpace(label))
	}
	return fmt.Sprintf("P(%s)=%.2f%s, firm %.2f (%s) in %s", label, a.Probs[a.Choice], a.OrderLine(a.Choice), a.Firm(), how, a.Took.Round(time.Millisecond))
}

// OrderLine is option i's probability in each order it was asked
// (" (0.95 in order, 0.23 reversed)"), or "" when it was asked once. Two far
// apart is a model leaning to a position; two close and low is a question
// it found hard.
func (a Answer) OrderLine(i int) string {
	if len(a.Orders) < 2 || i < 0 || i >= len(a.Orders[0]) || i >= len(a.Orders[1]) {
		return ""
	}
	return fmt.Sprintf(" (%.2f in order, %.2f reversed)", a.Orders[0][i], a.Orders[1][i])
}
