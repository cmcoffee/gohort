package servitor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	registeredStage = regexp.MustCompile(`RegisterRouteStage\(RouteStage\{[^}]*?Key:\s*"([^"]+)"`)
	usedRouteKey    = regexp.MustCompile(`RouteKey:\s*"([^"]+)"`)
)

// TestEveryRegisteredStageActuallySelectsATier — a routing row whose dropdown
// decides nothing.
//
// app.servitor.orchestrator was registered with a tier selector and a default
// of "worker (thinking)", and every loop in the package passed
// RouteKey: "app.servitor" instead. The stage was read only by
// RouteThinkBudget, so its BUDGET applied and its TIER was silently discarded:
// an operator could set "Servitor: Orchestrator" to Lead, save it, see it
// persist, and watch the investigator keep running on the worker with nothing
// anywhere reporting a conflict.
//
// Nothing local could catch that. The registration is in one file, the loops in
// another, and each is individually correct — only a rule comparing the two
// sees the gap.
func TestEveryRegisteredStageActuallySelectsATier(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]string{} // key -> file it was registered in
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		for _, m := range registeredStage.FindAllStringSubmatch(body, -1) {
			registered[m[1]] = f
		}
		for _, m := range usedRouteKey.FindAllStringSubmatch(body, -1) {
			used[m[1]] = true
		}
	}
	if len(registered) == 0 {
		t.Fatal("found no registered route stages — the sweep is no longer looking where they live")
	}
	for key, file := range registered {
		if !used[key] {
			t.Errorf("%s registers route stage %q, but no loop in this package passes it as a RouteKey. "+
				"Its tier dropdown in Admin → LLM Routing decides nothing: an operator can set it to Lead, "+
				"watch it save, and get the worker anyway. Either wire it or stop registering it.",
				file, key)
		}
	}
}

// TestRouteKeysAreRegistered — the mirror. A loop naming a stage nobody
// registered gets whatever routeEffectiveVal falls back to, and the operator
// has no row to change it with.
func TestRouteKeysAreRegistered(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	registered := map[string]bool{}
	type useSite struct{ key, file string }
	var uses []useSite
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		for _, m := range registeredStage.FindAllStringSubmatch(body, -1) {
			registered[m[1]] = true
		}
		for _, m := range usedRouteKey.FindAllStringSubmatch(body, -1) {
			uses = append(uses, useSite{m[1], f})
		}
	}
	for _, u := range uses {
		if !registered[u.key] {
			t.Errorf("%s routes through %q, which this package never registers — "+
				"there is no row for an operator to change it with", u.file, u.key)
		}
	}
}

// TestTheInvestigatorUsesTheOrchestratorStage — pinned specifically, because
// this is the one that was wrong and the fix is a single string that a future
// copy-paste from a neighbouring loop would quietly undo.
func TestTheInvestigatorUsesTheOrchestratorStage(t *testing.T) {
	data, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	i := strings.Index(body, "buildInvestigatorSystemPrompt(appliance, resolvedTools)")
	if i < 0 {
		t.Fatal("the investigator loop config has moved")
	}
	window := body[i:min(i+1400, len(body))]
	if !strings.Contains(window, `RouteKey:        "app.servitor.orchestrator"`) {
		t.Errorf("the investigator no longer routes through its own stage — "+
			"its tier would come from the worker row while its thinking budget comes "+
			"from the orchestrator row:\n%s", window)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
