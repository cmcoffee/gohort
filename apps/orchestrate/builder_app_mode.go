package orchestrate

// Builder's app mode: the turns of a session whose intake said "App".
//
// Builder builds tools, agents, pipelines, connections and apps, and its
// instructions are about all of them at once. An app is a different job:
// it is a web page and the backend behind it, and builds that were told
// about apps only in a tool's reference read a weather service's JSON back
// to the person and called it an app. App mode keeps the one agent (and the
// one authoring identity every permission check keys on) and gives an app
// session the instructions of a web developer instead, as a block an
// operator can reword like Builder's own.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// BuilderAppModeKey is the prompt-registry key for app mode's instructions.
const BuilderAppModeKey = "agent.builder.app"

const builderAppModeShipped = `## Building an app

This session is building an APP: a small web app that lives inside gohort. Work the way a web developer does, in the app's PROJECT FOLDER: app_def(action="checkout", name="<App name>") starts <slug>.app/ in your workspace with a page already wired to its backend (checkout id=<slug> writes out an existing app instead). Write each file with workspace write and change a few lines with workspace edit; app.json lists the sections, data sources and actions and names their files.

1. Design the page first: what the person sees and what they do there. Default to ONE html section holding a complete page (doctype, head, style, body) that looks finished: a clear title, a layout that works on a phone, readable type, and real visuals where they carry meaning (an icon per weather, a chart for numbers over time). Typed sections (form, table, chart, display) are the shortcut for an app that really is just a list or an admin form.
2. Then the backend the page needs, in Python. Each thing the page loads is a data source (GET data/<name>, returns JSON shaped for the page); each thing it does is an action (POST action/<name>), which saves the records it prints and hands the page back what it prints under result: a turn that changes the state and says what happened is ONE action, never a data source plus a save from the page. Everything that reaches gohort happens there: call_tool for the owner's tools (capabilities tool:<name>), fetch_via for their credentials, ask for the model (capabilities ask), and the person's records, shared data and settings arrive as env vars. Before writing a script around a tool or an API, call it once yourself and read what it actually returns: write the script against those keys, never against keys you expect it to have. call_tool's output (and ask's with json=True) reads as its JSON directly: out.get('key'), out['list']. Code two scripts need goes once in lib/<name>.py, imported as "from <name> import ..."; never copy it between files.
3. Wire the page to its backend with window.app: app.data(name, params), app.action(name, body), app.records.list/save/remove, app.shared(name), app.ask(prompt), app.asset(name), and app.onChange(fn) to stay live. Never show the person JSON: turn it into cards, rows, a chart, a sentence.
4. An AI-driven app gets its own brain, written for this app's job: an agent (create_agent with owning_app set to the app's slug, then app_def agent_id) that the backend asks (ask: an answer, no tools) or runs (run_agent: a turn WITH its tools, for an agent that should act, roll, look up), or a pipeline (pipeline create with owning_app, then app_def pipeline_id) whose run section shows its stages, or that a script runs to the end (run_pipeline) when it is short. owning_app files it under the app instead of among the owner's agents.
5. Ask the user only what you cannot decide well yourself. Not the app's name, not its colors: pick good ones.
6. Run each backend file as you write it (app_def run, file=..., sample=[...]) and fix it until its output is right. Before you publish, write NOTES.md: what the app is, the choices you made and why, what you left out; on a change, what you changed and why. Then app_def publish the folder, verify it with a sample, and LOOK at the screenshot verify saves (workspace view_image): fix what reads badly, publish again. Open your reply with a line or two on what you built beyond what was asked.`

func init() {
	RegisterPromptBlock(PromptBlock{
		Key:      BuilderAppModeKey,
		Title:    "Builder: building an app",
		Category: "Builder",
		Gate:     "Builder turns in a session whose intake answered \"App\" to \"What kind of thing?\".",
		Text:     builderAppModeShipped,
	})
}

