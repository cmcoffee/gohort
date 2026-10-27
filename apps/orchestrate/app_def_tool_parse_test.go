package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An action's and a data source's declared inputs are kept from app_def, so
// the app's agent is asked for exactly those.
func TestAppScriptInputsAreKept(t *testing.T) {
	acts, _ := appActionDefs([]any{map[string]any{"name": "fetch", "script": "print('{}')",
		"inputs": map[string]any{"case": "The case id", " ": "blank"}}})
	if len(acts) != 1 || acts[0].Inputs["case"] != "The case id" || len(acts[0].Inputs) != 1 {
		t.Errorf("action inputs: %+v", acts)
	}
	ds, _ := appDataSources([]any{map[string]any{"name": "cases", "script": "print('[]')",
		"inputs": map[string]any{"case": "Which case"}}})
	if len(ds) != 1 || ds[0].Inputs["case"] != "Which case" {
		t.Errorf("data source inputs: %+v", ds)
	}
	if acts, _ := appActionDefs([]any{map[string]any{"name": "x", "script": "y"}}); acts[0].Inputs != nil {
		t.Error("an action with no inputs declared got some")
	}
}

// run_agent and run_pipeline are capabilities a script may declare (the hook
// serves both, and the author is told to use them); they used to be dropped at
// save.
func TestAScriptMayDeclareRunAgentAndRunPipeline(t *testing.T) {
	keep, notes := appScriptCaps([]any{"run_agent", "run_pipeline", "fetch", "nonsense"}, "action x")
	if len(keep) != 3 || keep[0] != "run_agent" || keep[1] != "run_pipeline" {
		t.Errorf("kept %v", keep)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "nonsense") {
		t.Errorf("notes %v", notes)
	}
}

// Builder is told when a schedule's setting cannot say when the app runs.
func TestAScheduleSettingIsCheckedAgainstTheAppsSettings(t *testing.T) {
	act := func(setting string) AppAction {
		return AppAction{Name: "refresh", Schedule: &AppSchedule{Setting: setting}}
	}
	spec := AppSpec{
		Settings: []AppSetting{
			{Name: "refresh_time", Default: "06:00"},
			{Name: "mine", Default: "06:00", Scope: "user"},
			{Name: "blank"},
		},
	}
	for _, tc := range []struct {
		setting, want string
	}{
		{"refresh_time", ""},
		{"missing", "does not declare"},
		{"mine", "per user"},
		{"blank", "will not run until the owner sets it"},
	} {
		spec.Actions = []AppAction{act(tc.setting)}
		notes := strings.Join(appScheduleSettingNotes(spec), "\n")
		if tc.want == "" && notes != "" || tc.want != "" && !strings.Contains(notes, tc.want) {
			t.Errorf("%s: notes = %q, want %q", tc.setting, notes, tc.want)
		}
	}
	// A schedule naming only a setting is a schedule.
	if sch, _ := appSchedule(map[string]any{"setting": "refresh_time"}, "refresh"); sch == nil || sch.Setting != "refresh_time" {
		t.Errorf("parsed = %+v", sch)
	}
}

