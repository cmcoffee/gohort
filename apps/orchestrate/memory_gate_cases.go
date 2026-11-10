package orchestrate

// The memory gate's check tests (core/checktest): notes about to be saved,
// with what the gate should decide, asked through the gate's own prompt
// (core.FactGateDecision) on the model a run is for.

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   "memory.gate",
		Label: "Memory gate",
		Cases: memoryGateCases,
		Run:   runMemoryGateCase,
		// Structured: offloadable (checktest.Check.Structured).
		Structured: true,
		Quick:      func(c checktest.Case) QuickChoice { return FactGateQuick(c.Input["note"]) },
	})
}

// memoryGateDay is the date the gate is told it is, so a case's dates mean
// the same on every run.
var memoryGateDay = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// memoryGateCases are the gate's built-in cases. Input: note, existing (one
// saved note a line). Want: "drop", or "keep <kind>" with ", replaces N" for
// each existing note it replaces.
func memoryGateCases() []checktest.Case {
	c := func(id, title, want, note string, existing ...string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want,
			Input: map[string]string{"note": note, "existing": strings.Join(existing, "\n")}}
	}
	return []checktest.Case{
		c("small-talk", "Small talk", "drop", "User said good morning and asked how things are going."),
		c("right-now", "A passing state", "drop", "It is raining where the user is right now."),
		c("a-name", "Someone's name", "keep fact", "User's partner is named Sam."),
		c("moved", "A fact that replaces an older one", "keep fact, replaces 1",
			"User has moved and now lives in Austin.", "User lives in Denver.", "User likes hiking."),
		c("both-true", "Two preferences that can both hold", "keep fact",
			"User likes green tea.", "User likes coffee in the morning."),
		c("appointment", "Something on a date", "keep event", "User has a dentist appointment on 2026-12-03."),
		c("later", "Something still to do", "keep open item", "User wants to look into switching phone plans when the contract ends, still undecided."),
		c("birthday", "A date that comes round every year", "keep fact", "User's birthday is June 14."),
	}
}

// runMemoryGateCase asks one case on llm through the gate itself.
func runMemoryGateCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	var existing []string
	for _, ln := range strings.Split(tc.Input["existing"], "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			existing = append(existing, ln)
		}
	}
	keep, replaces, kind := FactGateDecision(llm.Chat, tc.Input["note"], existing, memoryGateDay)
	return checktest.Outcome{Got: memoryGateAnswer(keep, replaces, kind)}
}

// memoryGateAnswer is a gate decision in a case's words.
func memoryGateAnswer(keep bool, replaces []int, kind string) string {
	if !keep {
		return "drop"
	}
	out := "keep " + kind
	for _, n := range replaces {
		out += fmt.Sprintf(", replaces %d", n)
	}
	return out
}
