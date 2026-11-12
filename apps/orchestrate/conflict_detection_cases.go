package orchestrate

// The memory conflict check's check tests (core/checktest): a finding about
// to be saved beside existing ones, with which of them it contradicts, asked
// through the check's own prompt (judgeFindingConflicts) on the model a run
// is for.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   "memory.conflict_check",
		Label: "Memory conflict check",
		Cases: conflictCases,
		Run:   runConflictCase,
		// Structured: offloadable (checktest.Check.Structured).
		Structured: true,
		Quick:      func(c checktest.Case) QuickChoice { return conflictQuick(c.Input["new"], conflictHits(c)) },
	})
}

// conflictCases are the check's built-in cases. Input: new, existing (one
// finding a line). Want: "none", or "contradicts" and the numbers.
func conflictCases() []checktest.Case {
	c := func(id, title, want, newFinding string, existing ...string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want,
			Input: map[string]string{"new": newFinding, "existing": strings.Join(existing, "\n")}}
	}
	return []checktest.Case{
		c("reopened", "A state that changed", "contradicts 1",
			"The bridge reopened to traffic on May 2 after repairs finished.",
			"The bridge has been closed to all traffic since March and remains closed.", "The bridge was built in 1962."),
		c("more-detail", "A detail added, not a contradiction", "none",
			"The bridge repairs cost 4 million dollars.",
			"The bridge was closed in March for repairs.", "The bridge was built in 1962."),
		c("agrees", "The same fact in other words", "none",
			"The company has about 120 employees.",
			"The company employs roughly 120 people.", "The company is based in Lyon."),
		c("other-aspect", "A different aspect of the same thing", "none",
			"The library added opening hours on Sundays.",
			"The library is closed on Mondays.", "The library has a children's reading room."),
		c("two-at-once", "One finding against two", "contradicts 1, 3",
			"The trial found the drug had no effect on blood pressure.",
			"The trial found the drug lowered blood pressure by 10 points.", "The trial enrolled 300 adults.", "In the trial, the drug lowered blood pressure."),
		c("moved", "A place that changed", "contradicts 1",
			"This year the festival moved to the riverside park.",
			"The festival is held in the town square.", "The festival runs for three days."),
	}
}

// conflictHits are a case's existing findings as the check is given them.
func conflictHits(tc checktest.Case) []SearchHit {
	var hits []SearchHit
	for _, ln := range strings.Split(tc.Input["existing"], "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			hits = append(hits, SearchHit{ID: fmt.Sprint(len(hits) + 1), Text: ln})
		}
	}
	return hits
}

// runConflictCase asks one case on llm through the check itself.
func runConflictCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	hits := conflictHits(tc)
	index := map[string]int{}
	for i, h := range hits {
		index[h.Text] = i + 1
	}
	found, err := judgeFindingConflicts(llm.Chat, tc.Input["new"], hits)
	if err != nil {
		// No verdict, not "none": a reply that could not be read scored as
		// finding no conflict, so a model answering in another shape read
		// as getting every contradiction wrong.
		return checktest.Outcome{Got: ScanNoVerdict, Reason: err.Error()}
	}
	if len(found) == 0 {
		return checktest.Outcome{Got: "none"}
	}
	var nums []int
	for _, h := range found {
		nums = append(nums, index[h.Text])
	}
	sort.Ints(nums)
	var out []string
	for _, n := range nums {
		out = append(out, fmt.Sprint(n))
	}
	return checktest.Outcome{Got: "contradicts " + strings.Join(out, ", ")}
}
