// New Agent Wizard — the guided create flow at /agent/wizard.
//
// Where the full editor (page_agent.go) shows every field of the record,
// the wizard asks plain-language questions in steps (what kind of agent,
// what should it do, optional tuning) and the server drafts the working
// prompt from that brief via the worker LLM at create time. The user
// lands in the full editor afterward to review and refine the draft.
// Built on the generic ui.FormPanel Steps primitive — nothing here leaks
// into core/ui.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// wizard_kinds maps the wizard's "agent type" answer to the same
// character-defining defaults the editor's Agent-type presets stamp
// (agentTypeTemplates): Cortex + memory mode, Fleet off, recall hints on.
// Two types only — the one identity question is "for people, or for
// other agents?"; standing mind and memory behavior are DIALS the
// wizard's Memory step (and the editor) can override, not species.
// label doubles as the select option text and the type line in the
// prompt-drafting brief.
var wizard_kinds = map[string]struct {
	label       string
	cortex      bool
	memory_mode string
}{
	"assistant":  {"Assistant — a conversational agent that works with people", true, "chatbot"},
	"specialist": {"Specialist — a focused worker other agents dispatch to", false, "agent"},
}

// renderAgentWizard shows the guided New Agent flow: a Steps FormPanel
// whose final submit POSTs the brief to /api/agents/wizard, which
// drafts the prompt, creates the agent, and redirects into the full
// editor on the new record.
//
// Two query-param variants layer on top of the generic flow:
//   - ?kind=<type> pre-selects the agent type (the Type step's select
//     collapses to a hidden field) — used by first-run and any future
//     "create a <type>" deep link.
//   - ?first_run=1 marks the onboarding pass for a user who owns no
//     agents yet (page_chat redirects them here): assistant-flavored
//     copy, a skip affordance when there's somewhere usable to skip TO,
//     and completion lands in a conversation with the new assistant
//     instead of the editor.
func (T *OrchestrateApp) renderAgentWizard(w http.ResponseWriter, r *http.Request, user string) {
	kindPreset := ""
	if k := r.URL.Query().Get("kind"); k != "" {
		if _, ok := wizard_kinds[k]; ok {
			kindPreset = k
		}
	}
	firstRun := r.URL.Query().Get("first_run") == "1"
	assistantRun := firstRun && kindPreset == "assistant"

	typeStep := ui.FormStep{
		Title: "Type",
		Intro: "Pick what kind of agent this is and give it a name. The type sets sensible defaults (memory, standing mind) — everything stays adjustable in the editor afterward.",
		Fields: []ui.FormField{
			{Field: "agent_kind", Type: "select", Label: "Agent type", Required: true,
				Options: []ui.SelectOption{
					{Value: "", Label: "— choose an agent type —"},
					{Value: "assistant", Label: wizard_kinds["assistant"].label},
					{Value: "specialist", Label: wizard_kinds["specialist"].label},
				},
				Help: "One question: who is it for? An Assistant talks with people — you, a room, a contact — keeps a standing mind, and remembers who it talks to. A Specialist does focused work other agents dispatch to it, and remembers lessons rather than people. The Memory step adjusts either."},
			{Field: "name", Type: "text", Label: "Name", Required: true,
				Placeholder: "Research helper", SuggestURL: "../api/agents/suggest"},
			{Field: "description", Type: "text", Label: "Description",
				Placeholder: "One sentence on what this agent is for. Leave blank to have it written for you.",
				SuggestURL:  "../api/agents/suggest"},
		},
	}
	if kindPreset != "" {
		// Type already chosen by the link — collapse the select to a
		// hidden carrier and retitle the step around naming.
		typeStep.Title = "Name"
		typeStep.Intro = "Give it a name. Everything stays adjustable in the editor afterward."
		typeStep.Fields = append(
			[]ui.FormField{{Field: "agent_kind", Type: "hidden", Default: kindPreset}},
			typeStep.Fields[1:]...)
		if assistantRun {
			typeStep.Intro = "Let's set up your personal assistant. Give it a name — you can rename it any time."
			typeStep.Fields[1].Placeholder = "e.g. Jarvis, Ada, Scout"
		}
	}

	purposeStep := ui.FormStep{
		Title: "Purpose",
		Intro: "Describe the job in plain language. You are not writing the agent's prompt — this is the brief it gets drafted from, so concrete beats polished.",
		Fields: []ui.FormField{
			{Field: "purpose", Type: "textarea", Label: "What should it do?", Rows: 4, Required: true,
				Placeholder: "e.g. Answer questions about our internal deployment runbooks: find the relevant doc, quote the exact steps, and flag anything out of date."},
			{Field: "example_tasks", Type: "textarea", Label: "Example requests", Rows: 3,
				Help:        "A few real asks users will make of it, one per line. These sharpen the draft a lot.",
				Placeholder: "How do I roll back the api tier?\nWhich runbooks mention the standby database?"},
			{Field: "style", Type: "text", Label: "Tone & style (optional)",
				Placeholder: "e.g. terse and technical / warm and plain-spoken"},
		},
	}
	if assistantRun {
		purposeStep.Intro = "What do you want help with day to day? Plain language is perfect — this becomes the brief your assistant's working prompt is drafted from."
		purposeStep.Fields[0].Label = "What should it help you with?"
		purposeStep.Fields[0].Placeholder = "e.g. Keep track of my projects and deadlines, draft and tidy up emails, dig up answers when I ask, and remind me about the things I tell it to remember."
		purposeStep.Fields[1].Placeholder = "What's on my plate this week?\nDraft a reply to this email.\nRemind me to call the vet tomorrow."
	}

	// The personalization step — an assistant that knows you from message
	// one. ShowWhen keys off agent_kind, so in the generic wizard it
	// appears live when the user picks Assistant in step 1, and with the
	// ?kind=assistant preset it's simply always there (the hidden field
	// seeds the form state at render).
	aboutStep := ui.FormStep{
		Title:    "About you",
		ShowWhen: "agent_kind:assistant",
		Intro:    "Optional, and worth it: what your assistant knows about you from the start. It keeps these as its working notes — view or edit them any time.",
		Fields: []ui.FormField{
			{Field: "call_you", Type: "text", Label: "What should it call you?",
				Placeholder: "e.g. Craig / boss / Dr. Lee"},
			{Field: "about_you", Type: "textarea", Label: "Anything it should know about you?", Rows: 3,
				Placeholder: "Working hours, current projects, people you mention often, preferences…"},
		},
	}

	// The Memory step surfaces the dials the two-type split stopped
	// encoding: memory behavior and the standing mind. Both selects lead
	// with a "default for this type" option so an untouched step stamps
	// the kind's defaults — a select can say "default" honestly where a
	// toggle would have to show some state that may not be true.
	memoryStep := ui.FormStep{
		Title: "Memory",
		Intro: "How it remembers, and whether it keeps a standing mind. The defaults fit most agents — skip this step if unsure; everything stays adjustable in the editor.",
		Fields: []ui.FormField{
			{Field: "memory", Type: "select", Label: "Memory",
				Options: []ui.SelectOption{
					{Value: "", Label: "Default for this type — Assistant: personalized, Specialist: lessons only"},
					{Value: "personalized", Label: "Personalized — remembers the people it talks to, plus lessons"},
					{Value: "lessons", Label: "Lessons only — remembers what works, not who"},
					{Value: "none", Label: "None — starts fresh every conversation"},
				},
				Help: "Personalized stores facts about people attributed by name (\"Dana prefers texts before 8pm\") alongside general lessons — pick Lessons only for an agent shared across unrelated groups, so one room's personal details never surface in another. None turns off remembering across sessions entirely; uploaded knowledge and working notes still apply."},
			{Field: "cortex", Type: "select", Label: "Standing mind",
				Options: []ui.SelectOption{
					{Value: "", Label: "Default for this type — Assistant: on, Specialist: off"},
					{Value: "on", Label: "On — keep a persistent home thread"},
					{Value: "off", Label: "Off — ordinary sessions only"},
				},
				Help: "A standing mind is the agent's persistent home thread (the 🧠 row pinned in its rail) where schedule reports and monitor wakes land, kept bounded by a rolling summary. Turn it off for a plain back-and-forth persona that nothing ever wakes."},
		},
	}

	tuningStep := ui.FormStep{
		Title: "Tuning",
		Intro: "Optional — the defaults are fine. Skip anything you're unsure about; it's all editable later.",
		Fields: []ui.FormField{
			{Field: "triggers", Type: "tags", Label: "Dispatch triggers",
				Help: "Patterns that nudge the host to route a matching message to THIS agent first — case-insensitive substrings of the message (a pattern with * or ? matches attachment filenames instead). Use specific phrases its questions actually contain; loose ones over-fire. Empty is fine."},
		},
	}

	createStep := ui.FormStep{
		Title: "Create",
		Intro: "That's everything. Creating the agent drafts its working prompt from your answers on the local model (a few seconds), then opens the full editor so you can review the draft and attach tools, knowledge, and credentials.",
	}
	redirectURL := "{id}"
	if firstRun {
		// Onboarding ends in a conversation, not a config screen — the
		// editor stays a click away via the picker. Landing on the chat
		// with ?agent= also writes the last-accessed cookie, so the
		// default-agent preference starts working immediately.
		createStep.Intro = "That's everything. Creating it drafts its working prompt from your answers on the local model (a few seconds), then drops you straight into your first conversation with it."
		redirectURL = "../?agent={id}"
	}

	steps := []ui.FormStep{typeStep, purposeStep, aboutStep, memoryStep, tuningStep, createStep}

	head := wizardAdvancedLinkHTML()
	title := "New agent"
	sectionTitle := "Guided setup"
	sectionSub := "Answer a few questions and the agent's working prompt is drafted for you. Nothing is created until the last step."
	if firstRun {
		title = "Welcome"
		sectionTitle = "Create your personal assistant"
		sectionSub = "You don't have any agents of your own yet. Answer a few questions and your assistant is drafted for you — nothing is created until the last step."
		head += wizardSkipLinkHTML(T.hasSharedReachableAgents(r, user))
	}

	page := ui.Page{
		Title:     title,
		ShowTitle: true,
		BackURL:   "..",
		MaxWidth:  "760px",
		Sections: []ui.Section{{
			Title:    sectionTitle,
			Subtitle: sectionSub,
			Body: ui.FormPanel{
				PostURL:        "../api/agents/wizard",
				Method:         "POST",
				SubmitLabel:    "Create agent",
				RedirectURL:    redirectURL,
				RedirectTarget: "_self",
				Steps:          steps,
			},
		}},
		ExtraHeadHTML: head,
	}
	page.ServeHTTP(w, r)
}

