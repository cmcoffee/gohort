// Package anvil is gohort's coding surface: an agent that reads and changes a
// real project, driven by gohort's own model stack.
//
// WHY IT IS A GOHORT APP. A coding assistant can be a window onto somebody
// else's binary talking to somebody else's API. Anvil is the other trade:
// gohort's loop, gohort's tools, gohort's routing, so the same work can run on
// the local model, on the lead, or on whatever a deployment has configured,
// and so it builds on the framework's own memory, skills and approvals.
//
// WHAT IT IS NOT. Anvil does not rebuild or restart anything itself. A project
// carries the commands its OPERATOR wrote (see actions.go), so a deployment
// that wants a rebuild-and-restart button configures one, on the project it
// applies to, gated on a green build.
package anvil

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// anvilAgentID is the app-owned agent every Anvil conversation runs as.
const anvilAgentID = "app-anvil-coder"

func init() {
	RegisterApp(new(Anvil))
	RegisterAdminSection(AdminSectionEntry{Section: bundleAdminSection(), App: "/anvil"})
	RegisterAdminSection(AdminSectionEntry{Section: publishAdminSection(), App: "/anvil"})
	// NO route stage. One was registered here ("app.anvil", defaulting to
	// lead) and it was DEAD: an app agent's reasoning tier is chosen by
	// orchestrate from the agent's own LeadModel flag, and this key was never
	// passed as a RouteKey by anything. What it produced was a dropdown in
	// Admin > Routing that looked like the control and moved nothing — worse
	// than no control at all, because it answers the question wrongly for
	// whoever finds it first. The tier is per PROJECT now (Project.Reasoning),
	// which is the granularity that actually matters here: a refactor across
	// a real tree wants the lead, a one-line fix in a scratch checkout does
	// not.
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID:          anvilAgentID,
		OwningApp:   "Anvil",
		Name:        "Anvil",
		Description: "Reads and edits a project's source, checking its work as it goes.",
		Hidden:      true, // reached through the Anvil app, not the agent picker
		MemoryMode:  "agent",
		Prompt:      coderPrompt,
		// Its own tools for the project (codeTools) and these, nothing
		// else: every tool an agent may use was on its catalog, 44 of them
		// at about 15K tokens a call, image, video and Builder's request
		// among them, on a local worker that spent minutes on each round.
		ExactTools:   true,
		AllowedTools: anvilCoreTools,
	})
}

// anvilCoreTools are the framework's tools a coding turn keeps beside its
// own: asking the person, its notes and what it learned, reading output too
// long to show at once, freeing context, carrying on past a promise to act,
// and the web for a library's docs, an error message, or the live page an
// app reads. browse_page is the browser: an app that scrapes a site is built
// by looking at what the site serves, and fetch_url falls back to the browser
// only on a refusal, not on a site that never answers a plain request.
var anvilCoreTools = []string{
	"ask_user", "ask_user_form",
	"update_notes", "remember", "recall", "forget",
	"read_output", "release_output", "compact_context", "keep_going",
	"web_search", "fetch_url", "browse_page",
}

// anvilRounds is how many rounds a turn may take unless the person set their
// own. The default of 15 ran out on an ordinary change to an app: reading its
// seven files and planning took eight, and the edits, the checks and fixing
// what they found did not fit.
const anvilRounds = 40

// coderPrompt is the coding agent's standing instruction.
//
// Written action-first and short. A long prompt spends the context a coding
// task needs for the code itself, and the local worker follows a list of
// concrete moves far better than it follows an essay about craft. Anything
// that can be enforced by a tool refusing bad input is enforced there instead
// of being asked for here — the anchor-uniqueness rule lives in edit_file's
// error message, where it arrives exactly when it is relevant.
const coderPrompt = `You are Anvil, a coding assistant working inside one project. You change real files. Work like an engineer who has to live with the result.

HOW TO WORK
1. Find out what is actually there before changing it. search_code to locate, list_files to orient, read_file to read. Never edit a file you have not read this session.
2. Make the smallest change that does the job. Match the surrounding code's naming, comment density and idiom. The goal is a change that reads like the file it lands in, not like a different author.
3. Check your work. Run the build or the tests if the project offers them. If you cannot verify, say what you did not verify.
4. For a change that touches several files, set a short plan with the plan tool before you start, and mark each step done as you finish it.
5. Report what changed and where, in one short paragraph. The diffs are already shown to the user, so do not paste the code back at them.

EDITING
- edit_file for every change to an existing file. It replaces exact text, so read first and copy the anchor precisely.
- create_file only for genuinely new files. It refuses to overwrite, on purpose.
- Never re-emit a whole file to change part of it.
- One logical change at a time. Several small anchored edits beat one large one: if something is wrong, the user can see which part.
- When an edit is refused, its message shows the file's real lines: copy the anchor from those and try again. Never end the turn handing back edits you could still make; stop only for what needs the person.

WHEN YOU ARE UNSURE
Ask. A question costs one turn; a confident wrong edit costs the user their afternoon. Ask when the request is ambiguous, when there are two reasonable designs, or when the change would touch something you were not asked about.

WHAT NOT TO DO
- Do not invent APIs, flags, filenames or behavior. If you have not read it in this project, check.
- Do not report success you have not observed. "The tests pass" is only true after you ran them.
- Do not widen the job. If you notice something else worth fixing, mention it; do not fix it unasked.

WORKING NOTES
You keep one short notes block for this project and it is always in front of you. Put in it what you had to find out the hard way and would otherwise work out again next session: the build and test commands that actually work here, what is half-finished and what the next step was, a decision and the reason for it. Rewrite it with update_notes when it stops being true. Keep it a working set, not a log: when it is full, drop what has stopped mattering.
The operator's project notes and the repository's own instruction files outrank it. If your notes disagree with either, your notes are stale: correct them, do not act on them.`

