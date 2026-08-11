// First-run setup wizard — the guided flow at /admin/setup.
//
// A fresh install serves with no model configured: LLM setup used to live in
// the terminal `--setup` menu and never worked well there (no way to actually
// try the credentials, and a flat wall of tuning knobs next to the three
// settings that matter). It moved here instead, where the form can POST the
// unsaved values at a live endpoint and tell the operator whether the provider
// answers BEFORE anything is written.
//
// The wizard is deliberately thin. It writes through the SAME api/worker-llm
// endpoint the normal Worker LLM section uses, so there is one save path and
// one set of validation rules; the wizard is presentation over that endpoint,
// not a second way to configure a model. Everything it does not ask about
// (thinking budgets, no-think signals, context size, parallel caps, the Lead
// tier) keeps its default and is tuned afterward in the LLMs section.

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// systemNeedsSetup reports whether this install has never had an LLM provider
// configured. That is the one setting without which nothing works, so it is
// the whole definition of "new system" here — an operator who has picked a
// provider is past first-run even if they never touched anything else.
func systemNeedsSetup(db Database) bool {
	if db == nil {
		return false
	}
	var provider string
	db.Get(LLMTable, "provider", &provider)
	return strings.TrimSpace(provider) == ""
}

// setupWizardPath is the wizard's absolute URL. Built from WebPath rather than
// written relative because it is used for a SERVER-side redirect: apps are
// mounted with http.StripPrefix, so r.URL.Path has already lost the "/admin"
// prefix by the time a handler sees it, and a relative target would resolve
// against the server root.
func (a *AdminApp) setupWizardPath() string { return a.WebPath() + "/setup" }

// handleSetupWizard serves the first-run wizard, and the skip affordance that
// dismisses it.
//
// Already-configured systems are bounced to the normal admin page instead of
// being shown the wizard. That is a correctness guard, not just tidiness: the
// wizard renders a subset of the Worker LLM record, so submitting it on a
// tuned system would write that subset over settings it never asked about.
func (a *AdminApp) handleSetupWizard(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	// Skip — record the dismissal against the user so it does not reappear on
	// their next visit, or on another device.
	if r.URL.Query().Get("skip") == "1" {
		AuthSetFirstRunDismissed(AuthDB(), AuthCurrentUser(r), true)
		http.Redirect(w, r, a.WebPath()+"/", http.StatusFound)
		return
	}
	if !systemNeedsSetup(a.db) {
		http.Redirect(w, r, a.WebPath()+"/", http.StatusFound)
		return
	}
	a.renderSetupWizard(w, r)
}

// renderSetupWizard serves the guided form.
func (a *AdminApp) renderSetupWizard(w http.ResponseWriter, r *http.Request) {
	page := a.setupWizardPage()
	page.ServeHTTP(w, r)
}

