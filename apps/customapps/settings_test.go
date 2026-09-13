package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// settingsSpec is one app with every setting type and both scopes.
func settingsSpec() AppSpec {
	return AppSpec{Slug: "wx", Name: "Weather", Owner: "alice", Settings: []AppSetting{
		{Name: "refresh_minutes", Label: "Refresh every", Type: "number", Default: "5", Min: 1, Max: 60},
		{Name: "alerts", Type: "toggle", Default: "false"},
		{Name: "city", Type: "string", Default: "Santa Cruz, CA", Scope: "user"},
		{Name: "units", Type: "choice", Default: "metric", Options: []string{"metric", "imperial"}, Scope: "user"},
		{Name: "", Type: "string"}, // nameless: ignored
	}}
}

// TestVisibleSettings: the owner may set every setting; anyone else only the
// per-user ones; a nameless declaration is dropped.
func TestVisibleSettings(t *testing.T) {
	spec := settingsSpec()
	if got := visibleSettings(spec, true); len(got) != 4 {
		t.Fatalf("owner sees %d, want 4", len(got))
	}
	got := visibleSettings(spec, false)
	if len(got) != 2 || got[0].Name != "city" || got[1].Name != "units" {
		t.Fatalf("recipient sees %v", got)
	}
}

// TestSettingsValuesRoundTrip covers the values endpoint: defaults come back
// typed for the controls, a save stores strings and refuses what the
// declaration rules out, a recipient can neither see nor write the owner's
// settings, and reset clears only what that person may set.
func TestSettingsValuesRoundTrip(t *testing.T) {
	T := sharingTestApp(t)
	spec := settingsSpec()

	get := func(uid string, owner bool) map[string]any {
		w := httptest.NewRecorder()
		T.handleSettingsValues(w, httptest.NewRequest(http.MethodGet, "/apps/wx/_settings/values", nil), spec, uid, owner)
		if w.Code != http.StatusOK {
			t.Fatalf("GET as %s: %d %s", uid, w.Code, w.Body.String())
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	post := func(uid string, owner bool, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleSettingsValues(w, httptest.NewRequest(http.MethodPost, "/apps/wx/_settings/values", strings.NewReader(body)), spec, uid, owner)
		return w
	}

	// Defaults, typed.
	d := get("alice", true)
	if d["refresh_minutes"] != 5.0 || d["alerts"] != false || d["city"] != "Santa Cruz, CA" || d["units"] != "metric" {
		t.Fatalf("defaults = %v", d)
	}

	// A save stores strings.
	if w := post("alice", true, `{"refresh_minutes": 15, "alerts": true, "units": "imperial"}`); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stored := loadSettingValues(T.settingsBase(spec, "alice"), "wx")
	if stored["refresh_minutes"] != "15" || stored["alerts"] != "true" || stored["units"] != "imperial" {
		t.Fatalf("stored = %v", stored)
	}
	if d = get("alice", true); d["refresh_minutes"] != 15.0 || d["alerts"] != true {
		t.Fatalf("after save = %v", d)
	}

	// Out of range and off the list are refused, and nothing changes.
	if w := post("alice", true, `{"refresh_minutes": 999}`); w.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range must be refused: %d", w.Code)
	}
	if w := post("alice", true, `{"units": "furlongs"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("off-list choice must be refused: %d", w.Code)
	}
	if w := post("alice", true, `{"refresh_minutes": "abc"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("non-number must be refused: %d", w.Code)
	}
	if stored = loadSettingValues(T.settingsBase(spec, "alice"), "wx"); stored["refresh_minutes"] != "15" || stored["units"] != "imperial" {
		t.Fatalf("a refused save must change nothing: %v", stored)
	}

	// A recipient sees only their own settings and cannot write the owner's.
	b := get("bob", false)
	if _, ok := b["refresh_minutes"]; ok || b["city"] != "Santa Cruz, CA" {
		t.Fatalf("recipient view = %v", b)
	}
	if w := post("bob", false, `{"city": "Reno, NV", "refresh_minutes": 1}`); w.Code != http.StatusOK {
		t.Fatalf("recipient save: %d %s", w.Code, w.Body.String())
	}
	bs := loadSettingValues(T.settingsBase(spec, "bob"), "wx")
	if bs["city"] != "Reno, NV" {
		t.Fatalf("bob's city = %v", bs)
	}
	if _, ok := bs["refresh_minutes"]; ok {
		t.Fatal("a recipient must not write an owner-scoped setting")
	}
	if stored = loadSettingValues(T.settingsBase(spec, "alice"), "wx"); stored["refresh_minutes"] != "15" {
		t.Fatalf("bob's save must not touch alice's values: %v", stored)
	}

	// Reset clears the person's own overrides and nobody else's.
	w := httptest.NewRecorder()
	T.handleSettingsReset(w, httptest.NewRequest(http.MethodPost, "/apps/wx/_settings/reset", nil), spec, "bob", false)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d", w.Code)
	}
	if b = get("bob", false); b["city"] != "Santa Cruz, CA" {
		t.Fatalf("after reset bob's city = %v", b["city"])
	}
	if d = get("alice", true); d["refresh_minutes"] != 15.0 {
		t.Fatalf("bob's reset must not touch alice's values: %v", d)
	}
}

// TestSettingsPageShowsWhatThePersonMaySet: the owner's page carries every
// declared field; a recipient's carries only the per-user ones; the form
// loads from the app's own values endpoint.
func TestSettingsPageShowsWhatThePersonMaySet(t *testing.T) {
	T := sharingTestApp(t)
	spec := settingsSpec()
	render := func(owner bool) string {
		w := httptest.NewRecorder()
		T.handleSettingsPage(w, httptest.NewRequest(http.MethodGet, "/apps/wx/_settings", nil), spec, owner)
		if w.Code != http.StatusOK {
			t.Fatalf("page: %d", w.Code)
		}
		return w.Body.String()
	}
	own := render(true)
	for _, want := range []string{"refresh_minutes", "alerts", "city", "units", "/apps/wx/_settings/values", "/apps/wx/_settings/reset", "Default: 5."} {
		if !strings.Contains(own, want) {
			t.Fatalf("owner page lacks %q", want)
		}
	}
	other := render(false)
	if strings.Contains(other, "refresh_minutes") || !strings.Contains(other, "city") {
		t.Fatalf("recipient page must show only per-user settings")
	}

	// An app with no settings says so rather than rendering an empty form.
	w := httptest.NewRecorder()
	T.handleSettingsPage(w, httptest.NewRequest(http.MethodGet, "/apps/x/_settings", nil), AppSpec{Slug: "x", Name: "X", Owner: "alice"}, true)
	if !strings.Contains(w.Body.String(), "Nothing to set") {
		t.Fatal("an app without settings must render the empty state")
	}
}
