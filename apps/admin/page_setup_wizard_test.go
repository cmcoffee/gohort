package admin

// The first-run wizard writes through the shared api/worker-llm endpoint, so
// the failure mode is silent: a mistyped field name or endpoint renders a
// perfectly nice form that saves nothing, or saves under a key the loader
// never reads. These pin the wiring the renderer cannot check for itself.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
)

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
