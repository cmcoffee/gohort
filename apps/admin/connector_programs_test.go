package admin

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/flows"
)

// The programs jobs run are connectors read from the jobs: each says what it
// does, now or standing by, and whether it has the network; its command
// lines are its endpoints. A program not installed is listed only while a
// job it would do has no other way to run.
func TestSystemHooksProgramsAreConnectors(t *testing.T) {
	jobs := []flows.Job{
		{ID: "read.a", Group: flows.GroupReading, Title: "A files", Takes: []string{"application/x-a"}, Becomes: flows.KindText,
			Native: []flows.Flow{{Steps: []string{"fakeconv -t plain {in} -o {out}"}, Install: "fakeconv"}, {Steps: []string{"altconv {in}"}, Install: "altconv"}}},
		{ID: "export.y", Group: flows.GroupWriting, Title: "Y files", Takes: []string{"text/markdown"}, Gives: "y",
			Native: []flows.Flow{{Steps: []string{"otherconv {in} {out}"}, Install: "otherconv"}, {Steps: []string{"fakeconv -f markdown {in} -o {out}"}, Install: "fakeconv"}}},
		{ID: "media.grab", Group: flows.GroupMedia, Title: "Video grabs", Becomes: flows.KindVideo, Network: true, Builtin: true, Uses: []string{"grabber"},
			Params: []flows.Param{{Name: "url"}}},
		{ID: "read.z", Group: flows.GroupReading, Title: "Z files", Takes: []string{"application/x-z"}, Becomes: flows.KindText,
			Native: []flows.Flow{{Steps: []string{"missingtool {in}"}, Install: "missingtool-utils"}}},
	}
	have := map[string]bool{"fakeconv": true, "otherconv": true, "grabber": true}
	got := map[string]programConnector{}
	var names []string
	for _, pc := range programConnectors(jobs, func(p string) bool { return have[p] }, false) {
		got[pc.program] = pc
		names = append(names, pc.program)
	}
	if strings.Join(names, ",") != "fakeconv,grabber,missingtool,otherconv" {
		t.Errorf("listed: %v (altconv is an alternative to a job that runs, so not listed)", names)
	}
	fc := got["fakeconv"]
	if fc.name != "program:fakeconv" || fc.wrapper != "Reads A files; Writes Y files ⇐ fakeconv on this machine (no network)" {
		t.Errorf("fakeconv: %q %q", fc.name, fc.wrapper)
	}
	if !strings.Contains(fc.status, "Does: Reads A files.") || !strings.Contains(fc.status, "Standing by for: Writes Y files") || !strings.HasSuffix(fc.status, "Set up under Admin > Capabilities > System Hooks.") {
		t.Errorf("fakeconv status: %s", fc.status)
	}
	if w := got["grabber"].wrapper; w != "Video grabs ⇐ grabber on this machine (network for Video grabs)" {
		t.Errorf("grabber: %s", w)
	}
	if s := got["missingtool"].status; !strings.HasPrefix(s, "Not installed. Install missingtool-utils for: Reads Z files.") {
		t.Errorf("missingtool: %s", s)
	}

	var eps []string
	for _, e := range programEndpoints(fc) {
		eps = append(eps, strings.Join([]string{e.Name, e.Address, e.UsedBy, e.Sends, e.Reads}, " | "))
	}
	if want := "read.a | fakeconv -t plain {in} -o {out} | Reads A files | {in} ← the file (" + flows.FormatLabel("application/x-a") + ") | {out} → text\n" +
		"export.y | fakeconv -f markdown {in} -o {out} | Writes Y files (standing by) | {in} ← the document, as " + flows.FormatLabel("text/markdown") + " | {out} → a .y file"; strings.Join(eps, "\n") != want {
		t.Errorf("fakeconv endpoints:\n%s\nwant\n%s", strings.Join(eps, "\n"), want)
	}
	if e := programEndpoints(got["grabber"])[0]; e.Address != "run by gohort, driving grabber" || e.Sends != "{url}" || e.Reads != "{out} → "+flows.KindVideo.Label() {
		t.Errorf("grabber endpoint: %+v", e)
	}

	refused := programConnectors(jobs, func(p string) bool { return have[p] }, true)
	if !strings.Contains(refused[0].status, "Nothing runs: the sandbox is not available") {
		t.Errorf("with no sandbox: %s", refused[0].status)
	}
}