// setupWizardPage builds the three-step guided form. Steps mirror the
// questions in the order an operator can answer them: what they are connecting
// to, which model, then a live check before committing. Split from the handler
// so the wiring (endpoints, field names, gating values) is assertable without
// standing up a server.
func (a *AdminApp) setupWizardPage() ui.Page {
	providerStep := ui.FormStep{
		Title: "Provider",
		Intro: "Pick where the model runs. A local provider (Ollama or llama.cpp) needs an endpoint and no key; a hosted one needs an API key. You can add a second, higher-precision model later — this sets the one most work runs on.",
		Fields: []ui.FormField{
			{Field: "provider", Label: "Provider", Type: "select", Required: true,
				Options: []ui.SelectOption{
					{Value: "ollama", Label: "Ollama (local)"},
					{Value: "llama.cpp", Label: "llama.cpp (local)"},
					{Value: "anthropic", Label: "Anthropic"},
					{Value: "openai", Label: "OpenAI"},
					{Value: "gemini", Label: "Gemini"},
					{Value: "bedrock", Label: "AWS Bedrock"},
				}},
			{Field: "endpoint", Label: "Endpoint", Type: "text", ShowWhen: "provider:ollama|llama.cpp",
				Placeholder: "http://localhost:11434",
				Help:        "Where the local server is listening.",
				Presets: []ui.FieldPreset{
					{Label: "Ollama", Value: "http://localhost:11434"},
					{Label: "llama.cpp", Value: "http://localhost:8080/v1"}}},
			{Field: "api_key", Label: "API key", Type: "password",
				ShowWhen: "provider:anthropic|openai|gemini|bedrock",
				Help:     "Stored encrypted. On AWS Bedrock this is optional — leave it blank to sign with your AWS credentials (including SSO) instead of a bearer token."},
			{Field: "aws_region", Label: "AWS region", Type: "text", ShowWhen: "provider:bedrock",
				Placeholder: "us-east-1",
				Help:        "Blank uses $AWS_REGION, then us-east-1. Not every region AWS lists for Bedrock has a Messages-API endpoint — us-west-1 notably does not, use us-west-2.",
				Presets:     bedrockRegionPresets()},
			{Field: "bedrock_api", Label: "Bedrock API", Type: "select", ShowWhen: "provider:bedrock",
				Options: []ui.SelectOption{
					{Value: "", Label: "Messages API (bedrock-mantle)"},
					{Value: "invoke", Label: "InvokeModel (bedrock-runtime)"}},
				Help: "Which Bedrock API your AWS role is allowed to call. Messages API needs bedrock-mantle:CreateInference; InvokeModel needs bedrock:InvokeModel and is what most AI-tooling permission sets grant. A 403 on CreateInference means switch to InvokeModel. Both stream."},
			{Field: "aws_profile", Label: "AWS profile", Type: "text", ShowWhen: "provider:bedrock",
				Placeholder: "(default)",
				Help:        "Which set of AWS credentials to use. Blank uses $AWS_PROFILE. Credentials are never stored here — they come from your environment or ~/.aws, so for SSO run `aws sso login` on this machine first."},
		},
	}

	modelStep := ui.FormStep{
		Title: "Model",
		Intro: "Name the model to use. Leave it blank to take the provider's default, which is a reasonable starting point if you are not sure.",
		Fields: []ui.FormField{
			{Field: "model", Label: "Model", Type: "text",
				Placeholder: "e.g. qwen3.6-27b, claude-sonnet-5, us.anthropic.claude-opus-4-8",
				Help:        "For a local provider this is the name the server knows it by (`ollama list`). On Bedrock: Bare names get the `anthropic.` prefix added (anthropic.claude-opus-4-8). Many AWS accounts require a region-prefixed inference profile instead — e.g. us.anthropic.claude-opus-4-8 — and a bare id is denied. Anything already containing `anthropic.` is sent as typed."},
		},
	}

	checkStep := ui.FormStep{
		Title: "Check",
		Intro: "Press Test connectivity to send one short prompt with the settings above. This is a real request: it proves the endpoint answers, the credentials work, and the model name is valid. Nothing has been saved yet — Finish setup writes it.",
	}

	page := ui.Page{
		Title:     "Welcome to gohort",
		ShowTitle: true,
		MaxWidth:  "760px",
		Sections: []ui.Section{{
			Title:    "Connect a language model",
			Subtitle: "Nothing works without one, so it is the only thing first-run setup asks for. Everything else has a working default you can tune afterward.",
			Body: ui.FormPanel{
				// Relative: resolved by the browser against /admin/setup, so
				// they land on /admin/api/... regardless of where the app is
				// mounted. Only the server-side redirects need WebPath().
				Source:         "api/worker-llm",
				PostURL:        "api/worker-llm",
				Method:         "POST",
				TestURL:        "api/worker-llm/test",
				TestLabel:      "Test connectivity",
				SubmitLabel:    "Finish setup",
				RedirectURL:    ".",
				RedirectTarget: "_self",
				Steps:          []ui.FormStep{providerStep, modelStep, checkStep},
			},
		}},
		ExtraHeadHTML: setupSkipLinkHTML(),
	}
	return page
}

// setupSkipLinkHTML adds a "Skip for now" escape hatch beside the page title.
// An operator who is only here to add a user should not be trapped in the
// wizard. Same deferred-mount pattern the orchestrate wizard uses: the header
// renders asynchronously, so poll for the title before inserting.
//
// No backticks — this lives in a Go raw string.
func setupSkipLinkHTML() string {
	return `<style>
#setup-skip-link{font-size:0.78rem;color:var(--text-mute);text-decoration:none;align-self:center;margin-left:.7rem;border:1px solid var(--border);border-radius:999px;padding:.2rem .65rem;white-space:nowrap}
#setup-skip-link:hover{color:var(--accent);border-color:var(--accent)}
</style>
<script>
(function(){
  function mount(){
    var h = document.querySelector('.page-title, h1');
    if (!h) { requestAnimationFrame(mount); return; }
    if (document.getElementById('setup-skip-link')) return;
    var a = document.createElement('a');
    a.id = 'setup-skip-link';
    a.href = '?skip=1';
    a.textContent = 'Skip for now';
    a.title = 'Configure a model later under Admin, LLMs';
    h.appendChild(a);
  }
  requestAnimationFrame(mount);
})();
</script>`
}

