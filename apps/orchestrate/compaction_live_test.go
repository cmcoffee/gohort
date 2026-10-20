package orchestrate

// A history fold shows while it runs. It makes model calls of its own, and
// with no run behind them they were activity nothing on the page could
// account for: the live ribbon and the Monitor showed nothing, and a peer
// lending its model saw an unnamed call.

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAFoldShowsWhileItRuns(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	hold := make(chan struct{})
	T := &OrchestrateApp{AppCore: AppCore{DB: db, LLM: &FakeLLM{Turns: []FakeTurn{{Content: "Running summary.", Wait: hold, Repeat: true}}}}}
	agent := AgentRecord{ID: "ag", Name: "Wiwee", Owner: "u", ContextDepth: 4}
	seedFoldSession(t, db, "ag", "s1", 30)
	// The turn that set the fold off, running in the same conversation. A run
	// registered under a session replaces the one there, so a fold registered
	// under it cancelled this turn and a channel answered with an apology.
	turnCtx, turn := T.runsRegistry().CreateCancellable(context.Background(), "u", "ag", "s1")
	defer turn.Complete(RunStatusCompleted)

	done := make(chan struct{})
	go func() {
		defer close(done)
		T.foldOperatorHistory(db, "u", agent, "s1", CompactionConfig{KeepRecent: 4, Trigger: 6})
	}()
	var fold RunSnapshot
	deadline := time.Now().Add(5 * time.Second)
	for fold.ID == "" && time.Now().Before(deadline) {
		for _, s := range T.runsRegistry().ActiveSnapshots() {
			if s.Kind == "compaction" {
				fold = s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fold.ID == "" {
		close(hold)
		t.Fatal("no run showed while the fold was summarizing")
	}
	if fold.UserID != "u" || fold.AgentID != "ag" || fold.AgentName != "Wiwee" || !strings.Contains(fold.Detail, "summarizing") || !fold.Cancellable {
		t.Errorf("the fold's run should name whose thread it is and offer a Stop: %+v", fold)
	}
	if turnCtx.Err() != nil {
		t.Error("the fold cancelled the turn running in its conversation")
	}
	if r := T.runsRegistry().BySession("u", "s1"); r == nil || r.ID != turn.ID {
		t.Error("the conversation's run is no longer the turn: a page reattaching would find the fold")
	}
	if !markBackground([]RunSnapshot{fold})[fold.ID] {
		t.Error("a fold should read as background work")
	}

	close(hold)
	<-done
	for _, s := range T.runsRegistry().ActiveSnapshots() {
		if s.ID == fold.ID {
			t.Error("the fold's run is still active after it finished")
		}
	}
	if st := loadCompactState(db, "ag", "s1"); st.FoldSeq != 1 {
		t.Errorf("the fold should still complete: %+v", st)
	}
}