// anvilSeedNotes is the Working-notes block a project starts with.
//
// A shape rather than a blank. An empty block invites the model to either
// ignore the layer or fill it with narration of the current turn; three
// headings say what belongs there and, by omission, what does not. It stays the
// durable fallback until the first update_notes writes over it, so a project
// nobody has worked in yet still shows the operator what this block is for.
const anvilSeedNotes = `Nothing recorded for this project yet.

Build / test: the commands that actually work here, and any that look right but do not.
In flight: what is half-done, and what the next step was.
Decided: a choice already made and why, so it is not reopened next session.`

// Anvil is the app.
type Anvil struct {
	// eval is the task-set run going now, or the last one's outcome
	// (eval.go).
	eval *evalState
	AppCore
}

func (T Anvil) Name() string         { return "anvil" }
func (T Anvil) SystemPrompt() string { return "" }
func (T Anvil) Desc() string {
	return "Apps: pair with an AI on a real project to read, edit and check its source."
}

func (T *Anvil) Init() error { return T.Flags.Parse() }

func (T *Anvil) Main() error {
	Log("Anvil is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

// --- WebApp ------------------------------------------------------------------

func (T *Anvil) WebPath() string { return "/anvil" }
func (T *Anvil) WebName() string { return "Anvil" }
func (T *Anvil) WebDesc() string { return "Read, edit and check a project's source with an AI." }

func (T *Anvil) Routes() {
	T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handlePage(w, r)
	})
	T.HandleFunc("/projects", T.handleProjectsPage)
	T.HandleFunc("/project", T.handleProjectSettingsPage)
	T.HandleFunc("/api/projects", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleProjects),
		"POST": jsonapi.Post(T.DB, T.handleProjectSave),
	}.Serve)
	T.HandleFunc("/api/projects/cards", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleProjectCards)}.Serve)
	T.HandleFunc("/api/project/{id}", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.handleProject),
		"DELETE": jsonapi.Delete(T.DB, T.handleProjectDelete),
	}.Serve)
	T.HandleFunc("/api/project/{id}/status", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleProjectStatus)}.Serve)
	T.HandleFunc("/api/project/{id}/worktree", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleWorktree)}.Serve)
	T.HandleFunc("/api/project/{id}/deepen", jsonapi.Post(T.DB, T.handleDeepen))
	T.HandleFunc("/api/project/{id}/patch", T.handleDownloadChanges)
	// A repository started here, committed and published (publish.go).
	T.HandleFunc("/api/projects/new", jsonapi.Post(T.DB, T.handleNewProject))
	T.HandleFunc("/api/project/{id}/commit", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleCommitState),
		"POST": jsonapi.Post(T.DB, T.handleCommit),
	}.Serve)
	T.HandleFunc("/api/project/{id}/commit/undo", jsonapi.Post(T.DB, T.handleCommitUndo))
	T.HandleFunc("/api/project/{id}/commit/assist", T.commitAssist())
	T.HandleFunc("/api/project/{id}/publish", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handlePublishState),
		"POST": jsonapi.Post(T.DB, T.handlePublish),
	}.Serve)
	T.HandleFunc("/api/project/{id}/bundle", T.handleDownloadBundle)
	T.HandleFunc("/api/publish/settings", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handlePublishSettings),
		"POST": jsonapi.Post(T.DB, T.handlePublishSettingsSave),
	}.Serve)
	// One of your apps (app_project.go).
	T.HandleFunc("/api/apps/open", jsonapi.Post(T.DB, T.handleOpenApp))
	T.HandleFunc("/api/project/{id}/install", jsonapi.Post(T.DB, T.handleAppInstall))
	T.HandleFunc("/api/project/{id}/install_new", jsonapi.Post(T.DB, T.handleAppInstallNew))
	T.HandleFunc("/api/project/{id}/preview", jsonapi.Post(T.DB, T.handleAppPreview))
	// The app's versions, what each changed, and going back to one
	// (app_history_view.go).
	T.HandleFunc("/api/project/{id}/app_history", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleAppHistory)}.Serve)
	T.HandleFunc("/api/project/{id}/app_history/diff", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleAppHistoryDiff)}.Serve)
	T.HandleFunc("/api/project/{id}/app_history/restore", jsonapi.Post(T.DB, T.handleAppHistoryRestore))
	// A repository brought as a git bundle (bundle.go).
	T.HandleFunc("/api/bundle/upload", T.handleBundleUpload)
	T.HandleFunc("/api/bundle/finalize", jsonapi.Post(T.DB, T.handleBundleFinalize))
	T.HandleFunc("/api/bundle/status", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleBundleStatus)}.Serve)
	T.HandleFunc("/api/bundle/settings", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleBundleSettings),
		"POST": jsonapi.Post(T.DB, T.handleBundleSettingsSave),
	}.Serve)
	// Not jsonapi: a detached action flushes its answer before the command
	// replaces the server answering it.
	T.HandleFunc("/api/project/{id}/action/{action}", T.handleRunAction)
	T.HandleFunc("/api/notes", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleNotes),
		"POST": jsonapi.Post(T.DB, T.handleNotesSave, jsonapi.MaxBody(maxChatBody)),
	}.Serve)
	T.HandleFunc("/api/edits", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleEdits)}.Serve)
	T.HandleFunc("/api/edit/{id}/revert", jsonapi.Post(T.DB, T.handleRevert))
	// Plan mode, undo from a message, "# remember this" (planmode.go).
	T.HandleFunc("/api/plan_mode", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handlePlanMode),
		"POST": jsonapi.Post(T.DB, T.handlePlanModeSave),
	}.Serve)
	T.HandleFunc("/api/undo", jsonapi.Post(T.DB, T.handleUndo))
	T.HandleFunc("/api/commands", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleCommands)}.Serve)
	T.HandleFunc("/api/mentions", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleMentions)}.Serve)
	T.HandleFunc("/api/notes/add", jsonapi.Post(T.DB, T.handleRemember, jsonapi.MaxBody(maxChatBody)))
	// Measuring Anvil on its task set: administrators only (eval.go).
	T.HandleFunc("/eval", T.handleEvalPage)
	T.HandleFunc("/api/eval/settings", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleEvalSettings)}.Serve)
	T.HandleFunc("/api/eval/start", jsonapi.Post(T.DB, T.handleEvalStart))
	T.HandleFunc("/api/eval/progress", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleEvalProgress)}.Serve)
	T.HandleFunc("/api/eval/stop", jsonapi.Post(T.DB, T.handleEvalStop))
	T.HandleFunc("/api/eval/runs", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleEvalRunList)}.Serve)
	T.HandleFunc("/api/eval/latest", jsonapi.Methods{"GET": jsonapi.Get(T.DB, T.handleEvalLatest)}.Serve)
	// The chat and the standing approvals, which hand off to the agent host.
	T.HandleFunc("/api/", T.handleAPI)
}

