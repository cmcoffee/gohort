package servitor

import (
	"os"
	"strings"
	"testing"
)

// Every loop Servitor runs for a person goes through orchestrate's guardrails
// (servitorGuard -> AppLoopGuard.Apply): Servitor runs its own loops, so a
// loop config built bare is one no Always rule, agent rule or scan sees. The
// command-line sysprobe is the exception: it has no web user to guard for.
func TestEveryServitorLoopIsGuarded(t *testing.T) {
	for _, f := range []string{"probe_session.go", "map_session.go", "workspace_session.go", "repo_audit.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		for i := strings.Index(s, "AgentLoopConfig{"); i >= 0; {
			if !strings.HasSuffix(s[:i], ".Apply(") {
				t.Errorf("%s:%d builds a loop config that is not guarded", f, strings.Count(s[:i], "\n")+1)
			}
			next := strings.Index(s[i+1:], "AgentLoopConfig{")
			if next < 0 {
				break
			}
			i += 1 + next
		}
	}
}
