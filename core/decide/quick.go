package decide

import (
	"context"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// quickTimeout bounds System One on a check's hot path: past it, the check
// asks its full question as it always did.
const quickTimeout = 5 * time.Second

func init() { QuickDecide = quickDecide }

// QuickQuestion is q as a System One question, asked both ways round.
func QuickQuestion(q QuickChoice) Question {
	opts := make([]string, len(q.Answers))
	for i, a := range q.Answers {
		opts[i] = a.Means
	}
	return Question{System: q.System, State: q.State, Ask: q.Ask, Options: opts, Both: true}
}

// QuickStands reports whether answer choice of q, chosen as firmly as firm
// (Answer.Firm), stands on System One: one that needs no reason, at
// QuickSure or q's own bar or more.
func QuickStands(q QuickChoice, choice int, firm float64) bool {
	return choice >= 0 && choice < len(q.Answers) && !q.Answers[choice].Reason && firm >= QuickBar(q)
}

// QuickBar is how firm an answer to q must be to stand: q's own bar, or
// QuickSure.
func QuickBar(q QuickChoice) float64 {
	if q.Sure > 0 {
		return q.Sure
	}
	return QuickSure
}

// quickDecide is core.QuickDecide: System One's answer to q when the map
// gives q's check to it and it stands; nothing otherwise.
func quickDecide(ctx context.Context, q QuickChoice) (string, bool) {
	m := Model()
	if m == nil || !Mapped(q.Key) {
		return "", false
	}
	sctx, cancel := context.WithTimeout(ctx, quickTimeout)
	defer cancel()
	a, err := Ask(sctx, m, QuickQuestion(q), WithRouteKey(q.Key))
	if err != nil {
		Rest(err)
		return "", false
	}
	if !a.OK {
		return "", false
	}
	if !QuickStands(q, a.Choice, a.Firm()) {
		Debug("[decide] %s: System One %s, asking the full question", q.Key, a.line(QuickQuestion(q).Options))
		return "", false
	}
	Debug("[decide] %s settled by System One: %s", q.Key, a.line(QuickQuestion(q).Options))
	return q.Answers[a.Choice].Key, true
}
