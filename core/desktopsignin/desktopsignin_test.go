package desktopsignin

import (
	"testing"
	"time"
)

// A sign-in is confirmed once, by someone, and claimed once, with the secret
// whose hash started it. Before it is confirmed the app is told to wait; a
// wrong secret or the time running out ends it.
func TestASignInIsConfirmedAndClaimedOnce(t *testing.T) {
	if _, _, err := Start("not-a-hash"); err == nil {
		t.Error("a sign-in started with no challenge")
	}
	code, phrase, err := Start(Challenge("secret-1"))
	if err != nil || code == "" || len(phrase) != 9 {
		t.Fatalf("start: %q %q %v", code, phrase, err)
	}
	if got, ok := Phrase(code); !ok || got != phrase {
		t.Errorf("the page's phrase is %q %v, want %q", got, ok, phrase)
	}
	if _, st := Claim(code, "secret-1"); st != Waiting {
		t.Errorf("before confirming: %v, want Waiting", st)
	}
	if !Confirm(code, "dana") {
		t.Fatal("confirm refused")
	}
	if Confirm(code, "erin") {
		t.Error("a confirmed sign-in was confirmed again, for someone else")
	}
	if _, ok := Phrase(code); ok {
		t.Error("the page still offers a confirmed sign-in")
	}
	if user, st := Claim(code, "secret-1"); st != Ready || user != "dana" {
		t.Errorf("claim: %q %v, want dana Ready", user, st)
	}
	if _, st := Claim(code, "secret-1"); st != Gone {
		t.Errorf("a second claim: %v, want Gone", st)
	}

	// Someone holding only the code cannot claim it, and trying ends it.
	code, _, _ = Start(Challenge("secret-2"))
	Confirm(code, "dana")
	if _, st := Claim(code, "guess"); st != Gone {
		t.Errorf("a wrong secret: %v, want Gone", st)
	}
	if _, st := Claim(code, "secret-2"); st != Gone {
		t.Errorf("after a wrong secret the right one still claimed: %v", st)
	}

	code, _, _ = Start(Challenge("secret-4"))
	Confirm(code, "dana")
	prev := now
	now = func() time.Time { return prev().Add(TTL + time.Minute) }
	defer func() { now = prev }()
	if _, st := Claim(code, "secret-4"); st != Gone {
		t.Errorf("past its time: %v, want Gone", st)
	}
}
