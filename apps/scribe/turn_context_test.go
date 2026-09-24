package scribe

// Long work in Scribe ran on the request's context, so closing the page
// cancelled it: a chat tool that dispatches its own sub-run died underneath a
// turn that carried on, and "Curate now" stopped partway through a batch.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func TestChatToolsOutliveThePageButNotAStop(t *testing.T) {
	reqCtx, closePage := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/scribe/chat/send", nil).WithContext(reqCtx)
	turnCtx, endTurn := turnContext(r)
	defer endTurn()

	closePage()
	if turnCtx.Err() != nil {
		t.Fatal("closing the page cancelled the context the chat tools are built on")
	}

	// A tool that roots its work on the context it was BUILT with, the way a
	// source's investigate tool does, rather than on its call context.
	tools := followTurn([]AgentToolDef{{
		Tool: Tool{Name: "investigate_x"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			select {
			case <-turnCtx.Done():
				return "stopped", nil
			case <-time.After(5 * time.Second):
				return "ran on", nil
			}
		},
	}}, endTurn)

	// A Stop cancels the call's context while it is in flight.
	callCtx, stop := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		out, _ := tools[0].Handler(callCtx, nil)
		done <- out
	}()
	time.Sleep(20 * time.Millisecond)
	stop()
	select {
	case out := <-done:
		if out != "stopped" {
			t.Errorf("the tool finished with %q; a Stop should have reached it", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a Stop did not reach work rooted on the tool's build context")
	}
}

func TestACallThatReturnsDoesNotEndTheTurn(t *testing.T) {
	r := httptest.NewRequest("POST", "/scribe/chat/send", nil)
	turnCtx, endTurn := turnContext(r)
	defer endTurn()
	tools := followTurn([]AgentToolDef{{
		Tool:    Tool{Name: "noop"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil },
	}}, endTurn)
	callCtx, cancel := context.WithCancel(context.Background())
	if _, err := tools[0].Handler(callCtx, nil); err != nil {
		t.Fatal(err)
	}
	// The call context ending AFTER the call returned is the next round's
	// business, not a Stop of this one.
	cancel()
	time.Sleep(10 * time.Millisecond)
	if turnCtx.Err() != nil {
		t.Error("a finished call's context ending cancelled the rest of the turn")
	}
}

// "Curate now" took no lock, so a press landing while the threshold or interval
// run was working read the same queue and filed the findings twice.
func TestCurateNowWaitsItsTurn(t *testing.T) {
	udb := testStore(t)
	udb.Set(findingsTable, "f1", mkFinding("topic", "content", "verified"))
	mu := curatorLock("curate-now-user")
	mu.Lock()
	defer mu.Unlock()

	w := httptest.NewRecorder()
	(&Scribe{}).handleCuratorRunNow(w, httptest.NewRequest(http.MethodPost, "/curator/run", nil), udb, "curate-now-user")
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Ran    bool   `json:"ran"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Ran || got.Reason == "" {
		t.Errorf("a second run started alongside the one in flight: %+v", got)
	}
}
