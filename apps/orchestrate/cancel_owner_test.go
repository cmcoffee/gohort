package orchestrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The cancel endpoint looked a turn up by the session id alone, which comes
// from the request, so any signed-in user could stop another user's turn by
// naming its id. It is looked up under (user, id) now.
func TestCancelOnlyReachesOwnTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inflightCancels.Store(runSessKey("user1", "sess-x"), context.CancelFunc(cancel))
	defer inflightCancels.Delete(runSessKey("user1", "sess-x"))

	T := &OrchestrateApp{}
	T.handleCancel(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/cancel?id=sess-x", nil), "user2", AgentRecord{})
	if ctx.Err() != nil {
		t.Fatal("another user's cancel stopped the turn")
	}
	T.handleCancel(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/cancel?id=sess-x", nil), "user1", AgentRecord{})
	if ctx.Err() == nil {
		t.Fatal("the owner's cancel did not stop the turn")
	}
}

// "A fresh send cancels the old run" was keyed by session id too, so another
// user's send on the same id cancelled the turn, and the running flag and
// stream resume reported the wrong user's run.
func TestRunRegistrySessionsArePerUser(t *testing.T) {
	rr := NewRunRegistry()
	ctx1, run1 := rr.CreateCancellable(context.Background(), "user1", "", "sess-x")
	_, run2 := rr.CreateCancellable(context.Background(), "user2", "", "sess-x")
	time.Sleep(20 * time.Millisecond) // the replace cancels asynchronously
	if ctx1.Err() != nil {
		t.Fatal("another user's run on the same session id cancelled this one")
	}
	if got := rr.BySession("user1", "sess-x"); got != run1 {
		t.Fatalf("user1's session resolved to %v, want its own run", got)
	}
	if got := rr.BySession("user2", "sess-x"); got != run2 {
		t.Fatalf("user2's session resolved to %v, want its own run", got)
	}
	run1.Cancel()
	run2.Cancel()
}
