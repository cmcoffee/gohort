package imagefetch

import (
	"image/color"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func resetChains() {
	chains.Lock()
	chains.m = map[string]*passChain{}
	chains.Unlock()
}

// sources is n pictures: the one to change, then references.
func sources(n int) []string {
	out := []string{"image#scene"}
	for i := 1; i < n; i++ {
		out = append(out, "image#ref"+string(rune('0'+i)))
	}
	return out
}

// A chain of passes is counted as one, across the wakes that start its
// passes: it stops at two passes per person plus one, naming the last pass
// that passed; a request from the person starts a new count.
func TestAChainOfPassesStopsAtItsBudget(t *testing.T) {
	resetChains()
	now := time.Now()
	person := &ToolSession{Username: "u", ChatSessionID: "c", Request: "put us all on the stage"}
	wake := &ToolSession{Username: "u", ChatSessionID: "c"}
	src := sources(3) // the scene and two people: five passes
	if err := chainBegin(person, src, now); err != nil {
		t.Fatalf("the person's own request: %v", err)
	}
	chainRecord(person, true, false, "image#r.one", now)
	for i := 2; i <= chainLimit(len(src)); i++ {
		if err := chainBegin(wake, src, now); err != nil {
			t.Fatalf("pass %d refused: %v", i, err)
		}
		chainRecord(wake, true, false, "image#r.p"+string(rune('0'+i)), now)
	}
	err := chainBegin(wake, src, now)
	if err == nil || !strings.Contains(err.Error(), "made 5 passes") || !strings.Contains(err.Error(), "image#r.p5, the last pass that passed") {
		t.Fatalf("past the budget: %v", err)
	}
	if err := chainBegin(wake, src, now); err == nil {
		t.Error("a stopped chain started again from a wake")
	}
	if err := chainBegin(person, src, now); err != nil {
		t.Errorf("the person asking again did not start a new count: %v", err)
	}
}

// Delivered passes count as well as held ones: a delivered edit whose check
// failed woke a turn that tried again on the failed result, and that one's
// result woke the next. The retry is pointed at the picture the person asked
// to change while nothing has passed, and the chain stops after two failures
// in a row, naming that picture.
func TestDeliveredRetriesCountAndPointBack(t *testing.T) {
	resetChains()
	now := time.Now()
	person := &ToolSession{Username: "u", ChatSessionID: "c", Request: "swap the guy on the left with my reference photo"}
	wake := &ToolSession{Username: "u", ChatSessionID: "c"}
	chainBegin(person, []string{"media#1", "image#face"}, now)
	base := chainRecord(person, true, true, "image#r.fail1", now)
	if base != "media#1" || !strings.Contains(chainRetryNote(base), "edit media#1 as picture, not this result") {
		t.Errorf("after a first failure, the retry builds on %q", base)
	}
	// The retry, from the wake, built on the failed result: still one chain.
	if err := chainBegin(wake, []string{"image#r.fail1", "image#face"}, now); err != nil {
		t.Fatalf("the first retry: %v", err)
	}
	chainRecord(wake, true, true, "image#r.fail2", now)
	err := chainBegin(wake, []string{"image#r.fail2", "image#face"}, now)
	if err == nil || !strings.Contains(err.Error(), "last 2 passes did not pass their check") || !strings.Contains(err.Error(), "media#1 (the picture they asked to change)") {
		t.Fatalf("a third try after two failures: %v", err)
	}
}

// A pass that passes resets the run of failures, and becomes what a later
// failure points back to.
func TestAPassBetweenFailuresKeepsTheChainGoing(t *testing.T) {
	resetChains()
	now := time.Now()
	person := &ToolSession{Username: "u", ChatSessionID: "c", Request: "four of us"}
	wake := &ToolSession{Username: "u", ChatSessionID: "c"}
	src := sources(5)
	chainBegin(person, src, now)
	chainRecord(person, true, true, "a", now)
	chainBegin(wake, src, now)
	chainRecord(wake, true, false, "b", now)
	chainBegin(wake, src, now)
	if base := chainRecord(wake, true, true, "c", now); base != "b" {
		t.Errorf("a failure after a pass points at %q, want the pass", base)
	}
	if err := chainBegin(wake, src, now); err != nil {
		t.Errorf("failures with a pass between them stopped the chain: %v", err)
	}
	if chainLimit(1) != 3 || chainLimit(5) != 9 || chainLimit(40) != chainPassesDefault {
		t.Errorf("limits: %d %d %d", chainLimit(1), chainLimit(5), chainLimit(40))
	}
}

// The picture the person asked to change is remembered by a name that still
// works in the wake a retry starts from: a photo sent with the message
// (media#1, which names nothing once that turn is over) by its stable ring id.
func TestTheOriginIsRememberedByALastingName(t *testing.T) {
	resetChains()
	sess := fidelitySession(t)
	sess.ChatSessionID, sess.Request = "c", "swap me in"
	photo := pngOf(t, 40, 20, color.RGBA{10, 20, 30, 255})
	sess.RegisterInboundMediaDescribed("image", photo, "a friend", "")
	_, stable := RecordRecentImageStable(sess, photo, "received", ImageFromUser)
	if stable == "" {
		t.Fatal("setup: no stable id")
	}
	chainBegin(sess, []string{"media#1", "image#face"}, time.Now())
	if base := chainRecord(sess, true, true, "image#r.fail", time.Now()); base != stable {
		t.Errorf("the retry is pointed at %q, want the photo's lasting id %q", base, stable)
	}
}
