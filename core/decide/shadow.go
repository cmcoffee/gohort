package decide

// Shadows: System One asked the question a step with fixed answers answers
// (a machine step that only routes, a pipeline stage whose whole output is
// one yes/no or one-of field), beside the step and never instead of it. Such
// a step could be System One's, but a wrong answer runs a whole wrong path;
// so each step earns it first. The tally per step says how often System
// One was sure, and how often sure and right (agreeing with the step), on
// the deployment's own traffic.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { ShadowRoute = shadowRoute }

const (
	// ShadowTable keeps the tallies, one record per machine step.
	ShadowTable = "system_one_shadow"
	// RouteSure is how firmly a route must be chosen to stand on System One
	// (Answer.Firm): one mark for every destination, since an author's
	// routes have no direction that is cheap to be wrong about.
	RouteSure = 0.75
	// shadowWait is how long a step that has chosen waits for System One's
	// answer before its trace moves on; the answer is still tallied.
	shadowWait = 2 * time.Second
	// shadowTimeout bounds System One's answer.
	shadowTimeout = 10 * time.Second
)

const routeShadowSys = `You answer for one step of a workflow. You are shown everything the step is shown, and the answers it may give. Pick the answer the step's own instructions call for.`

// ShadowTally is how System One did on one router step: sure and agreeing,
// sure and choosing otherwise, unsure (it would have left the choice to the
// step), and failing to answer.
type ShadowTally struct {
	Machine  string    `json:"machine"`
	Step     string    `json:"step"`
	Agreed   int       `json:"agreed"`
	Differed int       `json:"differed"`
	Unsure   int       `json:"unsure"`
	Failed   int       `json:"failed"`
	Last     string    `json:"last,omitempty"`
	At       time.Time `json:"at"`
}

// Line is the tally as a sentence.
func (t ShadowTally) Line() string {
	sure := t.Agreed + t.Differed
	return fmt.Sprintf("%s, step %s: sure %d of %d times, and agreed with the step %d of those %d; unsure %d, no answer %d.",
		t.Machine, t.Step, sure, sure+t.Unsure+t.Failed, t.Agreed, sure, t.Unsure, t.Failed)
}

var shadowMu sync.Mutex

// Shadows is every router step's tally, by machine and step.
func Shadows() []ShadowTally {
	var out []ShadowTally
	if RootDB == nil {
		return nil
	}
	for _, k := range RootDB.Keys(ShadowTable) {
		var t ShadowTally
		if RootDB.Get(ShadowTable, k, &t) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Machine != out[j].Machine {
			return out[i].Machine < out[j].Machine
		}
		return out[i].Step < out[j].Step
	})
	return out
}

// tally adds one outcome to machine/step's record.
func tally(machine, step, last string, add func(*ShadowTally)) {
	if RootDB == nil {
		return
	}
	shadowMu.Lock()
	defer shadowMu.Unlock()
	key := machine + "\x00" + step
	var t ShadowTally
	RootDB.Get(ShadowTable, key, &t)
	t.Machine, t.Step, t.Last, t.At = machine, step, last, time.Now()
	add(&t)
	RootDB.Set(ShadowTable, key, t)
}

type shadowResult struct {
	a   Answer
	err error
}

// shadowRoute is core.ShadowRoute: System One asked now, compared when the
// step has chosen.
func shadowRoute(ctx context.Context, machine, step, prompt string, choices, descs []string) func(string) string {
	m := Model()
	if m == nil || len(choices) < 2 {
		return nil
	}
	opts := make([]string, len(choices))
	for i, c := range choices {
		opts[i] = strings.TrimSpace(c)
		if i < len(descs) && strings.TrimSpace(descs[i]) != "" {
			opts[i] = strings.TrimSpace(descs[i])
		}
	}
	ch := make(chan shadowResult, 1)
	go func() {
		// Its own deadline, off the turn's: a turn that ends first does not
		// cut the answer the tally is waiting for.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shadowTimeout)
		defer cancel()
		a, err := Ask(sctx, m, Question{System: routeShadowSys, State: prompt, Ask: "Which answer does the step give?", Options: opts, Both: true},
			WithRouteKey("machine.route_shadow"))
		Rest(err)
		ch <- shadowResult{a, err}
	}()
	return func(chosen string) string {
		chosen = strings.TrimSpace(chosen)
		compare := func(r shadowResult) string {
			switch {
			case r.err != nil || !r.a.OK:
				why := "neither option was a likely first token"
				if r.err != nil {
					why = r.err.Error()
				}
				tally(machine, step, "no answer: "+why, func(t *ShadowTally) { t.Failed++ })
				return "System One gave no answer (" + why + ")"
			}
			picked := strings.TrimSpace(choices[r.a.Choice])
			line := fmt.Sprintf("System One chose %s, firm %.2f%s, in %s", picked, r.a.Firm(), r.a.OrderLine(r.a.Choice), r.a.Took.Round(time.Millisecond))
			switch {
			case r.a.Firm() < RouteSure:
				tally(machine, step, line, func(t *ShadowTally) { t.Unsure++ })
				return line + ": unsure, it would have left the answer to the step"
			case picked == chosen:
				tally(machine, step, line, func(t *ShadowTally) { t.Agreed++ })
				return line + ": sure, and the step chose the same"
			}
			tally(machine, step, line+"; the step chose "+chosen, func(t *ShadowTally) { t.Differed++ })
			return line + ": sure, but the step chose " + chosen
		}
		select {
		case r := <-ch:
			return compare(r)
		case <-time.After(shadowWait):
			go func() { compare(<-ch) }()
			return "System One had not answered when the step chose; its answer is tallied when it comes"
		}
	}
}
