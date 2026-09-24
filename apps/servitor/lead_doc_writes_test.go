package servitor

import (
	"strings"
	"testing"
)

// The lead does not write the knowledge docs: consolidation does, once, after
// the answer. A prompt that still told the lead to call update_doc after every
// probe would send it hunting a tool it no longer holds, a round per probe.
func TestTheLeadIsNotToldToWriteDocs(t *testing.T) {
	for _, typ := range []string{"", "repo", "bundle", "toolset"} {
		got := buildLeadSystemPrompt(nil, Appliance{Name: "box", Type: typ}, map[string]string{"overview": "x"}, "", "", "", "", "", false)
		if strings.Contains(got, "update_doc") {
			t.Errorf("type %q: the lead prompt still names update_doc", typ)
		}
	}
}

// Consolidation reads EVERY probe's findings, not the last one's: it is the
// only writer, so a finding it is not handed is a finding the docs lose.
func TestConsolidationSeesEveryProbe(t *testing.T) {
	pr := &probeRun{}
	pr.c.allProbeResults = []string{"first: port 5432", "second: /etc/app.conf"}
	got := pr.allFindings()
	if !strings.Contains(got, "port 5432") || !strings.Contains(got, "/etc/app.conf") {
		t.Errorf("findings should carry every probe: %q", got)
	}
	pr.c.allProbeResults = []string{strings.Repeat("a", maxTurnFindings+10)}
	if got := pr.allFindings(); len(got) > maxTurnFindings+len("\n... [truncated]") {
		t.Errorf("findings should be capped, got %d bytes", len(got))
	}
}
