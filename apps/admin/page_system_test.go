package admin

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Sign-in shows three of the Tuning tab's knobs by key. A key that stops being
// registered (renamed, removed) is left off the form without a word, so the
// setting would quietly be on one tab only.
func TestSignInTunablesAreRegistered(t *testing.T) {
	for _, k := range signInTunables {
		if _, ok := LookupTunable(k); !ok {
			t.Errorf("Sign-in shows %q, which is not a registered tunable", k)
		}
	}
	if got := len(tunableFieldsFor(signInTunables...)); got != len(signInTunables) {
		t.Errorf("Sign-in builds %d tunable fields, want %d", got, len(signInTunables))
	}
}

// Site Settings was split into Site and Sign-in. Every field it had is on one
// of them or, for the fetch cache quota, on the Tuning tab's Storage &
// retention form: a split that drops a field leaves a setting with no screen.
func TestSiteSettingsFieldsAllHaveAHome(t *testing.T) {
	a := &AdminApp{}
	shown := map[string]bool{}
	for _, sec := range append(a.systemSections(), buildTunableSections()...) {
		for _, fp := range formPanelsIn(sec.Body) {
			for _, f := range fp.Fields {
				shown[f.Field] = true
			}
		}
	}
	for _, f := range []string{
		"allow_signup", "ui_theme", "service_name", "doc_brand", "site_name",
		"external_url", "timezone", "notify_from", "api_key_allow_query",
		"session_days", "session_absolute_days", "max_login_attempts",
		"lockout_minutes", "fetch_cache_quota_mb",
	} {
		if !shown[f] {
			t.Errorf("%s is on no settings form", f)
		}
	}
}

// Network Timeouts and Agent Loop Tuning were one-form sections on other tabs;
// they are panes on their Tuning categories now, still saving to their own
// stores.
func TestRelocatedPanesAreOnTheirCategories(t *testing.T) {
	want := map[string]string{"api/network": "Integrations", "api/agent-loop-tuning": "Turns & context"}
	found := map[string]string{}
	for _, sec := range buildTunableSections() {
		for _, fp := range formPanelsIn(sec.Body) {
			if _, ok := want[fp.Source]; ok {
				found[fp.Source] = sec.Title
			}
		}
	}
	for src, cat := range want {
		if found[src] != cat {
			t.Errorf("%s is on %q, want the %q category", src, found[src], cat)
		}
	}
}
