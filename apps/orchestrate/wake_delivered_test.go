package orchestrate

import (
	"os"
	"strings"
	"testing"
)

// A scheduled fire or task wake tells the turn judge what goes out with its
// reply. Without it every wake read as delivering nothing, and a caption
// presenting the picture it carried was convicted and rewritten. No harness
// runs a wake turn end to end, so this pins the wiring.
func TestAWakeTurnCountsWhatItDelivers(t *testing.T) {
	src, err := os.ReadFile("scheduled_updates.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "func fireOrchestrateUpdate(")
	if i < 0 {
		t.Fatal("fireOrchestrateUpdate is gone")
	}
	body := s[i:]
	if j := strings.Index(body, "\n}\n"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "DeliveredCount: func() int { return len(subSess.Images) + len(subSess.Videos) + len(subSess.Files) }") {
		t.Error("the wake turn's loop config does not count what it delivers")
	}
}
