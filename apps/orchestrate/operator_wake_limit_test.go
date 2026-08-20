// The operator webhook is guarded by an unguessable token and nothing else.
// Past that token every POST read an unbounded body, put it into a prompt, and
// launched a detached goroutine running a full Operator turn — so one leaked
// webhook URL was unbounded LLM spend and unbounded goroutine fan-out.
package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func wakeFixture(t *testing.T) {
	t.Helper()
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	prevFires, prevFails := operatorEventFires, operatorEventFailures
	t.Cleanup(func() {
		RootDB = prevRoot
		operatorEventFires, operatorEventFailures = prevFires, prevFails
	})
}

func postEvent(token, body, addr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost,
		"/orchestrate/api/operator/event/"+token, strings.NewReader(body))
	r.RemoteAddr = addr
	return r
}

func TestUnknownTokenIsThrottledPerSource(t *testing.T) {
	// A token that resolves to nothing still costs a scan of every monitor
	// before it can be refused, so guessing must not be free.
	wakeFixture(t)
	operatorEventFailures = NewRateLimiter(3, time.Minute)
	T := &OrchestrateApp{}

	refused := 0
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		T.handleOperatorEvent(w, postEvent("guess", `{"summary":"x"}`, "203.0.113.5:5555"))
		switch w.Code {
		case http.StatusTooManyRequests:
			refused++
		case http.StatusNotFound:
			// expected for the first few
		default:
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	if refused == 0 {
		t.Fatal("unlimited guessing at the webhook token")
	}
}

func TestKnownMonitorHasAFiringCeiling(t *testing.T) {
	// Keyed on the MONITOR, not the caller: the token authorizes the work, so
	// it carries the budget, and driving it from many addresses must not
	// multiply what one token can spend.
	wakeFixture(t)
	operatorEventFires = NewRateLimiter(2, time.Minute)
	SaveEventMonitor(RootDB, EventMonitor{
		Owner: "craig", Name: "watch", Kind: EventKindWebhook, Token: "tok-abc", Paused: true,
	})
	T := &OrchestrateApp{}

	// Paused, so nothing actually fires — the ceiling is what is under test.
	allowed, refused := 0, 0
	for i := 0; i < 6; i++ {
		w := httptest.NewRecorder()
		T.handleOperatorEvent(w, postEvent("tok-abc", `{"summary":"x"}`, "203.0.113."+string(rune('1'+i))+":5555"))
		if w.Code == http.StatusTooManyRequests {
			refused++
		} else {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("expected 2 wakes under the ceiling, got %d", allowed)
	}
	if refused != 4 {
		t.Errorf("expected 4 refusals from different addresses, got %d", refused)
	}
}

func TestEventBodyIsBounded(t *testing.T) {
	// The decoded text goes straight into an Operator prompt, so an unbounded
	// read was both a memory question and a token-spend one.
	wakeFixture(t)
	SaveEventMonitor(RootDB, EventMonitor{
		Owner: "craig", Name: "watch", Kind: EventKindWebhook, Token: "tok-abc", Paused: true,
	})
	T := &OrchestrateApp{}

	huge := `{"summary":"` + strings.Repeat("A", maxOperatorEventBody*2) + `"}`
	w := httptest.NewRecorder()
	T.handleOperatorEvent(w, postEvent("tok-abc", huge, "203.0.113.5:5555"))
	// The oversized body is cut off, so the decode fails and the handler
	// proceeds with an empty summary rather than holding megabytes.
	if w.Code == http.StatusOK && w.Body.Len() > maxOperatorEventBody {
		t.Error("an oversized body was echoed back whole")
	}
	if maxOperatorEventBody > 1<<20 {
		t.Errorf("the body cap is too generous to be a cap: %d", maxOperatorEventBody)
	}
}

func TestMethodOtherThanPostIsRefused(t *testing.T) {
	wakeFixture(t)
	T := &OrchestrateApp{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/orchestrate/api/operator/event/tok-abc", nil)
	T.handleOperatorEvent(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should be refused, got %d", w.Code)
	}
}
