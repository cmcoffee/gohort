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
		{"from oddjob import browse_page\nx = browse_page(u)", "browse_page", true},
		{"from oddjob import fetch_url, log", "log", true},
		{"resp = oddjob.fetch_via(\"cred\", url)", "fetch_via", true},
		{"from oddjob import fetch_url\nx = fetch_url(u)", "browse_page", false},
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

// A script that imports a name the oddjob module does not export is refused at
// authoring, and a credential's catalog-tool name is answered with the call that
// actually works from a script.
func TestAScriptCannotImportWhatTheOddjobModuleDoesNotExport(t *testing.T) {
	why := unknownOddjobName("import os\nfrom oddjob import fetch_url_gemini_api\n")
	for _, want := range []string{"fetch_url_gemini_api", "from oddjob import fetch_url", "no grant", `fetch_via("gemini_api"`, "fetch_via:gemini_api"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal should carry %q:\n%s", want, why)
		}
	}
	if why := unknownOddjobName("from oddjob import (\n    fetch_via,\n    made_up as m,\n)\n"); !strings.Contains(why, `"made_up"`) || !strings.Contains(why, "fetch_via") {
		t.Errorf("a parenthesized import is read name by name: %q", why)
	}
	if why := unknownOddjobName("import oddjob\nr = oddjob.call_weather(url)\n"); !strings.Contains(why, `fetch_via("weather"`) {
		t.Errorf("a method call on the module is checked too: %q", why)
	}
	for _, ok := range []string{
		"from oddjob import fetch_url, fetch_via as fv, log\n",
		"from oddjob import secret  # the key\nimport oddjob\noddjob.fetch_url(u)\n",
		"# see docs.oddjob.example(1) for more\nprint('from oddjob import nothing')\n",
	} {
		if why := unknownOddjobName(ok); why != "" {
			t.Errorf("a script using only real names must pass: %q\n%s", ok, why)
		}
	}
}

// A tool that calls the author's own tools through default_api is refused at
// save, in a command or a script, with the grant route named.
func TestAToolCannotCallTheAuthorsOwnTools(t *testing.T) {
	for _, src := range []string{
		"print(default_api.bulletins(action='create', name='news'))",
		"import os\nfrom default_api import bulletins\nprint(bulletins(action='create'))\n",
	} {
		why := callsOwnTools(src)
		for _, want := range []string{"default_api", "allow_poster", "allowed tools", "oddjob module"} {
			if !strings.Contains(why, want) {
				t.Errorf("%q: the refusal should carry %q:\n%s", src, want, why)
			}
		}
	}
	if !strings.Contains(callsOwnTools("default_api.bulletins(x)"), "your bulletins tool") {
		t.Error("the tool it reached for is named")
	}
	if why := callsOwnTools("from oddjob import fetch_url\nprint(fetch_url('https://example.com'))\n"); why != "" {
		t.Errorf("an ordinary script passes: %s", why)
	}
}
