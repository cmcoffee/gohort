package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		"provider": true, "model": true, "api_key": true, "api_key_from": true,
		"endpoint": true, "aws_region": true, "aws_profile": true, "bedrock_api": true,
	}
	panel := wizardPanel(t)
	seen := map[string]bool{}
	for _, step := range panel.Steps {
		for _, f := range step.Fields {
			// A header, and a line the form shows and never sends.
			if f.Type == "header" || f.Type == "readonly" {
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
	// The values each field can hold: its options, and for a select whose
	// options load when the form opens (OptionsSource), its default.
	values := map[string]map[string]bool{}
	for _, step := range panel.Steps {
		for _, f := range step.Fields {
			if values[f.Field] == nil {
				values[f.Field] = map[string]bool{}
			}
			for _, o := range f.Options {
				values[f.Field][o.Value] = true
			}
			if f.OptionsSource != "" && f.Default != "" {
				values[f.Field][f.Default] = true
			}
		}
	}
	for _, step := range panel.Steps {
		for _, f := range step.Fields {
			// "a:x|y;b:z" is a AND b: each condition names its own field.
			for _, cond := range strings.Split(f.ShowWhen, ";") {
				field, rhs, found := strings.Cut(cond, ":")
				if !found {
					continue
				}
				for _, v := range strings.Split(rhs, "|") {
					if !values[field][v] {
						t.Errorf("field %q gates on %s %q, which is not one of its options", f.Field, field, v)
					}
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

// Skipping the setup is a link, so a link on another site must not do it for
// a signed-in administrator; the page's own Skip still does.
func TestSetupIsNotSkippedFromAnotherSite(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	AuthSetUser(db, "root", "pw", true)
	a := &AdminApp{db: db}
	skip := func(site string) {
		r := httptest.NewRequest("GET", "/admin/setup?skip=1", nil)
		r.Header.Set("Sec-Fetch-Site", site)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, "root")})
		a.handleSetupWizard(httptest.NewRecorder(), r)
	}
	skip("cross-site")
	if AuthGetFirstRunDismissed(db, "root") {
		t.Fatal("a link on another site skipped the setup")
	}
	skip("same-origin")
	if !AuthGetFirstRunDismissed(db, "root") {
		t.Error("the page's own Skip did nothing")
	}
}

// The Judge model's test asks what the checks ask, one answer in JSON with
// thinking off, and nothing more: no tool-call handoff, which checks never
// use, so its failures would read a working judge as broken.
func TestTheJudgeTestAsksWhatTheChecksAsk(t *testing.T) {
	run := func(reply string) (map[string]any, *FakeLLM) {
		t.Helper()
		llm := &FakeLLM{Turns: []FakeTurn{{Content: reply, Repeat: true}}}
		w := httptest.NewRecorder()
		(&AdminApp{}).testJudge(context.Background(), w, llm, "judge-4b", " Server: vLLM.")
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("result: %v %s", err, w.Body)
		}
		return out, llm
	}
	out, llm := run(`{"answer": "yes"}`)
	if out["ok"] != true || !strings.Contains(out["message"].(string), "in JSON with thinking off") {
		t.Errorf("a judge that answers in JSON: %+v", out)
	}
	if llm.Calls() != 1 {
		t.Errorf("the test made %d calls; one check, no handoff", llm.Calls())
	}
	cfg := llm.Config(0)
	if !cfg.JSONMode || cfg.Think == nil || *cfg.Think {
		t.Errorf("asked as a check is: json=%v think=%v", cfg.JSONMode, cfg.Think)
	}
	if out, _ := run("Sure! The sky is blue."); out["ok"] != false || !strings.Contains(asText(out["error"]), "not in the JSON the checks need") {
		t.Errorf("a judge that answers in prose: %+v", out)
	}
}

// asText is a decoded JSON value as a string, "" when it is not one.
func asText(v any) string {
	s, _ := v.(string)
	return s
}

// A system with a Lead or an Offload model set and no Worker is not new: it is
// not sent to the wizard, which sets only the Worker and hid the menu holding
// the rest. It gets a card saying every turn stops, with the Worker's
// settings a click away. A Worker that is set and did not start gets the
// same card, saying why. Neither is dismissable.
func TestAMissingWorkerOnAConfiguredSystemIsACardNotTheWizard(t *testing.T) {
	t.Cleanup(func() { SetWorkerInitError("", "", nil) })
	SetWorkerInitError("", "", nil)
	db := &DBase{Store: kvlite.MemStore()}
	app := &AdminApp{db: db}
	db.Set(LeadLLMTable, "provider", "bedrock")
	if systemNeedsSetup(db) {
		t.Fatal("a system with a Lead set is sent to first-run setup")
	}
	n, ok := app.workerNotice()
	if !ok || !strings.Contains(n.Text, "No Worker model is set") || n.Dismiss != "" || n.URL == "" {
		t.Fatalf("no Worker on a configured system: %+v %v", n, ok)
	}
	db.Unset(LeadLLMTable, "provider")
	db.Set(JudgeLLMTable, "provider", "vllm")
	if systemNeedsSetup(db) {
		t.Error("a system with an Offload model set is sent to first-run setup")
	}
	// Set, and did not start.
	db.Set(LLMTable, "provider", "bedrock")
	SetWorkerInitError("bedrock", "claude-x", errors.New("no AWS credentials found"))
	if n, ok := app.workerNotice(); !ok || !strings.Contains(n.Text, "did not start: no AWS credentials found") {
		t.Errorf("a Worker that did not start: %+v %v", n, ok)
	}
	// Running: no card. Nothing set anywhere: first-run, not this card.
	SetWorkerInitError("bedrock", "claude-x", nil)
	if _, ok := app.workerNotice(); ok {
		t.Error("a running Worker has a card")
	}
	empty := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	if _, ok := empty.workerNotice(); ok || !systemNeedsSetup(empty.db) {
		t.Error("a new system gets the Worker card instead of first-run setup")
	}
}
