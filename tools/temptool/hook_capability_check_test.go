package temptool

// The undeclared-hook check. A repairing agent swapped fetch_url for
// browse_page in a script and left hook_capabilities alone; the call was
// refused at dispatch, the script died on what it did with the result, and the
// agent concluded the remote SITE was blocking it. Static and deterministic —
// the same class of check as the syntax pass.

import (
	"strings"
	"testing"
)

func TestScriptCallsHookSpotsBothImportAndCall(t *testing.T) {
	cases := []struct {
		body, name string
		want       bool
	}{
		{"from gohort import browse_page\nx = browse_page(u)", "browse_page", true},
		{"from gohort import fetch_url, log", "log", true},
		{"resp = gohort.fetch_via(\"cred\", url)", "fetch_via", true},
		{"from gohort import fetch_url\nx = fetch_url(u)", "browse_page", false},
		{"", "fetch_url", false},
	}
	for _, c := range cases {
		if got := scriptCallsHook(c.body, c.name); got != c.want {
			t.Errorf("scriptCallsHook(%q, %q) = %v, want %v", c.body, c.name, got, c.want)
		}
	}
}

func TestHookCapabilityDeclaredMatchesTheServerGate(t *testing.T) {
	// Must mirror SandboxHook.granted: bare name, or any qualified form.
	caps := []string{"fetch", "fetch_via:openweather", " secret:apikey "}
	for _, want := range []string{"fetch", "fetch_via", "secret"} {
		if !hookCapabilityDeclared(caps, want) {
			t.Errorf("%q should count as declared by %v", want, caps)
		}
	}
	if hookCapabilityDeclared(caps, "browse_page") {
		t.Error("browse_page is not in that list — the check must not pass it")
	}
	// "fetch" must not satisfy "fetch_via": prefix matching runs the other way
	// (capability may be qualified, the wanted method never is).
	if hookCapabilityDeclared([]string{"fetch"}, "fetch_via") {
		t.Error("a bare fetch grant must not satisfy fetch_via")
	}
}

// A script that imports a name the gohort module does not export is refused at
// authoring, and a credential's catalog-tool name is answered with the call that
// actually works from a script.
func TestAScriptCannotImportWhatTheGohortModuleDoesNotExport(t *testing.T) {
	why := unknownGohortName("import os\nfrom gohort import fetch_url_gemini_api\n")
	for _, want := range []string{"fetch_url_gemini_api", "from gohort import fetch_url", "no grant", `fetch_via("gemini_api"`, "fetch_via:gemini_api"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal should carry %q:\n%s", want, why)
		}
	}
	if why := unknownGohortName("from gohort import (\n    fetch_via,\n    made_up as m,\n)\n"); !strings.Contains(why, `"made_up"`) || !strings.Contains(why, "fetch_via") {
		t.Errorf("a parenthesized import is read name by name: %q", why)
	}
	if why := unknownGohortName("import gohort\nr = gohort.call_weather(url)\n"); !strings.Contains(why, `fetch_via("weather"`) {
		t.Errorf("a method call on the module is checked too: %q", why)
	}
	for _, ok := range []string{
		"from gohort import fetch_url, fetch_via as fv, log\n",
		"from gohort import secret  # the key\nimport gohort\ngohort.fetch_url(u)\n",
		"# see docs.gohort.example(1) for more\nprint('from gohort import nothing')\n",
	} {
		if why := unknownGohortName(ok); why != "" {
			t.Errorf("a script using only real names must pass: %q\n%s", ok, why)
		}
	}
}