// scopedAgentID is the agent identity a project's conversations live under.
//
// Sessions are stored per agent id ("orchestrate_sessions:<id>"), so one id
// across every project means one undifferentiated list: the thread where you
// refactored a client's API sits next to yesterday's scratch experiment, and
// picking a project changes nothing about what you are looking at. Deriving
// the id from the project splits the store by construction rather than by a
// filter somebody has to remember to apply.
//
// The scope this creates is wider than sessions alone — per-agent memory and
// topics key on the same id, so those become per-project too. That is the
// intent and not a side effect: what the assistant learned about ONE codebase
// is rarely true of another, and a shared memory across projects is a way to
// carry a wrong assumption into a tree where nobody can see where it came
// from.
//
// Sessions created before this split remain under the bare id and will not
// appear. Anvil had not been released when this changed, so no migration was
// written for a handful of scratch threads.
func scopedAgentID(p Project) string {
	if p.ID == "" {
		return anvilAgentID
	}
	return anvilAgentID + ":" + p.ID
}

// callerIsAdmin answers the admin question the way the rest of gohort does.
// User and admin records live in AuthDB, not in the app's own root DB — a
// mistake worth naming here because reading T.DB looks right and silently
// answers false for everyone.
func callerIsAdmin(r *http.Request) bool {
	if AuthDB == nil {
		return false
	}
	return AuthIsAdmin(AuthDB(), r)
}
