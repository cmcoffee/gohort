package servitor

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

func tierPrivacy(t *testing.T, on bool) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	SetAllLLMsPrivate(on)
}

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

// TestLeadIsNotOfferedWhenPrivacyIsOff — the rule this codebase keeps relearning:
// a control that saves, reads back correctly and changes nothing is worse than
// no control. Servitor is pinned to the worker with privacy off, so the option
// must not be there to pick.
func TestLeadIsNotOfferedWhenPrivacyIsOff(t *testing.T) {
	tierPrivacy(t, false)
	var values []string
	for _, o := range applianceTierOptions("the orchestrator") {
		values = append(values, o.Value)
	}
	for _, v := range values {
		if v == "lead" {
			t.Error("Lead is offered while Model Privacy is off — it would save, read back, and do nothing")
		}
	}
	// Following routing and pinning DOWN to the worker stay available: neither
	// escalates, so neither is affected by the pin.
	if len(values) != 2 {
		t.Errorf("options are %v, want follow-routing and worker", values)
	}
	// And the help has to say WHY lead is missing, or its absence is its own
	// kind of confusing.
	help := applianceTierHelp("the orchestrator")
	if !strings.Contains(help, "Model Privacy") {
		t.Errorf("the help does not explain why Lead is absent: %q", help)
	}
}

// TestLeadIsOfferedWhenEveryModelIsPrivate — the mirror. Hiding it always would
// make the whole feature unreachable.
func TestLeadIsOfferedWhenEveryModelIsPrivate(t *testing.T) {
	tierPrivacy(t, true)
	var found bool
	for _, o := range applianceTierOptions("the workers") {
		if o.Value == "lead" {
			found = true
		}
	}
	if !found {
		t.Error("Lead is not offered even though every configured model is private")
	}
}
