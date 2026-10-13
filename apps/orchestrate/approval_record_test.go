package orchestrate

// An approval card lives in the run's event stream, which ends with the run
// or a restart. Its answer is kept with the turn's tool runs, so the saved
// conversation still says what was asked, with what, and how it was answered.

import (
	"strings"
	"testing"
	"time"
)

// answerNextCard answers the next card asked of user with value.
func answerNextCard(user, value string) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			answered := false
			toolConfirms.Range(func(k, v any) bool {
				if p := v.(*pendingToolConfirm); p.user == user {
					toolConfirms.Delete(k)
					p.resolve(value)
					answered = true
					return false
				}
				return true
			})
			if answered {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return done
}

func TestAnApprovalIsKeptWithTheTurnsCalls(t *testing.T) {
	turn := &chatTurn{user: "approver", sse: &sseWriter{live: &syncBuf{}}}
	// The loop hands Confirm its own "key: value" lines (core formatArgs),
	// keys sorted, not JSON.
	args := "max_results: 5\nquery: acme merger terms"
	done := answerNextCard("approver", "allow")
	if !turn.escalateToolConfirm(toolConfirmRequest{tool: "web_search", prompt: "Allow web_search to run?",
		detail: confirmCallSummary(nil, "web_search", args), because: "asks", args: args}) {
		t.Fatal("the allow was not taken")
	}
	<-done
	done = answerNextCard("approver", "deny")
	turn.escalateToolConfirm(toolConfirmRequest{tool: "web_search", prompt: "Allow web_search to run?", because: "asks", args: args})
	<-done

	saved := turn.persistedToolCalls()
	if len(saved) != 2 {
		t.Fatalf("saved %d records, want the two answers: %+v", len(saved), saved)
	}
	for i, want := range []string{"Allowed", "Denied"} {
		r := saved[i]
		if !r.Framework || r.Result != want || r.Args["query"] != "acme merger terms" || !strings.Contains(r.Label, "Allow web_search") {
			t.Errorf("record %d = %+v, want a framework record of %q with the query", i, r, want)
		}
	}
	// The model is not told it made them.
	if strings.Contains(turn.toolLogPromptSection(), "approval") {
		t.Error("an approval record reached the model's list of its own calls")
	}
}

// The card shows what the call would send right under its question, ahead of
// the tool's description.
func TestTheCardLeadsWithTheCallsArguments(t *testing.T) {
	for _, args := range []string{`{"max_results":5,"query":"acme merger terms"}`, "max_results: 5\nquery: acme merger terms"} {
		got := confirmCallSummary(nil, "some_tool", args)
		if !strings.HasPrefix(got, "query: acme merger terms\nmax_results: 5") {
			t.Errorf("from %q the query is not first on the card: %q", args, got)
		}
	}
	// A multi-line value stays one argument.
	if got := parseCallArgs("body: line one\nline two\nto: ops"); got["body"] != "line one\nline two" || got["to"] != "ops" {
		t.Errorf("parsed %v", got)
	}
}
