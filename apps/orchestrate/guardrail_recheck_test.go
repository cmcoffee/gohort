package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const wardenComply = `{"verdicts":[{"rule":"never email outside the company","status":"comply","reason":"internal address"}]}`

// flakyWarden fails its first n calls as a timeout, then complies.
func flakyWarden(n int) *FakeLLM {
	var turns []FakeTurn
	for i := 0; i < n; i++ {
		turns = append(turns, FakeTurn{Err: fmt.Errorf("warden: %w", context.DeadlineExceeded)})
	}
	return &FakeLLM{Turns: append(turns, FakeTurn{Content: wardenComply, Repeat: true})}
}

func strictAgent() AgentRecord {
	return AgentRecord{Name: "Mailer", Guardrails: "never email outside the company",
		GuardrailHooks: []string{"pre_action"}, GuardrailFailClosed: true}
}

const sendCall = `send_email to: "ops@example.com" body: "Restarting the collector"`

// One failed check is retried, wherever it runs, and a retry that reaches a
// verdict decides as any check does.
func TestAFailedCheckIsRetriedOnce(t *testing.T) {
	llm := flakyWarden(1)
	turn := guardTurn(t, llm, strictAgent())
	if turn.guardrailCheckHook()(guardHookPreAction, sendCall).Blocked {
		t.Error("a check that failed once and then passed blocked the action")
	}
	if n := llm.Calls(); n != 2 {
		t.Errorf("the warden was called %d times, want the call and one retry", n)
	}
}

// With nobody watching, a check that still has no verdict after its retry
// blocks an agent that fails closed, as before, and asks no one.
func TestAFailedCheckStillBlocksWhenNobodyIsWatching(t *testing.T) {
	llm := flakyWarden(5)
	turn := guardTurn(t, llm, strictAgent())
	if !turn.guardrailCheckHook()(guardHookPreAction, sendCall).Blocked {
		t.Error("an unattended fail-closed agent let an unchecked action through")
	}
	if n := llm.Calls(); n != 2 {
		t.Errorf("the warden was called %d times, want the call and one retry", n)
	}
}

// The owner, watching, is asked: the card says what was about to happen,
// against which rule, and why the check could not run. Check again runs it
// again, and the action goes ahead because that check passes.
func TestTheOwnerIsAskedToCheckAgain(t *testing.T) {
	llm := flakyWarden(2)
	turn := guardTurn(t, llm, strictAgent())
	buf := &syncBuf{}
	turn.sse = &sseWriter{live: buf}
	done := answerNextCard("u", "recheck")
	if turn.guardrailCheckHook()(guardHookPreAction, sendCall).Blocked {
		t.Error("a re-check that passed still blocked the action")
	}
	<-done
	if n := llm.Calls(); n != 3 {
		t.Errorf("the warden was called %d times, want two failures and the re-check", n)
	}
	card := buf.String()
	for _, want := range []string{"Check again", "Skip it", "before an action", "the check timed out",
		"About to run: send_email", "never email outside the company"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q:\n%s", want, card)
		}
	}
}

// Skip it blocks, as the agent's setting says.
func TestSkipItBlocks(t *testing.T) {
	llm := flakyWarden(5)
	turn := guardTurn(t, llm, strictAgent())
	turn.sse = &sseWriter{live: &syncBuf{}}
	done := answerNextCard("u", "skip")
	if !turn.guardrailCheckHook()(guardHookPreAction, sendCall).Blocked {
		t.Error("Skip it let the action through")
	}
	<-done
	if n := llm.Calls(); n != 2 {
		t.Errorf("the warden was called %d times after Skip it", n)
	}
}

// Someone other than the owner is not shown the owner's rules or asked to
// decide: their turn gets the retry and then the block.
func TestAContactIsNotAskedToCheckAgain(t *testing.T) {
	llm := flakyWarden(5)
	turn := guardTurn(t, llm, strictAgent())
	turn.ownerUser, turn.user = "owner", "contact"
	turn.sse = &sseWriter{live: &syncBuf{}}
	result := make(chan bool, 1)
	go func() { result <- turn.guardrailCheckHook()(guardHookPreAction, sendCall).Blocked }()
	select {
	case blocked := <-result:
		if !blocked {
			t.Error("a contact's unchecked action went ahead")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a contact's turn waited on a re-check card")
	}
	if n := llm.Calls(); n != 2 {
		t.Errorf("the warden was called %d times on a contact's turn", n)
	}
}
