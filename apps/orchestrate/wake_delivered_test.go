package orchestrate

import (
	"os"
	"strings"
	"testing"
)

// fireSource is the source of one recurring fire: fireOrchestrateUpdate and
// the fireRun phases it runs, so a pin on the fire cannot pass against some
// other function in the file.
func fireSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("scheduled_updates.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	var out []string
	for _, head := range []string{"func fireOrchestrateUpdate(", "func (fr *fireRun) "} {
		for rest := s; ; {
			i := strings.Index(rest, head)
			if i < 0 {
				break
			}
			body := rest[i:]
			if j := strings.Index(body, "\n}\n"); j > 0 {
				body = body[:j]
			}
			out = append(out, body)
			rest = rest[i+len(body):]
		}
	}
	if len(out) == 0 || !strings.HasPrefix(out[0], "func fireOrchestrateUpdate(") {
		t.Fatal("fireOrchestrateUpdate is gone")
	}
	return strings.Join(out, "\n")
}

// A scheduled fire or task wake tells the turn judge what goes out with its
// reply. Without it every wake read as delivering nothing, and a caption
// presenting the picture it carried was convicted and rewritten. No harness
// runs a wake turn end to end, so this pins the wiring.
func TestAWakeTurnCountsWhatItDelivers(t *testing.T) {
	body := fireSource(t)
	if !strings.Contains(body, "DeliveredCount: func() int { return len(fr.subSess.Images) + len(fr.subSess.Videos) + len(fr.subSess.Files) }") {
		t.Error("the wake turn's loop config does not count what it delivers")
	}
}

// A wake turn registers as its thread's background run and wakes the
// thread's card poll as it starts and ends, so an open page shows it working.
func TestAWakeTurnShowsOnItsThread(t *testing.T) {
	body := fireSource(t)
	for _, want := range []string{"fr.app.runsRegistry().WorkFor(fr.liveRun, p.SessionID, label)", "noteSessionChange(p.AgentID, p.SessionID)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the wake turn does not %s", want)
		}
	}
}

// Every loop the claim judge reads also tells it what the conversation has
// running and what a wake delivers. The channel run went without, and its
// judge convicted "it'll land here" on the turn carrying the finished song.
func TestEveryJudgedLoopKnowsItsBackgroundWork(t *testing.T) {
	for _, f := range []string{"runner.go", "runner_worker.go", "agent_dispatch.go", "scheduled_updates.go", "agents_grouped_tool.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		judged := strings.Count(s, "TurnClaimJudge:") + strings.Count(s, "loopCfg.TurnClaimJudge =")
		told := strings.Count(s, "BackgroundWork:") + strings.Count(s, "loopCfg.BackgroundWork =")
		if judged == 0 || told != judged {
			t.Errorf("%s: %d judged loop(s), %d told of background work", f, judged, told)
		}
	}
	// And a delegation into a busy thread waits its turn there.
	src, err := os.ReadFile("agents_grouped_tool.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	hold := strings.Index(s, "release, werr := holdDispatchThread(runCtx, thread)")
	run := strings.Index(s, "SubSessionID: thread,")
	if hold < 0 || run < 0 || hold > run {
		t.Error("the detached delegation no longer waits for its thread before running in it")
	}
	// Both delegation paths, inline and detached, key the thread the same way,
	// so a follow-up re-threads whichever path the first one took.
	for _, want := range []string{"ar.subSessID = t.dispatchThreadID(ar.target.ID)", "thread := t.dispatchThreadID(target.ID)"} {
		if !strings.Contains(s, want) {
			t.Errorf("a delegation path no longer keys its thread on its conversation: %s", want)
		}
	}
	// The detached delegation counts as under way from the moment it starts
	// waiting, so a repeat asked meanwhile is refused rather than queued.
	begin := strings.Index(s, "defer beginDelegation(thread, request)()")
	if begin < 0 || begin > hold {
		t.Error("the detached delegation is not under way while it waits for its thread")
	}
	// A hand-off to Builder is deliberate, and queues instead of being refused.
	hb, err := os.ReadFile("hand_to_builder.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hb), `"queue": true,`) {
		t.Error("a second hand-off to Builder is refused as a repeat")
	}
}
