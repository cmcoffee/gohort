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
