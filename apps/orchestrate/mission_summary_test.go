package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A short mission shows as written; a long one shows its summary once one is
// written for its current text, and its first forty words until then.
// Observed: a few paragraphs of mission printed in full on every Scheduler row.
func TestMissionForList(t *testing.T) {
	short := "Post one thought to the feed."
	if got := missionForList(StandingAgent{Mission: short}); got != short {
		t.Fatalf("short mission changed: %q", got)
	}
	long := strings.Repeat("word ", 120)
	sa := StandingAgent{Mission: long}
	if got := missionForList(sa); len(strings.Fields(got)) != missionListWords || !strings.HasSuffix(got, "…") {
		t.Fatalf("long mission not cut to %d words: %q", missionListWords, got)
	}
	if !missionNeedsSummary(sa) {
		t.Fatal("a long mission without a summary does not ask for one")
	}
	sa.MissionSummary, sa.MissionSummaryOf = "Posts a thought.", missionPrint(long)
	if got := missionForList(sa); got != "Posts a thought." || missionNeedsSummary(sa) {
		t.Fatalf("summary not used: %q", got)
	}
	// An edited mission does not wear the old one's summary.
	sa.Mission = long + "and more"
	if got := missionForList(sa); got == "Posts a thought." || !missionNeedsSummary(sa) {
		t.Fatalf("a stale summary was shown for a changed mission: %q", got)
	}
}

// The summary is written off the request onto the record, held to the limit,
// and not written over a mission that changed while the model worked.
func TestSummarizeMissionLater(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	ask := missionSummaryAsk
	t.Cleanup(func() { missionSummaryAsk = ask })
	missionSummaryAsk = func(ctx context.Context, mission string) (string, error) {
		return "\"" + strings.Repeat("short ", 60) + "\"", nil
	}
	long := strings.Repeat("word ", 120)
	sa := StandingAgent{Owner: "u", Name: "poster", AgentID: "a", Mission: long, IntervalSeconds: 3600}
	SaveStandingAgent(RootDB, sa)
	summarizeMissionLater(sa)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := GetStandingAgent(RootDB, "u", "poster")
		if got.MissionSummary != "" {
			if n := len(strings.Fields(got.MissionSummary)); n != missionListWords {
				t.Fatalf("summary not held to %d words: %d", missionListWords, n)
			}
			if got.IntervalSeconds != 3600 {
				t.Fatalf("summary write lost the schedule: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no summary was written")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
