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

// TestSettingsForResolvesByScope: defaults first, the owner's set values for
// owner-scoped settings, each person's own for per-user ones; an anonymous
// reader gets the owner's copy; every declared setting is always present.
func TestSettingsForResolvesByScope(t *testing.T) {
	T := sharingTestApp(t)
	spec := settingsSpec()
	T.settingsBase(spec, "alice").Set(settingsTable, "wx", map[string]string{"refresh_minutes": "15", "city": "Oakland, CA"})
	T.settingsBase(spec, "bob").Set(settingsTable, "wx", map[string]string{"city": "Reno, NV", "refresh_minutes": "1"})

	own := T.settingsFor(spec, "alice")
	if own["refresh_minutes"] != "15" || own["city"] != "Oakland, CA" || own["alerts"] != "false" || own["units"] != "metric" {
		t.Fatalf("owner = %v", own)
	}
	bob := T.settingsFor(spec, "bob")
	if bob["refresh_minutes"] != "15" {
		t.Fatalf("an owner-scoped setting must come from the owner even when bob stored one: %v", bob)
	}
	if bob["city"] != "Reno, NV" || bob["units"] != "metric" {
		t.Fatalf("bob's per-user values = %v", bob)
	}
	anon := T.settingsFor(spec, "")
	if anon["city"] != "Oakland, CA" || anon["refresh_minutes"] != "15" {
		t.Fatalf("anonymous must get the owner's copy: %v", anon)
	}
	if len(anon) != 4 {
		t.Fatalf("every declared setting must be present: %v", anon)
	}
	if n := len(T.settingsFor(AppSpec{Slug: "x", Owner: "alice"}, "alice")); n != 0 {
		t.Fatalf("an app without settings adds nothing: %d", n)
	}
}

// TestSettingsWinOverParams: a query param with a setting's name — which
// anyone holding a public link can put in the URL — never overrides the
// value set on the Settings page.
func TestSettingsWinOverParams(t *testing.T) {
	T := sharingTestApp(t)
	spec := settingsSpec()
	T.settingsBase(spec, "alice").Set(settingsTable, "wx", map[string]string{"city": "Oakland, CA"})
	args := map[string]any{"records": "[]", "city": "Hacked", "q": "1"}
	T.applySettings(args, spec, "alice")
	if args["city"] != "Oakland, CA" || args["q"] != "1" || args["refresh_minutes"] != "5" {
		t.Fatalf("args = %v", args)
	}
}

// TestSettingsReachTheScript runs a real data source and reads the settings
// back out of its environment. Skips where the sandbox cannot exec.
func TestSettingsReachTheScript(t *testing.T) {
	T := sharingTestApp(t)
	spec := settingsSpec()
	spec.DataSources = []AppDataSource{{
		Name:         "env",
		Language:     "bash",
		Script:       `printf '{"city":"%s","refresh":"%s","q":"%s"}' "$city" "$refresh_minutes" "$q"`,
		Capabilities: []string{},
	}}
	T.settingsBase(spec, "alice").Set(settingsTable, "wx", map[string]string{"refresh_minutes": "30"})
	T.settingsBase(spec, "bob").Set(settingsTable, "wx", map[string]string{"city": "Reno, NV"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/apps/wx/data/env?city=Hacked&q=1", nil)
	T.handleData(w, r, "alice", "bob", T.recordBase(spec, "bob"), spec, "env")
	if w.Code != http.StatusOK {
		t.Skipf("sandbox/bash unavailable in this environment: %d %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Skipf("non-JSON output (likely no sandbox): %q", w.Body.String())
	}
	if got["city"] != "Reno, NV" || got["refresh"] != "30" || got["q"] != "1" {
		t.Fatalf("script env = %v", got)
	}
}
