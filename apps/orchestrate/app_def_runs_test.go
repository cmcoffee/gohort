package orchestrate

import (
	"encoding/json"
	"errors"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

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
