package customapps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// paperSpec refreshes each morning at the time its owner sets.
func paperSpec() AppSpec {
	return AppSpec{Slug: "paper", Name: "Paper", Owner: "alice",
		Settings: []AppSetting{{Name: "refresh_time", Label: "Refresh at", Type: "string", Default: "06:00"}},
		Actions: []AppAction{{Name: "refresh", Script: "print('{}')",
			Schedule: &AppSchedule{Cron: "daily 05:00", Setting: "refresh_time"}}},
	}
}

// When an app refreshes can be its owner's setting: the trigger runs on the
// setting's value, and a save on the Settings page moves it with no Builder
// turn. A value that does not read as a time is refused, not saved and
// quietly ignored.
func TestARefreshTimeSettingMovesTheSchedule(t *testing.T) {
	T := sharingTestApp(t)
	spec := paperSpec()
	name := schedTriggerName(spec.Slug, "refresh")

	T.reconcileAppSchedules(spec)
	tr, ok := GetScheduledTrigger(RootDB, "alice", name)
	if !ok || tr.Cron != "daily 06:00" {
		t.Fatalf("the default should set the time: %+v (found %v)", tr, ok)
	}

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleSettingsValues(w, httptest.NewRequest(http.MethodPost, "/apps/paper/_settings/values", strings.NewReader(body)), spec, "alice", true)
		return w
	}
	if w := post(`{"refresh_time": "07:30"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if tr, _ := GetScheduledTrigger(RootDB, "alice", name); tr.Cron != "daily 07:30" {
		t.Errorf("a saved time should move the trigger, cron = %q", tr.Cron)
	}
	if w := post(`{"refresh_time": "breakfast"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Refresh at") {
		t.Errorf("an unreadable time should be refused by its label: %d %s", w.Code, w.Body.String())
	}
	if tr, _ := GetScheduledTrigger(RootDB, "alice", name); tr.Cron != "daily 07:30" {
		t.Errorf("a refused save must leave the trigger, cron = %q", tr.Cron)
	}
	// Reset brings back the default time.
	T.handleSettingsReset(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/apps/paper/_settings/reset", nil), spec, "alice", true)
	if tr, _ := GetScheduledTrigger(RootDB, "alice", name); tr.Cron != "daily 06:00" {
		t.Errorf("reset should restore the default time, cron = %q", tr.Cron)
	}
	// A number of minutes is a cadence.
	post(`{"refresh_time": "45"}`)
	if tr, _ := GetScheduledTrigger(RootDB, "alice", name); tr.IntervalSeconds != 2700 || tr.Cron != "" {
		t.Errorf("minutes should set an interval: %+v", tr)
	}
}

// A setting with nothing usable falls back to the schedule's own cadence.
func TestAnUnreadableRefreshSettingFallsBack(t *testing.T) {
	T := sharingTestApp(t)
	spec := paperSpec()
	spec.Settings[0].Default = ""
	if secs, cron := T.scheduleCadence(spec, spec.Actions[0]); secs != 0 || cron != "daily 05:00" {
		t.Errorf("fallback = %d %q", secs, cron)
	}
}