// builderAppMode reports whether this Builder turn is building an app: the
// session's latest intake answered App. A session with no intake (a handoff,
// a request from another agent) is not, and gets Builder as it was.
func (t *chatTurn) builderAppMode() bool {
	if t == nil || t.session == nil || !isBuilderAgent(t.agent.ID) {
		return false
	}
	kind, from := t.intakeKind()
	if from == "" {
		Debug("[builder] no intake on session %s (%d messages): not app mode", t.chatSessionID(), len(t.session.Messages))
	}
	return strings.EqualFold(kind, "app")
}

// intakeKind is the latest intake's answer to "What kind of thing?", and
// where it was read: the stored intake values, or else the intake's packed
// text ("**<label>:** App"), which every intake send carries. Three app-mode
// runs in a row never turned it on, and the values are the one piece of that
// path no check could see, so the text is read too.
func (t *chatTurn) intakeKind() (kind, from string) {
	label := ""
	for _, f := range t.agent.IntakeForm {
		if f.Name == "kind" {
			label = strings.TrimSpace(f.Label)
		}
	}
	for i := len(t.session.Messages) - 1; i >= 0; i-- {
		m := t.session.Messages[i]
		if m.Role != "user" {
			continue
		}
		if v := m.IntakeValues; len(v) > 0 {
			return strings.TrimSpace(v["kind"]), "values"
		}
		if label == "" {
			continue
		}
		marker := "**" + label + ":**"
		if j := strings.Index(m.Content, marker); j >= 0 {
			rest := strings.TrimSpace(m.Content[j+len(marker):])
			if k := strings.IndexAny(rest, "\n\r"); k >= 0 {
				rest = rest[:k]
			}
			return strings.TrimSpace(rest), "text"
		}
	}
	return "", ""
}

// renderBuilderAppModeBlock is app mode's instructions for this turn, or "".
func (t *chatTurn) renderBuilderAppModeBlock() string {
	if !t.builderAppMode() {
		return ""
	}
	Log("[builder] app mode on for session %s", t.chatSessionID())
	return "\n\n" + EffectivePromptText(BuilderAppModeKey, builderAppModeShipped)
}

// appTypedOnly reports an app built only from the typed data sections (form,
// table, display, chart, actions, empty): the shortcut for a plain list or
// an admin form. An app built around a chat, a pipeline run or a workbench
// is a different shape and is not this.
func appTypedOnly(spec AppSpec) bool {
	secs := appSectionList(spec)
	if len(secs) == 0 {
		return false
	}
	for _, sec := range secs {
		switch strings.ToLower(strings.TrimSpace(mapStr(sec, "kind"))) {
		case "form", "table", "display", "chart", "actions", "empty":
		default:
			return false
		}
	}
	return true
}

// appPlainListOnPurpose is a typed-only app whose notes record that the
// person asked for exactly that ("plain list: <why>").
func appPlainListOnPurpose(spec AppSpec) bool {
	return strings.Contains(strings.ToLower(spec.Notes), "plain list:")
}

// appModeTypedOnlyProblem is the app-mode objection to a typed-only app, or
// "". Prompt text alone did not move it: with the app-mode block and the
// help both saying design the page, a build still shipped form + table +
// display and said icons "would need a heavier custom page".
func (t *chatTurn) appModeTypedOnlyProblem(spec AppSpec) string {
	if !t.builderAppMode() || !appTypedOnly(spec) || appPlainListOnPurpose(spec) {
		return ""
	}
	return "this session builds a web app, and this one is only typed sections (form, table, display, chart), the shortcut for a plain list or an admin form. Build the page: ONE html section holding a complete, designed page that loads with app.data(...), saves with app.records.save(...) and stays live with app.onChange(...), with its pictures (icons drawn as inline SVG) and its chart (a CDN chart library). Keep the typed sections only if the person asked for a plain list: then write \"plain list: <why>\" into the app's notes."
}
