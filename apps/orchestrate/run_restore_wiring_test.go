package orchestrate

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Pipeline and machine runs are restorable, reachable from the activity
// ribbon, and stoppable and rejoinable from their own pages. The wiring is
// spread over several files, so pin it at the source.
func TestRunSurfacesAreRestorableAndReachable(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	runs, machine, page, routes, exposed, orch := read("pipeline_runs.go"), read("machine_run.go"),
		read("pipeline_page.go"), read("pipelines_http.go"), read("exposed.go"), read("orchestrate.go")

	// Each restorable surface names its kind and a page link ending in {id},
	// which the completion notice links to.
	for name, src := range map[string]string{"pipeline": runs, "machine": machine} {
		if !regexp.MustCompile(`Kind:\s+(pipelineRunKind|machineRunKind)|s\.Kind = pipelineRunKind`).MatchString(src) {
			t.Errorf("%s runs must declare their restore kind", name)
		}
		if !regexp.MustCompile(`"&session=\{id\}"`).MatchString(src) {
			t.Errorf("%s runs need a page link ending in {id} for the ribbon and the notice", name)
		}
	}
	if !strings.Contains(runs, "T.RegisterRunRestore(pipelineRunKind,") || !strings.Contains(runs, "T.RegisterRunRestore(machineRunKind,") {
		t.Error("both kinds must register a restorer")
	}
	if !strings.Contains(orch, "T.registerRunRestores()") {
		t.Error("the restorers must be registered with the routes, before the queue is restored")
	}
	if !strings.Contains(exposed, "s.Live, s.Kind, s.RestoreKey = live, AppPipelineRunKind, key") {
		t.Error("an app's pipeline runs must be restorable under AppPipelineRunKind")
	}

	// A run outlives its tab, so its page must be able to stop and rejoin it.
	if !strings.Contains(page, `CancelURL:    "api/pipelines/" + url_(def.ID) + "/cancel"`) ||
		!strings.Contains(page, `ReconnectURL: "api/pipelines/" + url_(def.ID) + "/reconnect/{id}"`) {
		t.Error("the pipeline page must declare cancel and reconnect")
	}
	if !regexp.MustCompile(`case "stream", "sessions", "cancel"[^:]*:`).MatchString(routes) || !strings.Contains(routes, `strings.CutPrefix(action, "reconnect/")`) {
		t.Error("the pipeline routes must reach cancel and reconnect")
	}
	if !strings.Contains(machine, `CancelURL:        base + "cancel"`) || !strings.Contains(machine, `ReconnectURL:     base + "reconnect/{id}"`) {
		t.Error("the machine page must declare cancel and reconnect")
	}
}