// hasSharedReachableAgents reports whether any OTHER user's agent is
// reachable by this user on the /agents surface — peer-shared to them
// (AllowedUsers) or published and granted. Drives the first-run skip
// affordance: with nothing shared, there's nowhere useful to skip to.
func (T *OrchestrateApp) hasSharedReachableAgents(r *http.Request, user string) bool {
	for _, e := range T.ListExposedAgents() {
		if e.Owner == user {
			continue
		}
		if containsString(e.AllowedUsers, user) ||
			(e.Exposed && UserHasAppAccess(r, "/agents/"+e.Slug)) {
			return true
		}
	}
	return false
}

// needsFirstRunSetup reports whether the user owns no top-level agents
// of their own — framework seeds and sub-agents don't count. Such a
// user gets walked into the personal-assistant wizard by page_chat
// instead of landing on a picker of retiring seeds.
func needsFirstRunSetup(agents []AgentRecord, user string) bool {
	for _, a := range agents {
		if a.Owner == user && a.OwnedBy == "" && !isSeedID(a.ID) {
			return false
		}
	}
	return true
}

// wizardAdvancedLinkHTML injects an "Advanced editor" escape hatch next
// to the page title — same rAF-poll mount pattern as the lock icon
// (agentLockIconHTML): the header renders asynchronously, so wait for
// the title before inserting. Relative href resolves /agent/wizard →
// /agent/new. No backticks (lives in a Go raw string); plain quotes only.
func wizardAdvancedLinkHTML() string {
	return `<style>
#agent-adv-link{font-size:0.78rem;color:var(--text-mute);text-decoration:none;align-self:center;margin-left:.7rem;border:1px solid var(--border);border-radius:999px;padding:.2rem .65rem;white-space:nowrap}
#agent-adv-link:hover{color:var(--accent);border-color:var(--accent)}
</style>
<script>
(function(){
  var a=document.createElement('a');
  a.id='agent-adv-link'; a.href='new'; a.textContent='Advanced editor';
  a.title='Skip the wizard and fill in the full agent form yourself';
  var tries=0;
  function mount(){
    if(document.getElementById('agent-adv-link')) return;
    var title=document.querySelector('.ui-page-title');
    if(title){ title.insertAdjacentElement('afterend', a); return; }
    if(tries++ < 120) requestAnimationFrame(mount);
  }
  mount();
})();
</script>`
}