// handleWorkerLLMTest runs a live one-shot chat against the posted (unsaved)
// settings and reports whether the provider answered.
//
// It builds the LLM from the request body rather than from stored config on
// purpose: the whole point is to validate credentials BEFORE writing them, so
// an operator never saves a key that was going to fail. A blank api_key falls
// back to the stored one, matching the form's "blank means keep the existing
// key" convention.
func (a *AdminApp) handleWorkerLLMTest(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		APIKey     string `json:"api_key"`
		Endpoint   string `json:"endpoint"`
		AWSRegion  string `json:"aws_region"`
		AWSProfile string `json:"aws_profile"`
		BedrockAPI string `json:"bedrock_api"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeTestResult(w, false, "", "invalid request body")
		return
	}
	if strings.TrimSpace(req.Provider) == "" {
		writeTestResult(w, false, "", "pick a provider first")
		return
	}
	if req.APIKey == "" && a.db != nil {
		a.db.Get(LLMTable, "api_key", &req.APIKey)
	}
	// Bedrock: check the endpoint exists before trying to talk to it. Several
	// regions AWS documents for Bedrock have no Messages-API host, and without
	// this the operator gets a bare DNS error that reads like a network fault.
	credNote := ""
	if req.Provider == "bedrock" && req.BedrockAPI != "invoke" {
		if err := CheckBedrockEndpoint(req.AWSRegion); err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		// Resolve the identity up front and report it either way. A bare
		// "denied" is unactionable: the expensive question in a Bedrock setup
		// is always WHICH credential got denied, and the AWS error names a
		// role that may appear nowhere in gohort's configuration.
		if req.APIKey == "" {
			src, err := DescribeBedrockCredentials(req.AWSProfile)
			if err != nil {
				writeTestResult(w, false, "", err.Error())
				return
			}
			credNote = " (signed with credentials from " + src + ")"
		} else {
			credNote = " (bearer token)"
		}
	}

	llm, err := NewLLMFromConfig(LLMProviderConfig{
		Provider:   req.Provider,
		Model:      req.Model,
		APIKey:     req.APIKey,
		Endpoint:   req.Endpoint,
		Region:     req.AWSRegion,
		Profile:    req.AWSProfile,
		BedrockAPI: req.BedrockAPI,
		// Short deadlines: this is an interactive check, and a provider that
		// needs more than this to answer "ok" is not going to make a usable
		// chat surface anyway.
		ConnectTimeout: 10 * time.Second,
		RequestTimeout: 30 * time.Second,
	})
	if err != nil {
		writeTestResult(w, false, "", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	started := time.Now()
	resp, err := llm.Chat(ctx,
		[]Message{{Role: "user", Content: "Reply with the single word: ok"}},
		WithMaxTokens(16), WithThink(false))
	if err != nil {
		writeTestResult(w, false, "", err.Error()+credNote)
		return
	}
	took := time.Since(started).Round(time.Millisecond)

	model := resp.Model
	if model == "" {
		model = req.Model
	}
	if model == "" {
		model = "the provider default"
	}
	writeTestResult(w, true, "Connected — "+model+" answered in "+took.String()+"."+credNote, "")
}

// bedrockRegionPresets lists regions that actually have a Messages-API
// endpoint, verified by resolving each host. AWS's published Bedrock region
// table is broader than this: it describes where the service is reachable via
// routing, and several of those regions have no endpoint to point a client at.
// Offered as presets rather than a closed select so a region AWS adds later
// can still be typed in.
func bedrockRegionPresets() []ui.FieldPreset {
	return []ui.FieldPreset{
		{Label: "us-east-1", Value: "us-east-1", Hint: "N. Virginia"},
		{Label: "us-east-2", Value: "us-east-2", Hint: "Ohio"},
		{Label: "us-west-2", Value: "us-west-2", Hint: "Oregon — use this one, not us-west-1"},
		{Label: "eu-west-1", Value: "eu-west-1", Hint: "Ireland"},
		{Label: "eu-central-1", Value: "eu-central-1", Hint: "Frankfurt"},
		{Label: "ap-northeast-1", Value: "ap-northeast-1", Hint: "Tokyo"},
		{Label: "ap-southeast-2", Value: "ap-southeast-2", Hint: "Sydney"},
	}
}
