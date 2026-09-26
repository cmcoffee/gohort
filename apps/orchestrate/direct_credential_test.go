package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Only Builder fetches a credential's API directly by default. Another agent
// reaches it through the tools bound to it, unless its owner granted the raw
// tool by name, and the refusal names the bound tools and the way to Builder.
func TestDirectCredentialAccessIsBuildersByDefault(t *testing.T) {
	if directCredentialRefusal(AgentRecord{ID: "seed-builder"}, &ToolSession{}) != nil {
		t.Error("Builder keeps direct access: it probes APIs while building against them")
	}

	sess := &ToolSession{}
	sess.AppendTempTool(&TempTool{Name: "generate_music", Mode: TempToolModeAPI, Credential: "gen_api"})
	sess.AppendTempTool(&TempTool{Name: "extract_song", ScriptBody: "x", HookCapabilities: []string{"fetch", "fetch_via:gen_api"}})
	sess.AppendTempTool(&TempTool{Name: "weather", Mode: TempToolModeAPI, Credential: "weather_api"})
	refuse := directCredentialRefusal(AgentRecord{ID: "wren"}, sess)
	msg := refuse("gen_api")
	for _, want := range []string{`"gen_api"`, "Nothing was sent", "Use extract_song, generate_music", "offer to have Builder"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should say %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "weather") {
		t.Error("only tools bound to this credential are named")
	}
	if msg := refuse("other_api"); !strings.Contains(msg, "No tool bound to it") {
		t.Errorf("with nothing bound, it says so: %s", msg)
	}

	owned := directCredentialRefusal(AgentRecord{ID: "ops", AllowedTools: []string{"web_search", "fetch_url_gen_api", "call_legacy_api"}}, sess)
	if owned("gen_api") != "" || owned("legacy_api") != "" {
		t.Error("a credential the owner granted by name, current or legacy spelling, stays reachable")
	}
	if owned("weather_api") == "" {
		t.Error("a credential not granted is still refused")
	}
}