// wizardSkipLinkHTML injects the first-run "Skip for now" pill next to
// the page title (same rAF-poll mount as the Advanced-editor pill).
// Clicking it hits the chat surface with ?skip_first_run=1, which
// records the dismissal on the user record and forwards to the /agents
// directory when agents are shared with them (that's what the label
// promises), else back to the chat surface. No backticks (Go raw
// string); plain quotes only.
func wizardSkipLinkHTML(hasShared bool) string {
	label := "Skip for now"
	title := "Skip the guided setup — you can come back via New agent"
	if hasShared {
		label = "Skip — use agents shared with you"
		title = "Skip the guided setup and open the agents other users shared with you"
	}
	return fmt.Sprintf(`<style>
#agent-skip-link{font-size:0.78rem;color:var(--accent);text-decoration:none;align-self:center;margin-left:.55rem;border:1px solid var(--accent);border-radius:999px;padding:.2rem .65rem;white-space:nowrap;opacity:.9}
#agent-skip-link:hover{opacity:1}
</style>
<script>
(function(){
  var a=document.createElement('a');
  a.id='agent-skip-link'; a.href='../?skip_first_run=1';
  a.textContent=%q; a.title=%q;
  var tries=0;
  function mount(){
    if(document.getElementById('agent-skip-link')) return;
    var title=document.querySelector('.ui-page-title');
    if(title){ title.insertAdjacentElement('afterend', a); return; }
    if(tries++ < 120) requestAnimationFrame(mount);
  }
  mount();
})();
</script>`, label, title)
}

