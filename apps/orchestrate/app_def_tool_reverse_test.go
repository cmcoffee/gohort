package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// get's full view shortens a long section field to a placeholder (one app's
// get came back at 72 KB, ~20k tokens riding on every later round), returns
// one section in full with section_id, and a write carrying a placeholder is
// refused rather than overwriting the content it stands for.
func TestAppGetShortensLongSectionsAndGuardsThem(t *testing.T) {
	pinRootDB(t)
	game := "<!DOCTYPE html><html><body><canvas id=g></canvas><script>" + strings.Repeat("function step(){ x += 1; }\n", 400) + "</script></body></html>"
	secs, _ := json.Marshal([]any{
		map[string]any{"kind": "html", "id": "html-game", "html": game},
		map[string]any{"kind": "empty", "id": "empty-start", "title": "Press start"},
	})
	SaveAppSpec(AppSpec{Owner: "u", Slug: "game", Name: "Game", Sections: secs})
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "seed-builder"}}

	out, err := turn.appDefGet(map[string]any{"id": "game"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "x += 1") {
		t.Error("the full view still carries the whole canvas")
	}
	if len(out) > 4*appSectionFieldMax {
		t.Errorf("the full view is %d bytes", len(out))
	}
	for _, want := range []string{appOmittedMarker, `section_id=\"html-game\"`, "Shortened in this view: html-game", "Press start"} {
		if !strings.Contains(out, want) {
			t.Errorf("the full view should carry %q:\n%.600s", want, out)
		}
	}

	one, err := turn.appDefGet(map[string]any{"id": "game", "section_id": "html-game"})
	if err != nil || !strings.Contains(one, "x += 1") || strings.Contains(one, appOmittedMarker) {
		t.Errorf("section_id should return the section in full: %v %.200s", err, one)
	}

	// Pasting the shortened view back is refused, before anything is written.
	var view struct {
		Sections []map[string]any `json:"sections"`
	}
	json.Unmarshal([]byte(out), &view)
	for _, args := range []map[string]any{
		{"action": "update", "id": "game", "sections": view.Sections},
		{"action": "update_section", "id": "game", "section_id": "html-game", "section_def": view.Sections[0]},
	} {
		got, err := turn.appDefToolDef().Handler(context.Background(), args)
		if err != nil || !strings.Contains(got, "NOT DONE") || !strings.Contains(got, "placeholder") {
			t.Errorf("%s with a placeholder was not refused: %q %v", args["action"], got, err)
		}
	}
	if spec, _ := LoadAppSpec("u", "game"); !strings.Contains(string(spec.Sections), "x += 1") {
		t.Error("the canvas was overwritten")
	}
	if msg := appOmittedRefusal("get", map[string]any{"x": appOmittedMarker}); msg != "" {
		t.Error("reading is never refused")
	}
}

// Builder, asked why an app is broken, reads how its scripts went when they
// ran for real: get lists the newest runs without their lines, and get with
// script=<name> lists that script's runs with what each logged.
func TestAppGetShowsRecentRuns(t *testing.T) {
	pinRootDB(t)
	SaveAppSpec(AppSpec{Owner: "u", Slug: "wx", Name: "Weather",
		DataSources: []AppDataSource{{Name: "forecast", Language: "python", Script: "print('{}')"}},
		Actions:     []AppAction{{Name: "refresh", Language: "python", Script: "print('{}')"}}})
	turn := &chatTurn{user: "u", agent: AgentRecord{ID: "seed-builder"}}

	quiet, err := turn.appDefGet(map[string]any{"id": "wx"})
	if err != nil {
		t.Fatal(err)
	}
	var none map[string]any
	json.Unmarshal([]byte(quiet), &none)
	if _, ok := none["recent_runs"]; ok {
		t.Error("an app that never ran should show no runs")
	}

	run := func(kind, name string, fail error, lines ...string) {
		j := appscript.Job{Owner: "u", Slug: "wx", Kind: kind, Name: name, Caller: "u", Interactive: true}
		trace := appscript.Track(&j)
		for _, l := range lines {
			j.OnLog("info", l)
		}
		trace.Done(fail)
	}
	run("data", "forecast", errors.New("KeyError: 'temp'"), "got 3 cities")
	run("action", "refresh", nil, "refreshed")

	out, err := turn.appDefGet(map[string]any{"id": "wx"})
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		Runs []appscript.AppRun `json:"recent_runs"`
		Note string             `json:"runs_note"`
	}
	json.Unmarshal([]byte(out), &full)
	if len(full.Runs) != 2 || full.Runs[0].Name != "refresh" || full.Runs[1].Error != "KeyError: 'temp'" || full.Note == "" {
		t.Fatalf("get should list both runs, newest first, with how each ended: %+v", full)
	}
	for _, r := range full.Runs {
		if len(r.Log) != 0 {
			t.Errorf("the whole-app view leaves the lines to the script's view: %+v", r)
		}
	}

	one, err := turn.appDefGet(map[string]any{"id": "wx", "script": "forecast"})
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Runs []appscript.AppRun `json:"recent_runs"`
	}
	json.Unmarshal([]byte(one), &script)
	if len(script.Runs) != 1 || script.Runs[0].Name != "forecast" || len(script.Runs[0].Log) != 1 || script.Runs[0].Log[0] != "got 3 cities" {
		t.Errorf("a script's view lists its own runs with their lines: %+v", script.Runs)
	}
}
