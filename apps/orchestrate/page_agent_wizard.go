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
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// assistantShape is the shape an assistant built by this wizard follows: the
// conversational recipe, which ships the framework's own Chat record. Named
// here rather than inlined because it is the one place the wizard reaches into
// the shape library, and a shape that gets renamed should break loudly at this
// line rather than quietly stop being followed.
const assistantShape = "conversational"

// wizard_kinds maps the wizard's "agent type" answer to the same
// character-defining defaults the editor's Agent-type presets stamp
// (agentTypeTemplates): Cortex + memory mode, Fleet off, recall hints on.
// Two types only — the one identity question is "a companion that
// knows its people, or a focused tool for a job?"; standing mind and
// memory behavior are DIALS the wizard's Memory step (and the editor)
// can override, not species. A Specialist isn't only for other agents:
// a user drives one directly too (a research agent with an intake
// form, a report generator) — what defines it is being task-focused,
// so it remembers lessons rather than people. label doubles as the
// select option text and the type line in the prompt-drafting brief.
var wizard_kinds = map[string]struct {
	label       string
	cortex      bool
	memory_mode string
}{
	"assistant":  {"Assistant — a conversational agent that works with people", true, "chatbot"},
	"specialist": {"Specialist — a focused agent for one job, used directly or by dispatch", false, "agent"},
}

// wizard_template is one crafted starting point offered by the wizard's
// "Start from a template" row: a seed record whose value is the TUNING
// (budgets, curated tools, prompt craft) that a from-scratch draft cannot
// reproduce. Picking one clones the full record as the user's own agent and
// collapses the guided questions to just a name.
type wizard_template struct {
	id    string
	label string
	order int
}

// wizardTemplates reads the offered templates off the archetype library: a
// recipe that carries a "template" label is offered, and the record it clones
// is the seed that recipe names.
//
// Derived rather than listed because a template is a SHAPE the user can pick,
// and the shape is already described in one place. As a Go list here, a
// shape's name for users sat a package away from the shape, adding one was a
// code change in a file about form rendering, and nothing tied the offered
// label to the recipe describing the same agent.
//
// Ordered by each recipe's declared order, then by label, so the row is stable
// and a shape that expresses no preference lands alphabetically.
func wizardTemplates() []wizard_template {
	var out []wizard_template
	for _, a := range loadArchetypes() {
		if a.Template == nil {
			continue
		}
		// parseArchetype refuses a template with no seed or no label.
		out = append(out, wizard_template{id: a.Seed(), label: a.Template.Label, order: a.Template.Order})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		return out[i].label < out[j].label
	})
	return out
}