// handleAgentWizard creates an agent from the wizard's brief:
// POST /api/agents/wizard {agent_kind, name, description, purpose,
// example_tasks, style, triggers, tag_name}. The orchestrator prompt
// (and the description, when left blank) is drafted from the brief via
// the same worker-LLM path as the editor's ✨ Suggest, then the record
// saves through the normal saveAgent path and the new record echoes
// back so the form can redirect to agent/{id}.
func (T *OrchestrateApp) handleAgentWizard(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req wizardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Purpose = strings.TrimSpace(req.Purpose)
	kind, kindOK := wizard_kinds[req.Kind]
	if !kindOK || req.Name == "" || req.Purpose == "" {
		http.Error(w, "agent type, name, and purpose are required", http.StatusBadRequest)
		return
	}

	rec := AgentRecord{
		Owner:       user,
		Name:        req.Name,
		Description: strings.TrimSpace(req.Description),
		Triggers:    req.Triggers,
		Cortex:      kind.cortex,
		MemoryMode:  kind.memory_mode,
		RecallHints: true,
	}
	if !applyWizardMemory(&rec, req) {
		http.Error(w, "unknown memory setting", http.StatusBadRequest)
		return
	}
	// Assistant personalization ("About you" step) lands as the agent's
	// initial Working notes, so it knows the user from message one.
	if req.Kind == "assistant" {
		if notes := wizardSeedNotes(req); notes != "" {
			rec.EnableNotes = true
			rec.SeedNotes = notes
		}
	}

	// Draft the persona from the brief. Two sequential worker calls at
	// most (prompt, then description when blank), inside one deadline.
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	brief := wizardBrief(kind.label, req)
	record := map[string]any{"name": rec.Name, "description": rec.Description}
	rec.OrchestratorPrompt = T.wizardDraftField(ctx, "orchestrator_prompt", brief, record)
	if rec.OrchestratorPrompt == "" {
		rec.OrchestratorPrompt = wizardFallbackPrompt(rec.Name, req.Purpose, req.Style)
	}
	if rec.Description == "" {
		record["orchestrator_prompt"] = rec.OrchestratorPrompt
		rec.Description = T.wizardDraftField(ctx, "description", brief, record)
	}
	if rec.Description == "" {
		rec.Description = wizardFallbackDescription(req.Purpose)
	}

	saved, err := saveAgent(udb, rec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

// wizardRequest is the wizard's create brief — the POST body of
// /api/agents/wizard, field names matching the wizard form's inputs.
type wizardRequest struct {
	Kind        string   `json:"agent_kind"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Purpose     string   `json:"purpose"`
	Examples    string   `json:"example_tasks"`
	Style       string   `json:"style"`
	Triggers    []string `json:"triggers"`
	// Memory-step dials; "" = the type's default.
	// Memory: "personalized" | "lessons" | "none".
	// Cortex: "on" | "off".
	Memory string `json:"memory"`
	Cortex string `json:"cortex"`
	// About-you personalization (assistant kind only): folded into the
	// drafting brief and seeded as the agent's initial Working notes.
	CallYou  string `json:"call_you"`
	AboutYou string `json:"about_you"`
}

// applyWizardMemory maps the Memory-step choices onto the record, over
// the type defaults already stamped. "" keeps the default; "none"
// disables both memory layers (Explicit facts + Reference recall) while
// leaving knowledge and working notes intact. Returns false on a value
// the wizard never offers.
func applyWizardMemory(rec *AgentRecord, req wizardRequest) bool {
	switch req.Memory {
	case "":
	case "personalized":
		rec.MemoryMode = "chatbot"
	case "lessons":
		rec.MemoryMode = "agent"
	case "none":
		rec.DisableExplicit = true
		rec.DisableInferred = true
	default:
		return false
	}
	switch req.Cortex {
	case "":
	case "on":
		rec.Cortex = true
	case "off":
		rec.Cortex = false
	default:
		return false
	}
	return true
}

// wizardBrief packs the wizard answers into the hint the suggest
// prompt-builder folds in under "User's guidance" — the same channel a
// user typing a hint into the ✨ Suggest dialog uses, so drafting
// quality tracks the editor's without a second prompt surface.
func wizardBrief(kindLabel string, req wizardRequest) string {
	var b strings.Builder
	b.WriteString("Draft from this creation brief.\n")
	b.WriteString("Agent type: " + kindLabel + "\n")
	b.WriteString("Purpose: " + req.Purpose + "\n")
	if e := strings.TrimSpace(req.Examples); e != "" {
		b.WriteString("Example requests it will handle:\n" + e + "\n")
	}
	if s := strings.TrimSpace(req.Style); s != "" {
		b.WriteString("Tone & style: " + s + "\n")
	}
	if c := strings.TrimSpace(req.CallYou); c != "" {
		b.WriteString("The agent should address its user as: " + c + "\n")
	}
	if a := strings.TrimSpace(req.AboutYou); a != "" {
		b.WriteString("About the user it serves: " + a + "\n")
	}
	return b.String()
}

// wizardSeedNotes composes the assistant's initial Working-notes block
// from the About-you answers. Empty when the user skipped both fields.
func wizardSeedNotes(req wizardRequest) string {
	var lines []string
	if c := strings.TrimSpace(req.CallYou); c != "" {
		lines = append(lines, "The user prefers to be called: "+c)
	}
	if a := strings.TrimSpace(req.AboutYou); a != "" {
		lines = append(lines, "About the user: "+a)
	}
	return strings.Join(lines, "\n")
}

// wizardDraftField runs one suggest-style worker call for a field and
// returns the cleaned value, or "" on any failure (no LLM configured,
// timeout, empty reply) so callers fall back to a static compose.
func (T *OrchestrateApp) wizardDraftField(ctx context.Context, field, hint string, record map[string]any) string {
	if T.LLM == nil {
		return ""
	}
	prompt := buildSuggestPrompt(field, hint, record)
	resp, err := T.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(suggestSystemPrompt),
		WithRouteKey("app.orchestrate.suggest"),
		WithThink(false),
	)
	if err != nil || resp == nil {
		return ""
	}
	return cleanSuggestion(field, resp.Content)
}

// wizardFallbackPrompt composes a serviceable prompt straight from the
// brief when the LLM draft is unavailable — the agent still works on
// day one and the editor's ✨ Suggest can rewrite it later.
func wizardFallbackPrompt(name, purpose, style string) string {
	var b strings.Builder
	b.WriteString("You are " + name + ". " + purpose)
	if s := strings.TrimSpace(style); s != "" {
		b.WriteString("\n\nTone and style: " + s + ".")
	}
	b.WriteString("\n\nDecompose non-trivial requests into a few concrete steps, brief each step with the specific deliverable and format you need back, and synthesize a direct answer. If the request is ambiguous, ask one clarifying question instead of guessing.")
	return b.String()
}

// wizardFallbackDescription derives the one-line picker description
// from the purpose: first sentence (or line), truncated sanely.
func wizardFallbackDescription(purpose string) string {
	s := purpose
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		s = s[:i+1]
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "\n"))
	if len(s) > 160 {
		s = strings.TrimSpace(s[:157]) + "…"
	}
	return s
}