// TestAppActionDefs_Schedule covers the self-update cadence parsing on an
// action's optional `schedule` object: interval floor, cron/interval clash,
// max_idle_days passthrough, and a schedule that names no cadence (no-op).
func TestAppActionDefs_Schedule(t *testing.T) {
	acts, notes := appActionDefs([]any{
		// 0: below the floor → floored, with a note.
		map[string]any{"name": "refresh", "script": "print('{}')",
			"schedule": map[string]any{"interval_seconds": float64(60), "max_idle_days": float64(7)}},
		// 1: cron + interval → cron wins, interval dropped, with a note.
		map[string]any{"name": "daily", "script": "print('{}')",
			"schedule": map[string]any{"cron": "MON 09:00", "interval_seconds": float64(3600)}},
		// 2: schedule object with no cadence → no schedule, with a note.
		map[string]any{"name": "empty_sched", "script": "print('{}')",
			"schedule": map[string]any{"max_idle_days": float64(3)}},
		// 3: no schedule at all → manual button, nil schedule.
		map[string]any{"name": "manual", "script": "print('{}')"},
	})

	if len(acts) != 4 {
		t.Fatalf("want 4 actions, got %d", len(acts))
	}

	// 0: floored to the minimum, idle days preserved.
	if s := acts[0].Schedule; s == nil {
		t.Fatal("refresh: expected a schedule")
	} else if s.IntervalSeconds != MinAppScheduleSeconds {
		t.Errorf("refresh: interval = %d, want floored to %d", s.IntervalSeconds, MinAppScheduleSeconds)
	} else if s.MaxIdleDays != 7 {
		t.Errorf("refresh: max_idle_days = %d, want 7", s.MaxIdleDays)
	}

	// 1: cron wins, interval cleared.
	if s := acts[1].Schedule; s == nil {
		t.Fatal("daily: expected a schedule")
	} else if s.Cron != "MON 09:00" || s.IntervalSeconds != 0 {
		t.Errorf("daily: cron=%q interval=%d, want cron kept + interval cleared", s.Cron, s.IntervalSeconds)
	}

	// 2: no cadence → nil schedule.
	if acts[2].Schedule != nil {
		t.Errorf("empty_sched: want nil schedule, got %+v", acts[2].Schedule)
	}

	// 3: manual button.
	if acts[3].Schedule != nil {
		t.Errorf("manual: want nil schedule, got %+v", acts[3].Schedule)
	}

	// Notes should flag the floor, the clash, and the no-op schedule.
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"below the", "using cron", "will NOT self-update"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q; got:\n%s", want, joined)
		}
	}
}

// TestAppSettingsParse covers the settings declaration parser: names become
// env-var-safe, defaults are typed per kind, a bad type or scope is corrected
// with a note rather than dropped, a choice's default must be an option, and
// a duplicate or nameless entry is reported.
func TestAppSettingsParse(t *testing.T) {
	raw := []any{
		map[string]any{"name": "Refresh Minutes", "type": "number", "default": 5, "min": 1, "max": 60, "label": "Refresh every"},
		map[string]any{"name": "alerts", "type": "toggle", "default": "yes"},
		map[string]any{"name": "units", "type": "choice", "options": []any{"metric", "imperial"}, "default": "furlongs", "scope": "user"},
		map[string]any{"name": "city", "scope": "team"},
		map[string]any{"name": "mode", "type": "dropdown"},
		map[string]any{"name": "flavor", "type": "choice"},
		map[string]any{"name": "count", "type": "number", "default": "lots"},
		map[string]any{"name": "alerts"},
		map[string]any{"type": "string"},
		"not an object",
	}
	out, notes := appSettings(raw)
	if len(out) != 7 {
		t.Fatalf("got %d settings: %+v", len(out), out)
	}
	byName := map[string]int{}
	for i, s := range out {
		byName[s.Name] = i
	}
	if i, ok := byName["refresh_minutes"]; !ok || out[i].Type != "number" || out[i].Default != "5" || out[i].Min != 1 || out[i].Max != 60 || out[i].Label != "Refresh every" {
		t.Fatalf("refresh_minutes = %+v", out)
	}
	if out[byName["alerts"]].Default != "true" {
		t.Fatalf("a toggle default is normalized to true/false: %+v", out[byName["alerts"]])
	}
	if u := out[byName["units"]]; u.Default != "metric" || u.Scope != "user" || len(u.Options) != 2 {
		t.Fatalf("units = %+v", u)
	}
	if c := out[byName["city"]]; c.Scope != "" || c.Type != "string" {
		t.Fatalf("an unknown scope falls back to owner: %+v", c)
	}
	if out[byName["mode"]].Type != "string" {
		t.Fatal("an unknown type falls back to string")
	}
	if out[byName["flavor"]].Type != "string" {
		t.Fatal("a choice with no options renders as text")
	}
	if out[byName["count"]].Default != "" {
		t.Fatal("a non-numeric default on a number is cleared")
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{
		`"Refresh Minutes" is registered as "refresh_minutes"`,
		`"units" defaults to "furlongs"`,
		`"city" has unknown scope "team"`,
		`"mode" has unknown type "dropdown"`,
		`"flavor" is a choice with no options`,
		`"count" is a number but defaults to "lots"`,
		`"alerts" declared twice`,
		`entry 9 IGNORED: needs a name`,
		`entry 10 IGNORED: not an object`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes lack %q:\n%s", want, joined)
		}
	}
	if out, notes := appSettings("nope"); out != nil || notes != nil {
		t.Fatal("a non-array declares nothing")
	}
}
