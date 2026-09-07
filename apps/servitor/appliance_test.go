package servitor

import (
	. "github.com/cmcoffee/gohort/core"
	"os"
	"strings"
	"testing"
)

// A write-only secret must survive a save that never carried it.
//
// Every read path blanks Password and RepoToken before the record leaves the
// server, so the edit form always loads with them empty — which means an empty
// field on save says "you were not shown this", never "clear it".
//
// The guard used to read the INCOMING type: `req.Type == "repo" && ...`. Any
// save whose body did not carry that type wrote a blank straight over a stored
// token, and it presented as "no token configured" rather than as a token that
// had stopped working, because by then there genuinely was none. Asserted on
// the source because the handler is inline in an HTTP switch with no seam to
// call — the shape of the guard IS the fix.
func TestAWriteOnlySecretIsNotBlankedByASaveThatOmitsIt(t *testing.T) {
	src := webSource(t)

	for _, want := range []string{
		"if req.Password == \"\" {\n\t\t\t\treq.Password = existing.Password",
		"if req.RepoToken == \"\" {\n\t\t\t\treq.RepoToken = existing.RepoToken",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("a write-only secret must be preserved on ANY save that omits it:\n%s", want)
		}
	}
	// The type-gated form is what let the blank through. If it comes back,
	// so does the bug.
	for _, bad := range []string{
		`req.Type == "ssh" && req.Password == ""`,
		`req.Type == "repo" && req.RepoToken == ""`,
	} {
		if strings.Contains(src, bad) {
			t.Errorf("the preserve is gated on the incoming type again: %s", bad)
		}
	}
	// A real type change still drops the secret that belongs to the kind this
	// appliance no longer is — it can never be used again, and leaving it is
	// secret material sitting in a record nobody thinks holds one.
	if !strings.Contains(src, `if req.Type != "" && req.Type != existing.Type {`) {
		t.Error("a genuine type change must clear the secret of the type being left")
	}
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
	body := webSource(t)
	if n := strings.Count(body, "LeadTierAvailable = AllLLMsPrivate()"); n < 3 {
		t.Errorf("only %d of the appliance read paths set the availability flag — a form "+
			"reached through one of the others would silently hide the Lead option", n)
	}
	if !strings.Contains(body, "req.LeadTierAvailable = false") {
		t.Error("the save path does not clear the computed flag, so it would be persisted")
	}
}
