package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

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
