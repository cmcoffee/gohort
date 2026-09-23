package orchestrate

// The Tools tab sorts by what a call can touch, and says so per row.
//
// It used to be one flat list where every registered tool read "framework", so
// read_file and a shell on a connected appliance carried the same two controls
// and looked like the same kind of thing.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The bands, in precedence order. What a call REACHES decides, not where the
// tool came from - a tool the owner wrote that dispatches through a credential
// reaches that system, and the system is the larger fact.
func TestAToolIsBandedByWhatItReaches(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		cred     string
		own      bool
		caps     []Capability
		band     string
		reaches  string
	}{
		{"an app's tool names the system it belongs to",
			"servitor", "", false, []Capability{CapExecute}, bandConnected, "servitor"},
		{"a credential-backed tool names the credential",
			"", "issue_tracker", true, []Capability{CapNetwork}, bandConnected, "issue_tracker"},
		{"an owner-scoped credential drops its scoping",
			"", "@u:alice:issue_tracker", true, nil, bandConnected, "issue_tracker"},
		{"no_auth is not a system",
			"", "no_auth", true, nil, bandOwn, ""},
		{"the owner's own tool, unauthenticated",
			"", "", true, nil, bandOwn, ""},
		{"a framework tool that dials out",
			"", "", false, []Capability{CapNetwork, CapRead}, bandInternet, ""},
		{"a framework tool that does not",
			"", "", false, []Capability{CapRead}, bandInternal, ""},
		{"an unannotated framework tool reads as internal, and is always allowed",
			"", "", false, nil, bandInternal, ""},
	}
	for _, c := range cases {
		band, reaches := classifyTool(c.provider, c.cred, c.own, c.caps)
		if band != c.band {
			t.Errorf("%s: band = %q, want %q", c.name, band, c.band)
		}
		if reaches != c.reaches {
			t.Errorf("%s: reaches = %q, want %q", c.name, reaches, c.reaches)
		}
	}

	// An app's tool beats the capabilities it happens to declare. A provider
	// is registered because the capability belongs to a machine somebody
	// connected, and that is true whether or not the tool calls it network.
	if band, _ := classifyTool("servitor", "", false, []Capability{CapRead}); band != bandConnected {
		t.Errorf("a read-only app tool fell out of the connected band: %q", band)
	}
}

// The always-allowed band is the point of the exercise: a per-call decision
// about read_file is a question nobody can answer usefully sixty times.
func TestOnlyWhatReachesOutsideCarriesControls(t *testing.T) {
	for _, band := range []string{bandConnected, bandInternet, bandOwn} {
		if !bandGoverns(band) {
			t.Errorf("%q lost its controls, so something reaching outside cannot be stopped", band)
		}
	}
	if bandGoverns(bandInternal) {
		t.Error("the internal band offers per-call controls again")
	}
	if bandGoverns(bandOff) {
		t.Error("a tool the agent does not load offers a decision about calling it")
	}
}

// Bands render in descending order of what a call can touch. The table groups
// in RECORD order, so this ordering is what puts the consequential tools at the
// top of the page instead of alphabetically among sixty others.
func TestTheConsequentialBandsComeFirst(t *testing.T) {
	want := []string{bandConnected, bandInternet, bandOwn, bandInternal, bandOff}
	for i := 1; i < len(want); i++ {
		if bandOrder(want[i-1]) >= bandOrder(want[i]) {
			t.Errorf("%q does not sort before %q", want[i-1], want[i])
		}
	}
}

// A decision already made stays visible even where new ones are not offered.
// The gate marks by NAME and does not ask what a tool reaches, so a mark set
// through the Tools modal - or before this page had bands - still fires. Hiding
// the control would leave a setting nobody can see or undo.
func TestAnExistingDecisionSurvivesTheAlwaysAllowedBand(t *testing.T) {
	src := mustReadFile(t, "agent_access_tools.go")
	if !strings.Contains(src, "bandGoverns(row.Band) || row.Asks || row.Unattended != PolicyAllow") {
		t.Error("a mark on an internal tool is now unreachable: the gate honours it and the page will not show it")
	}
}

// The page groups on the band and names the system.
func TestTheToolsTabRendersTheBands(t *testing.T) {
	page := mustReadFile(t, "page_agent_access.go")
	if !strings.Contains(page, `GroupBy: "band"`) {
		t.Error("the tools table is flat again")
	}
	if !strings.Contains(page, `{Field: "reaches"`) {
		t.Error("a row says it reaches a system you connected without saying which")
	}
	// The old copy said framework tools show no controls because they have no
	// record to hold one. That stopped being the reason: the marks are keyed by
	// name and reach any tool, and the reason now is what the tool reaches.
	if strings.Contains(page, "a framework tool has nothing to hold the setting") {
		t.Error("the tab still explains the controls by which tools carry a record")
	}
}
