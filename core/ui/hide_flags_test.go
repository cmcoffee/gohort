package ui

// An app can say an agent has no Channels section and no particular nav item
// of its own: both name a window-global map keyed by agent id, so core/ui
// learns neither why nor which agents.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHideFlagsReachThePanel(t *testing.T) {
	raw, _ := json.Marshal(AgentLoopPanel{ChannelsHideFlag: "APP_HIDE", OrchestratorNav: []OrchestratorNavItem{{Label: "Clock", HideFlag: "APP_HIDE"}}})
	for _, want := range []string{`"channels_hide_flag":"APP_HIDE"`, `"hide_flag":"APP_HIDE"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s: %s", want, raw)
		}
	}
	raw, _ = json.Marshal(AgentLoopPanel{OrchestratorNav: []OrchestratorNavItem{{Label: "Clock"}}})
	if strings.Contains(string(raw), "hide_flag") {
		t.Errorf("unused hide flags should be omitted: %s", raw)
	}
}

func TestTheRuntimeHonorsTheHideFlags(t *testing.T) {
	js := string(runtimeJS)
	for _, want := range []string{
		"function flaggedFor(flag, agentId)",
		"if (flaggedFor(item.hide_flag, agentId)) return false;",                  // ahead of all_agents / alt nav
		"if (flaggedFor(cfg.channels_hide_flag, window.GOHORT_AGENT_ID)) {",       // before the fetch
		"if (flaggedFor(cfg.channels_hide_flag, window.GOHORT_AGENT_ID)) return;", // a fetch that lands after a switch
	} {
		if !strings.Contains(js, want) {
			t.Errorf("runtime missing %q", want)
		}
	}
}