// isWizardTemplate reports whether id is one of the offered templates. This is
// the create endpoint's guard, so a forged POST cannot clone arbitrary seed
// IDs through the wizard path.
func isWizardTemplate(id string) bool {
	for _, t := range wizardTemplates() {
		if t.id == id {
			return true
		}
	}
	return false
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
				Help: "One question: companion or tool? An Assistant talks with people — you, a room, a contact — keeps a standing mind, and remembers who it talks to. A Specialist is built for one job — a research agent with an intake form, a report generator — used by you directly or dispatched to by other agents, and remembers lessons rather than people. The Memory step adjusts either."},
			{Field: "name", Type: "text", Label: "Name", Required: true,
				Placeholder: "Research helper", SuggestURL: "../api/agents/suggest"},
			{Field: "description", Type: "text", Label: "Description",
				Placeholder: "One sentence on what this agent is for. Leave blank to have it written for you.",
				SuggestURL:  "../api/agents/suggest"},
		},
	}
	if kindPreset == "" {
		// "Start from a template" — the archetype axis, deliberately
		// separate from the two identity types: a template is a finished,
		// tuned agent copied whole. Choosing one hides the type select
		// and collapses the guided steps (each gated on "!template")
		// down to Name → Create. Not offered on preset deep links —
		// they've already committed to a build.
		tplOpts := []ui.SelectOption{{Value: "", Label: "No template — guided setup"}}
		for _, t := range wizardTemplates() {
			tplOpts = append(tplOpts, ui.SelectOption{Value: t.id, Label: t.label})
		}
		typeStep.Fields[0].ShowWhen = "!template" // type select is a guided-path question
		typeStep.Fields = append([]ui.FormField{{
			Field: "template", Type: "select", Label: "Start from a template",
			Options: tplOpts,
			Help:    "A template is a finished, tuned agent — its prompt, budgets, and tools copied as your own. Pick one and the guided questions collapse to just a name. Leave on guided setup to build from a brief instead.",
		}}, typeStep.Fields...)
	}
	if kindPreset != "" {
		// Type already chosen by the link — collapse the select to a
		// hidden carrier and retitle the step around naming.
		typeStep.Title = "Name"
		typeStep.Intro = "Give it a name. Everything stays adjustable in the editor afterward."
		// first_run rides along as a hidden carrier so the create endpoint
		// knows this is the front-door agent, not the fourth one someone made.
		firstRunDefault := ""
		if firstRun {
			firstRunDefault = "true"
		}
		typeStep.Fields = append(
			[]ui.FormField{
				{Field: "agent_kind", Type: "hidden", Default: kindPreset},
				{Field: "first_run", Type: "hidden", Default: firstRunDefault},
			},
			typeStep.Fields[1:]...)
		if assistantRun {
			typeStep.Intro = "Let's set up your personal assistant. Give it a name — you can rename it any time."
			typeStep.Fields[1].Placeholder = "e.g. Jarvis, Ada, Scout"
		}
	}

	purposeStep := ui.FormStep{
		Title:    "Purpose",
		ShowWhen: "!template",
		Intro:    "Describe the job in plain language. You are not writing the agent's prompt — this is the brief it gets drafted from, so concrete beats polished.",
		Fields: []ui.FormField{
			// Pick-first, then write. A blank textarea is the hardest question
			// on the form for someone who has not used the thing yet: they do
			// not know what it CAN do, so "describe the job" asks them to
			// invent the product. These are the jobs it is actually good at,
			// and picking two gets a working agent without typing anything.
			{Field: "purpose_picks", Type: "checklist", Label: "What should it help with?",
				ShowWhen: "agent_kind:assistant",
				Options: []ui.SelectOption{
					{Value: "keep track of my projects, deadlines and what I said I would do", Label: "Keep track of my work",
						Help: "Projects, deadlines, follow-ups, and it remembers between conversations."},
					{Value: "look things up on the web and answer with sources I can check", Label: "Look things up",
						Help: "Searches, reads the pages, cites what it used."},
					{Value: "answer questions from documents and files I give it", Label: "Answer from my documents",
						Help: "Upload a corpus; it answers from that rather than from training."},
					{Value: "draft and tidy up my writing: emails, notes, documents", Label: "Draft and edit writing"},
					{Value: "watch things and tell me when they change or go wrong", Label: "Watch things for me",
						Help: "A page, an endpoint, a feed. It stays quiet until something happens."},
					{Value: "run work on a schedule and report back", Label: "Run things on a schedule",
						Help: "Daily summaries, periodic checks, anything on a clock."},
					{Value: "help me think something through before I commit to it", Label: "Think things through with me"},
				}},
			{Field: "purpose", Type: "textarea", Label: "What should it do?", Rows: 4,
				Placeholder: "e.g. Answer questions about our internal deployment runbooks: find the relevant doc, quote the exact steps, and flag anything out of date."},
			{Field: "example_tasks", Type: "textarea", Label: "Example requests", Rows: 3,
				Help:        "A few real asks users will make of it, one per line. These sharpen the draft a lot.",
				Placeholder: "How do I roll back the api tier?\nWhich runbooks mention the standby database?"},
		},
	}
	if assistantRun {
		// Fields: 0 = purpose_picks, 1 = purpose (free text), 2 = example_tasks.
		purposeStep.Fields[1].Label = "Anything else, in your own words?"
		purposeStep.Fields[1].Rows = 3
		purposeStep.Intro = "What do you want help with day to day? Plain language is perfect — this becomes the brief your assistant's working prompt is drafted from."
		purposeStep.Fields[1].Placeholder = "e.g. Keep track of my projects and deadlines, draft and tidy up emails, dig up answers when I ask, and remind me about the things I tell it to remember."
		purposeStep.Fields[2].Placeholder = "What's on my plate this week?\nDraft a reply to this email.\nRemind me to call the vet tomorrow."
	}

	// The personalization step — an assistant that knows you from message
	// one. ShowWhen keys off agent_kind, so in the generic wizard it
	// appears live when the user picks Assistant in step 1, and with the
	// ?kind=assistant preset it's simply always there (the hidden field
	// seeds the form state at render).
	// Personality — first-run assistants only. This is the same `style`
	// field the Purpose step offers every other agent as an optional extra,
	// promoted to a question of its own and asked with real answers, because
	// for the agent someone is going to talk to every day it is not a detail:
	// it is most of what makes the thing feel like theirs rather than like a
	// deployment. Free text still wins if they'd rather write it.
	personaStep := ui.FormStep{
		Title:    "Personality",
		ShowWhen: "!template",
		Intro:    "How should it write, and what should it do when the work goes sideways? This shapes its voice and its judgement, not what it can reach, and all of it is editable later.",
		Fields: []ui.FormField{
			{Field: "style", Type: "select", Label: "Its manner",
				Options: []ui.SelectOption{
					{Value: "warm and plain-spoken, friendly without being chatty", Label: "Warm and plain-spoken"},
					{Value: "terse and technical; assume expertise, skip the preamble", Label: "Terse and technical"},
					{Value: "dry and a little wry, never at the expense of being useful", Label: "Dry, with a sense of humour"},
					{Value: "formal and precise, the register of a written brief", Label: "Formal and precise"},
					{Value: "", Label: "No preference, draft something sensible"},
				}},
			{Field: "traits", Type: "checklist", Label: "How it should handle you",
				Options: []ui.SelectOption{
					{Value: "leads with the answer, then the reasoning", Label: "Answer first, reasoning after"},
					{Value: "pushes back when something looks wrong rather than agreeing", Label: "Pushes back",
						Help: "Says when it thinks you are wrong, instead of going along with it."},
					{Value: "asks before assuming what was meant, rather than guessing", Label: "Asks rather than assumes"},
					{Value: "keeps replies short unless depth is asked for", Label: "Brief by default"},
					{Value: "shows its working and cites what it actually used", Label: "Shows its working"},
					{Value: "says plainly when it does not know, instead of hedging", Label: "Admits what it doesn't know"},
					{Value: "offers the next step without being asked", Label: "Suggests the next move"},
				}},
			// Situations, not adjectives. "Pushes back" is a label someone has
			// to translate; a quoted reply is the thing itself, and the answer
			// they pick IS an example of the voice — which is far better
			// material for the draft than the word "direct".
			{Field: "on_wrong", Type: "select", Label: "You say something it believes is wrong",
				ShowWhen: "agent_kind:assistant",
				Options: []ui.SelectOption{
					{Value: "corrects the user directly and immediately, without softening it", Label: "\"That is not right. It is in /etc, not /usr/local.\""},
					{Value: "raises the doubt as a question and invites the user to check", Label: "\"I might be misreading this. Isn't it /etc?\""},
					{Value: "states the correction plainly but leaves room for the user to know better", Label: "\"Worth checking: I have it as /etc, unless yours is custom.\""},
					{Value: "", Label: "No preference"},
				}},
			{Field: "on_unsure", Type: "select", Label: "It doesn't know the answer",
				ShowWhen: "agent_kind:assistant",
				Options: []ui.SelectOption{
					{Value: "says plainly that it does not know, and offers to find out", Label: "\"I don't know. Want me to look?\""},
					{Value: "gives its best guess, clearly labelled as a guess, with the reasoning", Label: "\"Not certain. Best guess, and here is why.\""},
					{Value: "goes and checks first, and answers once it actually knows", Label: "\"Give me a moment, I will check.\""},
					{Value: "", Label: "No preference"},
				}},
			{Field: "on_vague", Type: "select", Label: "You ask for something big and vague",
				ShowWhen: "agent_kind:assistant",
				Options: []ui.SelectOption{
					{Value: "asks one clarifying question before starting", Label: "\"Before I start: do you mean X or Y?\""},
					{Value: "takes its best shot immediately and shows the assumptions it made", Label: "\"Assuming X, here's a first pass.\""},
					{Value: "breaks the work into steps and asks which one to begin with", Label: "\"That's about four things. Which first?\""},
					{Value: "", Label: "No preference"},
				}},
			{Field: "on_done", Type: "select", Label: "It finishes something you asked for",
				ShowWhen: "agent_kind:assistant",
				Options: []ui.SelectOption{
					{Value: "reports completion and stops", Label: "\"Done.\""},
					{Value: "reports completion and mentions anything it noticed on the way", Label: "\"Done. One thing looked odd while I was in there.\""},
					{Value: "reports completion and proposes the next step", Label: "\"Done. Want me to do the follow-up too?\""},
					{Value: "", Label: "No preference"},
				}},
			// The specialist's counterparts. Same idea, different situations:
			// a specialist is usually answering a dispatch rather than talking
			// to a person, so what defines it is not how it addresses you but
			// what it does when the work goes sideways. These map onto the
			// persona outline's "Failure modes" section, which that outline
			// calls its highest-value part and which is exactly where a
			// specialist earns its keep.
			{Field: "on_nothing", Type: "select", Label: "It finds nothing",
				ShowWhen: "agent_kind:specialist",
				Options: []ui.SelectOption{
					{Value: "reports plainly that it found nothing, and says where it looked", Label: "\"Nothing found. Here is where I looked.\""},
					{Value: "reports nothing found, and proposes the next place worth trying", Label: "\"Nothing there. Worth trying X next?\""},
					{Value: "widens the search once on its own before reporting an empty result", Label: "Tries a wider search first, then reports"},
					{Value: "", Label: "No preference"},
				}},
			{Field: "on_conflict", Type: "select", Label: "Its sources disagree",
				ShowWhen: "agent_kind:specialist",
				Options: []ui.SelectOption{
					{Value: "reports the disagreement and cites both sides rather than picking one", Label: "\"Two sources, two answers. Both cited.\""},
					{Value: "picks the more authoritative source, says which and why", Label: "\"Going with the vendor doc over the blog, because…\""},
					{Value: "reports the disagreement and asks which source to trust", Label: "\"These conflict. Which do you trust?\""},
					{Value: "", Label: "No preference"},
				}},
			{Field: "on_outofreach", Type: "select", Label: "The request needs something it cannot reach",
				ShowWhen: "agent_kind:specialist",
				Options: []ui.SelectOption{
					{Value: "says exactly what it would need and stops, rather than approximating", Label: "\"I would need access to X. Stopping here.\""},
					{Value: "answers the part it can reach and names the part it cannot", Label: "\"Here is the half I can see. The rest needs X.\""},
					{Value: "reasons from what it does have, labelling clearly that it is inference", Label: "\"Can't check directly. Inferring from Y:\""},
					{Value: "", Label: "No preference"},
				}},
			{Field: "on_partial", Type: "select", Label: "It is only half sure",
				ShowWhen: "agent_kind:specialist",
				Options: []ui.SelectOption{
					{Value: "gives the answer with an explicit confidence note attached", Label: "\"Likely X, though I am not certain.\""},
					{Value: "gives only what it can stand behind and omits the rest", Label: "Says only the part it can defend"},
					{Value: "does one more check before answering at all", Label: "Checks once more, then answers"},
					{Value: "", Label: "No preference"},
				}},
			{Field: "style_notes", Type: "textarea", Label: "Anything else about how it should behave?", Rows: 3,
				Help:        "Habits, not capabilities. What it should always do, or never do, in how it responds to you.",
				Placeholder: "e.g. Ask before assuming what I meant.\nLead with the answer, then the reasoning.\nNever apologise for things that aren't its fault."},
		},
	}

	if assistantRun {
		personaStep.Intro = "This is the agent you will speak to every day, so it is worth a minute.\n\nPick the answers you would actually want to hear. There are no wrong ones — they shape how it talks to you, not what it can do, and all of it is editable the moment you change your mind."
	}

	aboutStep := ui.FormStep{
		Title:    "About you",
		ShowWhen: "!template;agent_kind:assistant",
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
		Title:    "Memory",
		ShowWhen: "!template",
		Intro:    "How it remembers, and whether it keeps a standing mind. The defaults fit most agents — skip this step if unsure; everything stays adjustable in the editor.",
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
		Title:    "Tuning",
		ShowWhen: "!template",
		Intro:    "Optional — the defaults are fine. Skip anything you're unsure about; it's all editable later.",
		Fields: []ui.FormField{
			{Field: "triggers", Type: "tags", Label: "Dispatch triggers",
				Help: "Patterns that nudge the host to route a matching message to THIS agent first — case-insensitive substrings of the message (a pattern with * or ? matches attachment filenames instead). Use specific phrases its questions actually contain; loose ones over-fire. Empty is fine."},
		},
	}

	createStep := ui.FormStep{
		Title:    "Create",
		ShowWhen: "!template",
		Intro:    "That's everything. Creating the agent drafts its working prompt from your answers on the local model (a few seconds), then opens the full editor so you can review the draft and attach tools, knowledge, and credentials.",
	}
	// Template path gets its own Create step (the guided one is gated
	// off) so the copy matches what actually happens: a clone, not a
	// draft. Whichever of the two is the last VISIBLE step carries the
	// submit button.
	createFromTemplateStep := ui.FormStep{
		Title:    "Create",
		ShowWhen: "template",
		Intro:    "Creating copies the template — its prompt, budgets, and tools — as your own agent under the name you chose, then opens the editor to customize it.",
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

	steps := []ui.FormStep{typeStep, purposeStep, personaStep, aboutStep, memoryStep, tuningStep, createStep, createFromTemplateStep}

	if firstRun {
		// A different flow, not the same one retitled. Somebody meeting this
		// for the first time is not configuring an agent — they are deciding
		// who their agent IS. So: say what this is before asking anything,
		// ask about character before capability, and leave the name until
		// last, when there is something to suggest a name FROM. The generic
		// wizard keeps its own order, where naming first is right because the
		// user already knows what they came to build.
		welcome := ui.FormStep{
			Title: "Welcome",
			Intro: "You are about to create your agent: the one you will actually talk to.\n\n" +
				"It keeps its own memory of you and your work, and it can hand jobs to specialist agents you add later: a researcher, a watcher, something that answers from your own documents. You talk to one thing; it decides who does the work.\n\n" +
				"The next few questions are about who it is rather than what it does — how it talks to you, and how it handles you when you're wrong. None of it is permanent; all of it is editable the moment you change your mind.",
			Fields: []ui.FormField{
				{Field: "agent_kind", Type: "hidden", Default: "assistant"},
				{Field: "first_run", Type: "hidden", Default: "true"},
			},
		}
		// Naming last: the suggest endpoint can only propose something good
		// once it has the character and the job to go on. Asked first — which
		// is where it used to be — it suggests into a vacuum.
		nameStep := ui.FormStep{
			Title: "Name",
			Intro: "Last thing. Give it a name, or take one of the suggestions, which are drawn from everything you just said.",
			Fields: []ui.FormField{
				{Field: "name", Type: "text", Label: "Name", Required: true,
					Placeholder: "e.g. Jarvis, Ada, Scout", SuggestURL: "../api/agents/suggest",
					SuggestOnOpen: true},
				{Field: "description", Type: "text", Label: "One line about it (optional)",
					Placeholder: "Leave blank and it will write its own.",
					SuggestURL:  "../api/agents/suggest"},
			},
		}
		steps = []ui.FormStep{welcome, personaStep, purposeStep, aboutStep, nameStep, createStep}
	}

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

// wizardBaseRecord is what a wizard-built agent starts as, before the answers
// and the drafted persona go on top.
//
// An assistant built here IS the conversational shape wearing a persona
// drafted from this person's answers, so it starts from that shape's
// record and FOLLOWS it. Two things come of that.
//
// It arrives complete. Built from an empty struct, a wizard assistant got
// no budgets at all and fell back to the framework floor of five worker
// rounds, while the shape it obviously was carries eighteen precisely
// because inline multi-tool turns ("compare these three products") iterate
// across rounds before replying. Same for the per-turn Private toggle and
// the plan-first discipline: settings nobody would think to ask a new user
// about, which the shape already answers.
//
// And it keeps arriving complete. The persona, the name and whatever the
// wizard's questions decided are this agent's own forever; everything else
// tracks, so a framework improvement reaches the agent a person talks to
// every day rather than stopping at whoever had not signed up yet.
//
// A specialist starts empty as before: no shape ships a specialist record,
// because what a specialist DOES is the whole question and the wizard's
// brief is the only thing that answers it.
func wizardBaseRecord(kind string) AgentRecord {
	if kind != "assistant" {
		return AgentRecord{}
	}
	base, ok := shapeBaseRecord(assistantShape)
	if !ok {
		return AgentRecord{}
	}
	base.ID = ""         // a new agent, not the framework's
	base.Hidden = false  // the user's own front door, not a seed
	base.Exposed = false // reachable from their own agent list
	// Fleet stays OFF here even though the shape carries it, because the
	// caller turns it on for the FIRST-RUN assistant only. Conductor tools are
	// a real block of prompt on every turn, which is the "forty tools instead
	// of four" cost this system exists to avoid, and it is worth paying only
	// for the agent whose job is to delegate. Inheriting it would hand the
	// toolset to every later assistant too, and quietly.
	base.Fleet = false
	base.ShapeID = assistantShape
	return base
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

	// Template path: clone the crafted seed record whole (prompt,
	// budgets, tools) under the chosen name — no brief, no drafting.
	if tpl := strings.TrimSpace(req.Template); tpl != "" {
		if !isWizardTemplate(tpl) {
			http.Error(w, "unknown template", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		saved, err := cloneAgent(udb, tpl, user, req.Name, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if d := strings.TrimSpace(req.Description); d != "" && d != saved.Description {
			saved.Description = d
			if updated, uerr := saveAgent(udb, saved); uerr == nil {
				saved = updated
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(saved)
		return
	}

	kind, kindOK := wizard_kinds[req.Kind]
	// Picked jobs and typed words are the same answer arriving two ways; either
	// alone is enough to draft from, which is the point of offering the picks.
	req.Purpose = strings.TrimSpace(joinWizardPurpose(decodeWizardPicks(req.PurposePicks), req.Purpose))
	if !kindOK || req.Name == "" || req.Purpose == "" {
		http.Error(w, "agent type, a name, and at least one thing for it to do are required", http.StatusBadRequest)
		return
	}

	rec := wizardBaseRecord(req.Kind)
	rec.Owner = user
	rec.Name = req.Name
	rec.Description = strings.TrimSpace(req.Description)
	rec.Triggers = req.Triggers
	rec.Cortex = kind.cortex
	rec.MemoryMode = kind.memory_mode
	rec.RecallHints = true
	if !applyWizardMemory(&rec, req) {
		http.Error(w, "unknown memory setting", http.StatusBadRequest)
		return
	}
	// Assistant personalization ("About you" step) lands as the agent's
	// initial Working notes, so it knows the user from message one.
	// The first-run assistant is the front door: the agent a person talks to,
	// which hands work to the specialists they make later. So it is created a
	// CONDUCTOR — delegation, schedules and monitors — where a later assistant
	// is not. That asymmetry is deliberate: conductor tools are a real block of
	// prompt on every turn, which is the "forty tools instead of four" cost
	// this system exists to avoid, and it is only worth paying for the agent
	// whose job is actually to delegate.
	//
	// It is a DEFAULT, not a species: the toggle is in the editor either way.
	if req.Kind == "assistant" && strings.TrimSpace(req.FirstRun) != "" {
		rec.Fleet = true
	}
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
	Kind string `json:"agent_kind"`
	// Template short-circuits the guided flow: clone this wizard
	// template (a crafted seed) as the user's agent. "" = build from
	// the brief below.
	Template    string `json:"template"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Purpose     string `json:"purpose"`
	// PurposePicks is the checklist. RawMessage because a "checklist" field
	// saves a JSON array, but a form nobody touched sends a string or nothing
	// at all — the same shape apps/admin decodes for its capability lists.
	PurposePicks json.RawMessage `json:"purpose_picks"`
	Traits       json.RawMessage `json:"traits"`
	OnWrong      string          `json:"on_wrong"`
	OnUnsure     string          `json:"on_unsure"`
	OnVague      string          `json:"on_vague"`
	OnDone       string          `json:"on_done"`
	OnNothing    string          `json:"on_nothing"`
	OnConflict   string          `json:"on_conflict"`
	OnOutOfReach string          `json:"on_outofreach"`
	OnPartial    string          `json:"on_partial"`
	Examples     string          `json:"example_tasks"`
	Style        string          `json:"style"`
	StyleNotes   string          `json:"style_notes"`
	// A STRING, because the form carries it as a hidden field and a hidden
	// field submits text: decoding "true" into a bool fails the whole request,
	// which would have taken agent creation with it.
	FirstRun string   `json:"first_run"`
	Triggers []string `json:"triggers"`
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
	if tr := decodeWizardPicks(req.Traits); len(tr) > 0 {
		b.WriteString("Character — how it deals with its user:\n")
		for _, t := range tr {
			b.WriteString("- " + t + "\n")
		}
	}
	if m := wizardMoments(req); m != "" {
		b.WriteString("How it should handle specific moments — the user picked these:\n" + m)
	}
	if n := strings.TrimSpace(req.StyleNotes); n != "" {
		b.WriteString("How it should behave, in the user's words:\n" + n + "\n")
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
	// Whole-field draft: the wizard composes a complete value, so no
	// section scoping.
	prompt := buildSuggestPrompt(field, "", hint, record)
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
	// Sectioned, like a drafted prompt: this is what the agent gets when the
	// model call fails, and a fallback that lands as one block would open in
	// the editor's free-form area with every slot empty. A person arriving to
	// fix a failed draft should find the same structure they would have edited
	// had it succeeded.
	var b strings.Builder
	b.WriteString("## Role & voice\n\n")
	b.WriteString("You are " + name + ". " + purpose)
	if s := strings.TrimSpace(style); s != "" {
		b.WriteString("\n\nTone and style: " + s + ".")
	}
	b.WriteString("\n\n## Approach\n\n")
	b.WriteString("Decompose non-trivial requests into a few concrete steps, brief each step with the specific deliverable and format you need back, and synthesize a direct answer. If the request is ambiguous, ask one clarifying question instead of guessing.\n")
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

// decodeWizardPicks reads a "checklist" field, tolerating every shape a form
// can send it: the JSON array it saves, a bare string from a control that was
// never touched, or nothing at all. Rejecting an agent over the encoding of an
// empty list would be a poor way to meet someone.
func decodeWizardPicks(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && strings.TrimSpace(one) != "" {
		// A single value, or the array re-encoded as a string by a client that
		// stringified it on the way out.
		var nested []string
		if json.Unmarshal([]byte(one), &nested) == nil {
			return nested
		}
		return []string{one}
	}
	return nil
}

// joinWizardPurpose renders the picked jobs and the typed sentence as one brief.
func joinWizardPurpose(picks []string, written string) string {
	var b strings.Builder
	for _, p := range picks {
		if p = strings.TrimSpace(p); p != "" {
			b.WriteString("- " + p + "\n")
		}
	}
	if w := strings.TrimSpace(written); w != "" {
		if b.Len() > 0 {
			b.WriteString("\nAlso, in the user's own words: ")
		}
		b.WriteString(w)
	}
	return b.String()
}

// wizardMoments renders the situational answers. They were asked as quoted
// replies rather than adjectives, so what comes back is a description of the
// BEHAVIOUR the user picked — which is what a persona draft can act on, where
// "direct" would have to be interpreted first.
func wizardMoments(req wizardRequest) string {
	var b strings.Builder
	for _, m := range []struct{ when, picked string }{
		{"when the user says something it believes is wrong", req.OnWrong},
		{"when it does not know the answer", req.OnUnsure},
		{"when a request is big or vague", req.OnVague},
		{"when it finishes something", req.OnDone},
		{"when it finds nothing", req.OnNothing},
		{"when its sources disagree", req.OnConflict},
		{"when the request needs something it cannot reach", req.OnOutOfReach},
		{"when it is only partly sure of an answer", req.OnPartial},
	} {
		if p := strings.TrimSpace(m.picked); p != "" {
			b.WriteString("- " + m.when + ": " + p + "\n")
		}
	}
	return b.String()
}
