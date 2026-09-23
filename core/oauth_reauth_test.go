package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/internal/mcpclient"
)

// The distinction the whole recovery turns on: a dead refresh token needs a
// human, a failed attempt needs another attempt. Getting this backwards either
// nags the user for nothing or wedges the credential silently.
func TestOAuthGrantRejected(t *testing.T) {
	terminal := []error{
		errors.New(`token endpoint 400: {"error":"invalid_grant"}`),
		errors.New("INVALID_GRANT: refresh token expired"),
		fmt.Errorf("wrapped: %w", errors.New(`{"error":"invalid_token"}`)),
		errors.New(`{"error":"unauthorized_client"}`),
	}
	for _, err := range terminal {
		if !oauthGrantRejected(err) {
			t.Errorf("should be terminal: %v", err)
		}
	}
	transient := []error{
		nil,
		errors.New("dial tcp: connection refused"),
		errors.New("token endpoint 500: internal server error"),
		errors.New("context deadline exceeded"),
		errors.New(`{"error":"temporarily_unavailable"}`),
	}
	for _, err := range transient {
		if oauthGrantRejected(err) {
			t.Errorf("should be retryable: %v", err)
		}
	}
}

// A 401 must arrive at the manager as a TYPED error. It used to be "http 401"
// in a string, and keying a recovery off error text is how a message reword
// silently turns the recovery off.
func TestUnauthorizedIsTyped(t *testing.T) {
	err := fmt.Errorf("%w (http %d: %s)", mcpclient.ErrUnauthorized, 401, "bad token")
	if !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Error("a wrapped 401 must still match ErrUnauthorized")
	}
	if errors.Is(err, mcpclient.ErrSessionExpired) {
		t.Error("a rejected CREDENTIAL is not an expired SESSION — they recover differently")
	}
}

// One prompt per dead credential, not one per tool call.
//
// A turn calling six tools through the same credential reported the same
// "reconnect this" failure six times: each call resolved the credential on its
// own, found the same dead token, and said so. The model read each as a fresh
// failure and retried around them, so the count was usually worse than the
// number of tools.
func TestOnePromptPerDeadCredentialNotOnePerCall(t *testing.T) {
	resetReauth(t)

	// The first call to find it dead is the one that tells the user.
	if !ReauthAnnounce("alice", "issue_tracker") {
		t.Fatal("the first caller was not given the announcement")
	}
	// Every other call for the SAME credential is not.
	for i := 0; i < 5; i++ {
		if ReauthAnnounce("alice", "issue_tracker") {
			t.Fatalf("call %d announced again, so the user is asked twice", i+2)
		}
	}
	if !ReauthPending("alice", "issue_tracker") {
		t.Error("the credential does not read as pending")
	}

	// A DIFFERENT credential is a different question, and a different person's
	// copy of the same credential is too: one person reconnecting theirs says
	// nothing about anybody else's.
	if !ReauthAnnounce("alice", "calendar") {
		t.Error("a second credential was silenced by the first")
	}
	if !ReauthAnnounce("bob", "issue_tracker") {
		t.Error("one user's reconnect silenced another user's")
	}
	// And an MCP server does not share a name with a credential.
	if !ReauthAnnounce("alice", mcpReauthKey("issue_tracker")) {
		t.Error("an MCP server collided with a credential of the same name")
	}
}

// A waiter is released by the token being STORED, and finishes the work it was
// already doing rather than reporting a failure.
func TestAWaiterIsReleasedWhenTheReconnectLands(t *testing.T) {
	resetReauth(t)
	ReauthAnnounce("alice", "issue_tracker")

	got := make(chan bool, 1)
	go func() { got <- ReauthWait(context.Background(), "alice", "issue_tracker") }()

	// Nothing should be released before the reconnect lands.
	select {
	case v := <-got:
		t.Fatalf("the waiter returned %v before the reconnect", v)
	case <-time.After(20 * time.Millisecond):
	}

	ReauthResolved("alice", "issue_tracker")
	select {
	case v := <-got:
		if !v {
			t.Error("the waiter was released but reported the reconnect as not done")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter was never released")
	}
	// And the barrier is clear, so the next failure asks again rather than
	// waiting on something that already finished.
	if ReauthPending("alice", "issue_tracker") {
		t.Error("the entry outlived the reconnect")
	}
	if !ReauthAnnounce("alice", "issue_tracker") {
		t.Error("a later failure could not ask again")
	}
}

// The wait ends. A human is being asked to leave the page and come back, and a
// tool call cannot hold a turn open indefinitely for that.
func TestTheWaitDoesNotHoldATurnForever(t *testing.T) {
	resetReauth(t)

	// Nothing pending: nothing to wait for.
	if ReauthWait(context.Background(), "alice", "issue_tracker") {
		t.Error("waited on a reconnect nobody asked for")
	}

	// A cancelled turn stops waiting.
	ReauthAnnounce("alice", "issue_tracker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ReauthWait(ctx, "alice", "issue_tracker") {
		t.Error("a cancelled turn kept waiting")
	}

	// An entry older than the window is stale - the person never came back, or
	// the completion was missed - and a stale entry that never expired would
	// silence the prompt forever.
	reauthMu.Lock()
	reauthWaiting[reauthKey("alice", "issue_tracker")].at = time.Now().Add(-2 * reauthWaitWindow)
	reauthMu.Unlock()
	if ReauthPending("alice", "issue_tracker") {
		t.Error("a stale entry still reads as pending")
	}
	if !ReauthAnnounce("alice", "issue_tracker") {
		t.Error("a stale entry silenced the prompt forever")
	}
	if ReauthWait(ctx, "alice", "issue_tracker") {
		t.Error("waiting on a stale entry succeeded")
	}
}

// Nothing to coalesce on behaves exactly as before, rather than silently
// grouping every anonymous failure together.
func TestABlankKeyIsNotABarrier(t *testing.T) {
	resetReauth(t)
	for i := 0; i < 3; i++ {
		if !ReauthAnnounce("", "issue_tracker") {
			t.Error("a blank user was coalesced")
		}
		if !ReauthAnnounce("alice", "") {
			t.Error("a blank credential was coalesced")
		}
	}
}

func resetReauth(t *testing.T) {
	t.Helper()
	reauthMu.Lock()
	reauthWaiting = map[string]*reauthPending{}
	reauthMu.Unlock()
	t.Cleanup(func() {
		reauthMu.Lock()
		reauthWaiting = map[string]*reauthPending{}
		reauthMu.Unlock()
	})
}
