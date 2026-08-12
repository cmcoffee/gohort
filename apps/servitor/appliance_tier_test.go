package servitor

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestApplianceTierParsing — an unrecognized value follows routing rather than
// pinning a tier nobody chose. Records written by a future version, or edited by
// hand, must degrade to the deployment's own decision.
func TestApplianceTierParsing(t *testing.T) {
	cases := map[string]LLMTier{
		"lead": LEAD, "LEAD": LEAD, " lead ": LEAD,
		"worker": WORKER, "Worker": WORKER,
		"": TierUnset, "precision": TierUnset, "true": TierUnset,
	}
	for in, want := range cases {
		if got := applianceTierOverride(in); got != want {
			t.Errorf("applianceTierOverride(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"lead": "lead", "LEAD": "lead", "worker": "worker", "": "", "nonsense": "",
	} {
		if got := normalizeApplianceTier(in); got != want {
			t.Errorf("normalizeApplianceTier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTheModalGatesLeadOnTheServerFlag — the form is hand-written JS in the
// chat page's asset, so the gate has to be checked THERE.
//
// This is the second half of a lesson: the per-appliance tier fields were first
// added to applianceFields() in page.go, which read like the appliance form and
// was called by nothing at all. They rendered nowhere. That file is gone; the
// modal below is the form, and these assertions are against the thing that
// actually ships.
func TestTheModalGatesLeadOnTheServerFlag(t *testing.T) {
	src, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "if (rec.lead_tier_available) opts.push(['lead', 'Lead (always)']);") {
		t.Error("the modal no longer gates the Lead option on lead_tier_available — it would " +
			"offer a tier the server refuses to save and the runtime ignores")
	}
	for _, field := range []string{"orchestrator_tier: orchTierIn.value", "worker_tier:       workTierIn.value"} {
		if !strings.Contains(body, field) {
			t.Errorf("the save payload does not carry %q — the select would render and never persist", field)
		}
	}
	// Both selects must be in the modal body, or one of them is unreachable.
	if !strings.Contains(body, "tierSection,") {
		t.Error("the tier section is built but never added to the modal body")
	}
}

// TestTheAvailabilityFlagIsComputedNotStored — it is a deployment fact, so
// persisting it would freeze a snapshot that goes stale the moment Model
// Privacy changes.
func TestTheAvailabilityFlagIsComputedNotStored(t *testing.T) {
	src, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if n := strings.Count(body, "LeadTierAvailable = AllLLMsPrivate()"); n < 3 {
		t.Errorf("only %d of the appliance read paths set the availability flag — a form "+
			"reached through one of the others would silently hide the Lead option", n)
	}
	if !strings.Contains(body, "req.LeadTierAvailable = false") {
		t.Error("the save path does not clear the computed flag, so it would be persisted")
	}
}
