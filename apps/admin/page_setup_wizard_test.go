package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The first-run wizard writes through the shared api/worker-llm endpoint, so
// the failure mode is silent: a mistyped field name or endpoint renders a
// perfectly nice form that saves nothing, or saves under a key the loader
// never reads. These pin the wiring the renderer cannot check for itself.

// wizardPanel digs the FormPanel out of a freshly built wizard page.
func wizardPanel(t *testing.T) ui.FormPanel {
	t.Helper()
	a := &AdminApp{}
	page := a.setupWizardPage()
	if len(page.Sections) != 1 {
		t.Fatalf("expected one section, got %d", len(page.Sections))
	}
	panel, ok := page.Sections[0].Body.(ui.FormPanel)
	if !ok {
		t.Fatalf("section body is %T, want ui.FormPanel", page.Sections[0].Body)
	}
	return panel
}

func TestSetupWizardWiring(t *testing.T) {
	panel := wizardPanel(t)

	// Source/PostURL must match the endpoint that actually persists these
	// keys, and must stay RELATIVE: the page is served at <prefix>/setup, so
	// the browser resolves them to <prefix>/api/... wherever admin is mounted.
	for name, got := range map[string]string{
		"Source":  panel.Source,
		"PostURL": panel.PostURL,
	} {
		if got != "api/worker-llm" {
			t.Errorf("%s = %q, want %q", name, got, "api/worker-llm")
		}
		if strings.HasPrefix(got, "/") {
			t.Errorf("%s is absolute (%q); it must be relative to survive being mounted under a prefix", name, got)
		}
	}
	if panel.TestURL != "api/worker-llm/test" {
		t.Errorf("TestURL = %q", panel.TestURL)
	}
	// Without SubmitLabel the panel auto-saves each field on blur, which in a
	// wizard would write a half-configured provider before the test step.
	if panel.SubmitLabel == "" {
		t.Error("SubmitLabel must be set, or the wizard saves field-by-field instead of on Finish")
	}
	if panel.RedirectTarget != "_self" {
		t.Errorf("RedirectTarget = %q, want _self (the wizard replaces itself)", panel.RedirectTarget)
	}
	if len(panel.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(panel.Steps))
	}
	// The Test button renders on the LAST step only (runtime: appendTestRow
	// into the finish wrapper), so the closing step is the one that must
	// invite the operator to press it.
	if last := panel.Steps[len(panel.Steps)-1]; last.Intro == "" {
		t.Error("the final step needs an intro — it is where the Test button appears")
	}
}

// Every field the wizard renders has to name a key the worker-llm handler
// actually decodes, or the answer is silently dropped on submit.
func TestSetupWizardFieldsMatchHandler(t *testing.T) {
	handled := map[string]bool{
		"provider": true, "model": true, "api_key": true,
		"endpoint": true, "aws_region": true, "aws_profile": true,
		"bedrock_api": true,
	}
	panel := wizardPanel(t)
	seen := map[string]bool{}
	for _, step := range panel.Steps {
		for _, f := range step.Fields {
			if f.Type == "header" {
				continue
			}
			if !handled[f.Field] {
				t.Errorf("step %q field %q is not decoded by handleLLMConfig", step.Title, f.Field)
			}
			seen[f.Field] = true
		}
	}
	// Provider is the one field without which nothing else means anything.
	if !seen["provider"] {
		t.Error("the wizard never asks for a provider")
	}
}

// ShowWhen values must match the option values exactly — the runtime compares
// with String(actual) === opt, so a near-miss just hides the field forever.
func TestSetupWizardShowWhenMatchesOptions(t *testing.T) {
	panel := wizardPanel(t)
	values := map[string]bool{}
	for _, step := range panel.Steps {
		for _, f := range step.Fields {
			for _, o := range f.Options {
				values[o.Value] = true
			}
		}
	}
	for _, step := range panel.Steps {
		for _, f := range step.Fields {
			if f.ShowWhen == "" {
				continue
			}
			_, rhs, found := strings.Cut(f.ShowWhen, ":")
			if !found {
				continue
			}
			for _, v := range strings.Split(rhs, "|") {
				if !values[v] {
					t.Errorf("field %q gates on provider %q, which is not one of the select's options", f.Field, v)
				}
			}
		}
	}
}

// The page must marshal: it is handed to the browser as JSON, and an
// unmarshalable body fails at request time rather than at build time.
func TestSetupWizardPageMarshals(t *testing.T) {
	a := &AdminApp{}
	if _, err := json.Marshal(a.setupWizardPage()); err != nil {
		t.Fatalf("wizard page does not marshal: %v", err)
	}
}

// TestTheDashboardTellsAFreshAdminWhatIsMissing. The first-run wizard has
// always existed and always redirected — from the ADMIN app's root. Login
// redirects to "/", so the first admin met a grid of app cards, opened one,
// and watched it fail with no model, while the guided flow sat at a URL
// nothing had sent them to.
func TestTheDashboardTellsAFreshAdminWhatIsMissing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	app := &AdminApp{db: db}

	// No provider configured: the notice appears, and it hands over the
	// wizard rather than merely reporting the problem.
	got := app.firstRunNotices(false)
	if len(got) != 1 {
		t.Fatalf("a fresh install told the admin nothing: %+v", got)
	}
	if got[0].URL != app.setupWizardPath() {
		t.Errorf("the notice points at %q, not the wizard", got[0].URL)
	}
	if got[0].Action == "" {
		t.Error("the notice has no button, so it is a complaint rather than a route")
	}

	// Once a provider is saved it stops — the same condition the redirect
	// uses, read from the same helper, so the two cannot disagree.
	db.Set(LLMTable, "provider", "openai")
	if got := app.firstRunNotices(false); len(got) != 0 {
		t.Errorf("a configured install still nags: %+v", got)
	}

	// And a dismissed wizard stays dismissed on the dashboard too — one
	// decision, both surfaces.
	db.Unset(LLMTable, "provider")
	if got := app.firstRunNotices(true); len(got) != 0 {
		t.Errorf("the notice ignores the dismissal the wizard honours: %+v", got)
	}
}

// An error that already ends a sentence is not given a second full stop.
func TestANoticeSentenceEndsOnce(t *testing.T) {
	for in, want := range map[string]string{
		"run `aws sso login --profile x`.": "run `aws sso login --profile x`.",
		"the lead model failed":            "the lead model failed.",
		"is it configured?":                "is it configured?",
		"  ":                               "",
	} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// The lead notice says when the lead last failed and last worked, so it
// reads as failing now or recovering.
func TestTheLeadNoticeSaysWhenItLastFailedAndWorked(t *testing.T) {
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.Local)
	for _, c := range []struct {
		st   LeadCallStats
		want string
	}{
		{LeadCallStats{LastFailure: at}, "Last failed at 09:00; no call has worked yet."},
		{LeadCallStats{LastFailure: at, LastSuccess: at.Add(5 * time.Minute), OKStreak: 2}, "Last failed at 09:00; last worked at 09:05 (2 in a row since)."},
		{LeadCallStats{LastFailure: at, LastSuccess: at.Add(-time.Hour)}, "Last failed at 09:00; last worked at 08:00, before that."},
	} {
		if got := leadLatest(c.st); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
