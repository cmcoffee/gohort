// A background task in the live surfaces. It reaches them already — the runs
// registry is kind-agnostic and a task is registered like any other run — but a
// detached call is ONE tool call waiting on a backend, so it never reports a
// round or a tool and had nothing on it that ever changed.
package orchestrate

import (
	"strings"
	"testing"
	"time"
)

func TestAProgresslessRunStillShowsItIsMoving(t *testing.T) {
	// What the ribbon renders for a run, mirrored from the LiveProvider: kind,
	// then whatever progress detail exists, then elapsed when there is none.
	render := func(kind string, round int, lastTool string, started time.Time) string {
		status := kind
		if round > 0 {
			status += " · round " + shortElapsed(0)
		}
		if lastTool != "" {
			status += " · " + lastTool
		}
		if round == 0 && lastTool == "" {
			status += " · " + shortElapsed(time.Since(started))
		}
		return status
	}

	task := render("task", 0, "", time.Now().Add(-3*time.Minute))
	if !strings.HasPrefix(task, "task · ") {
		t.Fatalf("a task should carry elapsed: %q", task)
	}
	if task == "task" {
		t.Error("a fifteen-minute render must not sit in the ribbon as a motionless word")
	}
	// A chat turn already has motion — rounds and tool names — so elapsed would
	// just be noise on top of it.
	chat := render("chat", 2, "web_search", time.Now().Add(-time.Minute))
	if strings.Count(chat, "·") != 2 {
		t.Errorf("a run with real progress detail should not also get elapsed: %q", chat)
	}
}

func TestShortElapsedReadsLikeAPerson(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{3 * time.Minute, "3m00s"},
	} {
		if got := shortElapsed(c.d); got != c.want {
			t.Errorf("shortElapsed(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}
