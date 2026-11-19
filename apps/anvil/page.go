// The Anvil page.
//
// One AgentLoopPanel, a project picker riding on every message, and the
// operator's actions as a toolbar. Everything app-specific — the diff card
// renderer, the revert button, the action buttons — is registered from
// ExtraHeadHTML through the framework's extension registries, so nothing
// about Anvil leaks into core/ui.
package anvil

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// handlePage renders the working surface — always the same surface.
//
// It used to branch: no projects meant a different page carrying an empty
// state and an add form, and adding one navigated you to the real thing. That
// makes the first thing a new user sees a page that is not the app, and it
// makes "add a project" an action that costs you your place. The dialog is
// the right shape for adding one; replacing the workspace with it is not.
//
// So the panel is always mounted, the add-project form lives in a modal that
// floats over it, and having no projects only means that modal opens by
// itself on arrival. What you see behind it is the thing you are about to
// start using, not a waiting room for it.

// chatPanel builds the conversation panel. Extracted from handlePage so its
// wiring can be asserted on without standing up a request — several of these
// fields are the difference between an affordance existing and not, and none
// of them fail loudly when absent.
func (T *Anvil) chatPanel(options []ui.SelectOption, defaultProject, empty string, actions []ui.ToolbarAction) ui.AgentLoopPanel {
	return ui.AgentLoopPanel{
		SendURL:    "api/chat/send",
		CancelURL:  "api/chat/cancel",
		ConfirmURL: "api/chat/confirm",
		// Where a message typed mid-run goes. The agent drains the queue
		// between rounds, so a correction ("not that file — the other one")
		// reaches it without discarding the work already done.
		InjectURL: "api/inject",
		// Saved prompts as slash commands (prompts.go).
		CommandsURL: "api/commands?project_id={project_id}",
		// @file mentions (mentions.go).
		MentionsURL: "api/mentions?project_id={project_id}",
		// {project_id} is substituted by the panel from the picker on every
		// request, so the rail always lists the open project's threads and
		// switching projects switches the conversation history with it.
		ListURL:   "api/chat/sessions?project_id={project_id}",
		LoadURL:   "api/chat/sessions/{id}?project_id={project_id}",
		DeleteURL: "api/chat/sessions/{id}?project_id={project_id}",
		// Edit / Delete on the last user message — which is also the
		// retry. Edit replaces it, truncates the thread there, and
		// resends; Delete just truncates. Without this a turn that
		// died mid-stream leaves you with no way to run it again
		// except retyping it, which is the state this app was in
		// the first time a plan timed out.
		//
		// MessageScrub is deliberately NOT set. It needs a PATCH
		// endpoint that understands {delete_at}, and the public
		// session handler only understands {at} truncation — the
		// component's own comment warns that setting it against
		// such an endpoint renders a button that misfires.
		TruncateURL: "api/chat/sessions/{id}?project_id={project_id}",
		ListTitle:   "Sessions",
		// Two windows: the sessions beside the conversation, always there on
		// a desktop, as in the main chat. A preview opens in the pane the
		// main chat uses, when the assistant shows one (app_project.go).
		ListPosition: "top",
		NewLabel:     "New session",
		Markdown:     true,
		SubmitLabel:  "Send",
		Placeholder:  "Ask about the project, or describe the change you want. @ names a file, / a saved prompt, # something to remember.",
		EmptyText:    empty,
		ExtraFields: []ui.ChatField{{
			Name:        "project_id",
			Label:       "Project",
			Type:        "select",
			OptionPairs: options,
			Default:     defaultProject,
		}},
		ExtraFieldsInSidebar: true,
		// Plan mode, as in Claude Code (planmode.go).
		Modes:   []ui.ChatMode{planModeSwitch()},
		Actions: actions,
		// Each step shows as a line in the conversation as it runs, in
		// Anvil's words (anvilStepSummariesJS), so the Activity pane would
		// only repeat them.
		ShowSteps:    true,
		HideActivity: true,

		// --- reconnect and addressability ---------------------------------
		//
		// A coding turn is long: read a dozen files, edit, build, read the
		// failure, edit again. Without a run registry, closing the tab or a
		// reload part-way through orphans it — the work continues on the
		// server and the person who asked for it can never see the rest.
		// RunsURLBase reattaches to the in-flight run and replays what was
		// missed, and points Cancel at the run rather than the session.
		RunsURLBase: "api/runs/",
		// Puts the open thread in the address bar, so a reload comes back to
		// the conversation you were in rather than a blank one.
		DeepLinkParam: "session",

		// --- settling answered cards --------------------------------------
		//
		// The edit cards carry a Revert button, and a card's stored life does
		// not end when its button is clicked. Without this the card replays on
		// every load offering to revert an edit that is already back — the
		// server refuses the second revert, so the damage is a live control
		// over a settled decision rather than data loss, which is its own kind
		// of wrong.
		BlockResolveURL: "api/chat/sessions/{id}/blocks/{block_id}/resolve?project_id={project_id}",

		// --- composing -----------------------------------------------------
		//
		// A screenshot of a stack trace or a broken page is often the fastest
		// way to say what is wrong, and this app has vision available through
		// the same stack as everything else.
		Attachments: true,
		// Threads accumulate quickly when each project has its own rail.
		BulkSelect: true,
		// Rename: a coding session's auto-title is drawn from its first
		// message, which is routinely the least descriptive thing about it.
		RenameURL: "api/chat/sessions/{id}/rename?project_id={project_id}",
		// Why a turn did what it did — suppressed calls, denied tools, a
		// timeout. Anvil writes these already (a denied tool, a granted
		// standing approval); until now nothing displayed them, so the
		// answer to "why did that stop" was in the server log.
		DiagnosticsURL: "api/chat/sessions/{session}/diagnostics?project_id={project_id}",
	}
}

func (T *Anvil) handlePage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	isAdmin := callerIsAdmin(r)
	if id := strings.TrimSpace(r.URL.Query().Get("project")); id != "" {
		T.serveWorkspace(w, r, udb, user, isAdmin, id)
		return
	}
	T.serveHome(w, r, user, isAdmin)
}

// serveHome is Anvil's home: the projects you can work on, each a card that
// opens its workspace, and the New project menu of the ways to add one.
//
// Home is the projects because a project is chosen before a conversation,
// never a field in it (docs/design/anvil-development.md): what you are
// working on decides which sessions, notes, check and permissions apply.
func (T *Anvil) serveHome(w http.ResponseWriter, r *http.Request, user string, isAdmin bool) {
	empty := "No projects yet: add one with New project."
	sub := "Open one to work on it. Each keeps its own sessions, notes, check and permissions."
	bundles := T.mayBringBundle(isAdmin)
	if !isAdmin {
		empty = "None yet. Start one with New project, or ask an administrator to add one and let you open it."
		sub = "Open one to work on it. New project starts a repository of your own; adding one by its address is an administrator's."
		if bundles {
			sub = "Open one to work on it. New project starts a repository of your own or brings one as a git bundle; adding one by its address is an administrator's."
		}
	}
	// The ways to make a project are alternatives, so they are one menu, the
	// commonest first. A person with only one way gets a button that opens it.
	ways := []ui.ModalButton{{
		// A new repository, for anyone (publish.go).
		Label:    "Start a new repository",
		Subtitle: "An empty repository of your own, to build in with the assistant and publish when you want.",
		Width:    "560px",
		Body:     newRepoForm(),
	}}
	if bundles {
		ways = append(ways, ui.ModalButton{
			Label:    "Upload a bundle",
			Title:    "Bring a repository as a git bundle",
			Subtitle: "A file you make on your own machine. It becomes a checkout of your own here.",
			Width:    "560px",
			Body:     T.bundleUploadPanel(),
		})
	}
	// One of your own apps, for anyone who has one (app_project.go).
	if apps := appOptions(user); len(apps) > 0 {
		ways = append(ways, ui.ModalButton{
			Label:    "One of your apps",
			Title:    "Open one of your apps",
			Subtitle: "Its files, to change with the assistant. Preview shows your changes to you alone; Install puts them live.",
			Width:    "560px",
			Body:     appProjectForm(user),
		})
	}
	if isAdmin {
		ways = append(ways, ui.ModalButton{
			Label:    "A repository or a directory",
			Title:    "Add a repository or a directory",
			Subtitle: "A repository is checked out for you. A directory is edited in place.",
			Width:    "560px",
			Body:     T.projectForm(true, false, nil),
		})
	}
	// Inside the Projects card, above the list it adds to, as the admin
	// pages place theirs: on its own above the card it sat flush against it.
	newProject := ui.ModalButton{
		Label:   "＋ New project",
		Variant: "primary",
		Align:   "left",
		Items:   ways,
	}
	sections := []ui.Section{{
		Title:    "Projects",
		Subtitle: sub,
		// One row each, the last opened first, found by typing: a few
		// letters and Enter opens the first match, as a resume list does.
		Body: ui.Stack{Children: []ui.Component{newProject, ui.Table{
			Source:            "api/projects/cards",
			RowKey:            "id",
			Cards:             "list",
			Search:            true,
			SearchFocus:       true,
			SearchPlaceholder: "Find a project: its name, branch or address. Enter opens the first.",
			Columns: []ui.Col{
				{Field: "name", Label: "Name"},
				{Field: "kind", Label: "Kind", Type: "badge"},
				{Field: "where", Label: "Where", Card: "sub", Mute: true},
				{Field: "state", Label: "State", Card: "field"},
				{Field: "opened", Label: "Opened", Format: "reltime", Card: "field", Mute: true},
				{Field: "access", Label: "You may", Card: "field", Mute: true},
			},
			RowLink: "open",
			RowActions: []ui.RowAction{
				{Type: "button", Label: "Settings", PostTo: "project?id={id}", Method: "GET", OnlyIf: "can_change"},
				{Type: "button", Label: "Remove", PostTo: "api/project/{id}", Method: "DELETE", OnlyIf: "can_change",
					Confirm: "Remove this project? A repository's checkout is deleted; a directory is only forgotten.",
					Variant: "danger"},
			},
			EmptyText: empty,
		}}},
	}}
	page := ui.Page{
		Title:     "Anvil",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "1100px",
		Sections:  sections,
		Head:      ui.NewHead().CSS(anvilHomeCSS),
	}
	// Measuring Anvil is a page of its own, so it is a link in the header,
	// not a button left under the list.
	if isAdmin {
		page.Nav = []ui.NavLink{{Label: "Measure Anvil", URL: "/anvil/eval"}}
	}
	page.ServeHTTP(w, r)
}

// serveWorkspace is one project open: its conversation, its sessions beside
// it, and its changes, commands, settings, notes and permissions on the
// toolbar. The project is the page's (?project=), so the picker the panel
// carries it in is hidden; it stays for the requests and the client actions
// that read it. The query string, rather than a path, keeps every relative
// URL on the page resolving where it always has.
func (T *Anvil) serveWorkspace(w http.ResponseWriter, r *http.Request, udb Database, user string, isAdmin bool, id string) {
	p, found := T.findProject(udb, user, isAdmin, id)
	if !found {
		http.Redirect(w, r, "./", http.StatusFound)
		return
	}
	noteOpened(udb, p.ID)
	label := p.Name
	if p.Kind == KindRoot {
		label += " (directory)"
	}
	empty := "Describe what you want changed. Anvil reads before it edits, and every edit it makes can be reverted."
	if !writeAllowed(p, user, isAdmin) {
		empty = "You may read this project, not change it: ask about the code, and Anvil answers from it."
	}
	page := ui.Page{
		Title:     p.Name,
		ShowTitle: true,
		BackURL:   "/anvil/",
		MaxWidth:  "100%",
		Sections: []ui.Section{{
			NoChrome: true,
			Body:     T.chatPanel([]ui.SelectOption{{Value: p.ID, Label: label}}, p.ID, empty, T.toolbarActions(p, user, isAdmin)),
		}},
		Head: anvilHead([]Project{p}, user, isAdmin).CSS(anvilWorkspaceCSS).JS(gitFormsJS(p)),
	}
	page.ServeHTTP(w, r)
}

// anvilHomeCSS lines the add buttons up with the projects list below them,
// and leaves room before it. A row of alternatives is centred by default,
// which under a full-width list reads as floating.
const anvilHomeCSS = `.ui-stack.row:has(> .ui-modal-button-row) { justify-content: flex-start; margin-bottom: 0.75rem; }`

// anvilWorkspaceCSS hides the project picker on a workspace, where the page
// names the project; the git line the picker area also holds stays.
// The selector matches the runtime's own for that label, so it wins by order
// rather than by !important.
const anvilWorkspaceCSS = `.ui-agent .ui-agent-extras.ui-agent-extras-in-side .ui-agent-extras-label,
.ui-agent.ui-agent-list-top .ui-agent-extras-label { display: none; }`

// toolbarActions builds the open project's bar, by what each button acts on.
// Out front: what changed, and what this kind of project does with it (a
// repository commits and pushes, an app previews and installs), then its
// commands. Behind two menus: taking the work away as a file, and the
// project's own settings and records. Projects, and adding one, are on the
// home page.
func (T *Anvil) toolbarActions(p Project, user string, isAdmin bool) []ui.ToolbarAction {
	out := []ui.ToolbarAction{
		{Label: "Changes", Method: "client", URL: "anvil_history",
			Title: "What differs from the last commit, and every edit Anvil made"},
	}
	// Record the work and send it out (publish.go).
	if commitRefusal(p, user, isAdmin) == "" {
		out = append(out, ui.ToolbarAction{Label: "Commit", Method: "client", URL: "anvil_commit",
			Title: "Record what differs from the last commit as a commit of yours"})
	}
	if T.publishRefusal(p, user, isAdmin) == "" {
		out = append(out, pushAction(p))
	}
	// One of your apps: see the working copy, and put it live.
	if p.Source == sourceApp && writeAllowed(p, user, isAdmin) {
		out = append(out,
			ui.ToolbarAction{Label: "Preview", Method: "client", URL: "anvil_app_preview",
				Title: "Open your working copy of the app; only you see it"},
			ui.ToolbarAction{Label: "Install", Method: "client", URL: "anvil_app_install",
				Title: "Put your changes live, through the app's checks"},
			ui.ToolbarAction{Label: "History", Method: "client", URL: "anvil_app_history",
				Title: "Every version the app went live as, what each changed, and going back to one"})
	}
	// A checkout of the caller's that is not an app yet: out front when it
	// holds an app's folder, in the Project menu while it does not. Always
	// there, because the bar is drawn once and a turn can make the folder.
	if newAppOffRefusal(p, user) == "" {
		a := ui.ToolbarAction{Label: "Install as a new app", Method: "client", URL: "anvil_app_install_new",
			Title: "Make this project's app.json and files one of your apps, through the checks an install runs; the project is the app's from then on"}
		if newAppRefusal(p, user) != "" {
			a.Group = "Project"
		}
		out = append(out, a)
	}
	// Gated on PERMISSION, not on whether commands exist. An allowed user with
	// none configured gets the button and a modal saying so, which is where
	// they find out commands can be added.
	if actionsAllowed(p, user, isAdmin) {
		out = append(out, ui.ToolbarAction{Label: "Run", Method: "client", URL: "anvil_run",
			Title: "This project's configured commands"})
	}
	out = append(out, ui.ToolbarAction{Label: "Changes as a patch", Group: "Download", Method: "client", URL: "anvil_download_changes",
		Title: "A patch of everything that differs from the last commit, to apply elsewhere with git apply"})
	if p.Kind == KindClone {
		out = append(out, ui.ToolbarAction{Label: "The repository as a bundle", Group: "Download", Method: "client", URL: "anvil_download_bundle",
			Title: "The whole committed history as one file: git clone it at home and push from there"})
	}
	return append(out,
		ui.ToolbarAction{Label: "Settings", Group: "Project", Method: "client", URL: "anvil_settings",
			Title: "This project's notes, check, reasoning, commands and dependencies"},
		ui.ToolbarAction{Label: "What Anvil learned", Group: "Project", Method: "client", URL: "anvil_notes",
			Title: "The working notes Anvil keeps about this project: review, correct or clear them"},
		ui.ToolbarAction{Label: "Permissions", Group: "Project", Method: "client", URL: "anvil_permissions",
			Title: "Commands you allowed in this project without being asked again"},
		// The panel's own: the open session as Markdown, every step in it,
		// to paste into another assistant.
		ui.ToolbarAction{Label: "Copy session", Group: "Project", Method: "client", URL: "copy_session",
			Title: "Copy this conversation, with every step it took, to paste elsewhere"},
	)
}

// pushAction is Publish until the repository has somewhere it goes, then
// Push, naming where: the same dialog either way, filled in the second time.
func pushAction(p Project) ui.ToolbarAction {
	if p.PushURL == "" {
		return ui.ToolbarAction{Label: "Publish", Method: "client", URL: "anvil_publish",
			Title: "Push this repository's commits to a repository elsewhere"}
	}
	branch := p.PushBranch
	if branch == "" {
		branch = "its branch"
	}
	return ui.ToolbarAction{Label: "Push", Method: "client", URL: "anvil_publish",
		Title: "Push the commits to " + p.PushURL + " (" + branch + "), where it was published"}
}

// handleProjectsPage sends the old projects page home, which lists the
// projects and adds them now.
func (T *Anvil) handleProjectsPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "./", http.StatusFound)
}

// handleProjectSettingsPage is one project's full configuration.
//
// It exists because until now a project could only be CREATED. The add form
// posted to an endpoint that has always handled updates, and nothing in the
// interface ever sent an id — so every setting was decided once, at the moment
// you knew least about the project, and could never be revisited. Commands
// were worse still: settable through the API since the app was written, with
// no way to type one in.
func (T *Anvil) handleProjectSettingsPage(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	isAdmin := callerIsAdmin(r)
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	p, found := T.findProject(udb, user, isAdmin, id)
	if !found {
		http.NotFound(w, r)
		return
	}

	// Only projects this caller can already open, and never the project
	// itself — a self-link would shadow the root with a prefix.
	var others []ui.SelectOption
	for _, other := range T.listProjects(udb, user, isAdmin) {
		if other.ID == p.ID {
			continue
		}
		others = append(others, ui.SelectOption{Value: other.ID, Label: other.Name})
	}

	form := T.projectForm(isAdmin, true, others)
	form.Fields = append(form.Fields, knowledgeField(user), promptsField())
	// What the project's files say it is built with, beside the check box.
	if base, err := p.baseDir(); err == nil {
		if s := suggestCheck(base); s != "" {
			for i := range form.Fields {
				if form.Fields[i].Field == "check_command" {
					form.Fields[i].Help += " This project's files suggest: " + s
				}
			}
		}
	}
	form.Source = "api/project/" + p.ID

	sub := "A checkout Anvil made. Editing it affects nobody else until you push."
	if p.Kind == KindRoot {
		sub = "A directory on this host. Edits here change files somebody is using."
	}
	if p.Source != "" {
		// No address to change: the bundle, or the app, was its only source.
		kept := form.Fields[:0]
		for _, f := range form.Fields {
			switch f.Field {
			case "repo_url", "branch", "token", "full_history":
				continue
			}
			kept = append(kept, f)
		}
		form.Fields = kept
		sub = "A checkout made from " + p.BundleName + ". Commit records your work; Publish, on the project's toolbar, pushes it to a repository elsewhere, and Download changes gives you a patch to apply at home with git apply."
	}
	if p.Source == sourceNew {
		sub = "A repository you started in Anvil. Commit records your work; Publish, on the project's toolbar, pushes it to a repository elsewhere, and Download as a bundle gives you its whole history."
	}
	if p.Source == sourceApp {
		form.Fields = append(form.Fields, appProjectFields()...)
		sub = "Your app " + p.AppSlug + ", as its files. Preview shows your changes to you alone; Install puts them live, through the app's checks."
	}

	// Actions that DO something rather than save something, kept out of the
	// form so a click cannot be mistaken for an unsaved edit.
	var acts []ui.ToolbarAction
	if p.Kind == KindClone && p.Source == "" {
		acts = append(acts, ui.ToolbarAction{Label: "Fetch full history", Method: "client",
			URL: "anvil_deepen", Data: p.ID,
			Title: "Checkouts are shallow by default; git log and blame need this."})
	}
	acts = append(acts, ui.ToolbarAction{Label: "Remove project", Method: "client",
		URL: "anvil_delete_project", Data: p.ID, Variant: "danger",
		Confirm: removeWarning(p)})

	page := ui.Page{
		Title:     p.Name + ": settings",
		ShowTitle: true,
		// Back to the conversation, which is where you came from now that
		// Settings acts on the open project rather than being reached from a
		// list.
		BackURL:  "/anvil/?project=" + url.QueryEscape(p.ID),
		MaxWidth: "900px",
		Sections: []ui.Section{
			{Title: p.Name, Subtitle: sub, Body: form},
			{Title: "Actions", Subtitle: "These take effect immediately and are not part of the form above.",
				Body: ui.Toolbar{Actions: acts}},
		},
		Head: ui.NewHead().
			CSS(anvilCSS).
			ClientAction("anvil_deepen", anvilDeepenJS).
			ClientAction("anvil_delete_project", anvilDeleteProjectJS),
	}
	page.ServeHTTP(w, r)
}

// removeWarning states what removing this project actually destroys.
func removeWarning(p Project) string {
	if p.Kind == KindRoot {
		return "Remove " + p.Name + " from Anvil? The directory itself is left alone."
	}
	return "Remove " + p.Name + " and delete its checkout? Anything not pushed is lost."
}

// projectForm builds the project form, for creating one or editing one.
//
// ONE panel, not several. A FormPanel posts the WHOLE record, so two panels
// over the same project would each save their own fields plus stale copies of
// the other's — the second save quietly reverting the first. Kind-specific
// fields are shown or hidden with ShowWhen instead, which keeps the record
// whole on every write.
//
// editing selects the fields that only mean something once the project exists:
// its id (which turns the create into an update) and the commands editor.
func (T *Anvil) projectForm(isAdmin, editing bool, others []ui.SelectOption) ui.FormPanel {
	kindOptions := []ui.SelectOption{{Value: KindClone, Label: "Repository (checked out for you)"}}
	if isAdmin {
		kindOptions = append(kindOptions, ui.SelectOption{Value: KindRoot, Label: "Directory on this host (edited in place)"})
	}
	fields := []ui.FormField{}
	if editing {
		// Carries the identity through the save. Without it the endpoint mints
		// a new project on every edit, which looks like the form not saving.
		fields = append(fields, ui.FormField{Field: "id", Type: "hidden"})
	}
	fields = append(fields,
		ui.FormField{Field: "name", Label: "Name", Type: "text", Placeholder: "What you'll call it in the picker", Required: true},
		ui.FormField{Field: "kind", Label: "Kind", Type: "select", Options: kindOptions, Default: KindClone,
			LockWhen: lockKindWhenEditing(editing),
			Help:     helpForKind(editing)},
		ui.FormField{Field: "repo_url", Label: "Repository URL", Type: "text", Placeholder: "https://github.com/owner/name.git", ShowWhen: "kind:" + KindClone},
		ui.FormField{Field: "branch", Label: "Branch", Type: "text", Placeholder: "Leave blank for the default branch", ShowWhen: "kind:" + KindClone},
		ui.FormField{Field: "token", Label: "Access token", Type: "password", ShowWhen: "kind:" + KindClone,
			Placeholder: tokenPlaceholder(editing),
			Help:        "Only for a private repository. It is never sent back to this form, and never left in the checkout's git config."},
		ui.FormField{Field: "full_history", Label: "Fetch full history", Type: "toggle", ShowWhen: "kind:" + KindClone,
			Help: "Off is much faster to check out. Turn it on only if you need git log or blame: on a shallow clone log is nearly empty and blame is refused, because it would attribute every line to one commit."},
	)
	if isAdmin {
		fields = append(fields,
			ui.FormField{Field: "root", Label: "Directory path", Type: "text", Placeholder: "/srv/projects/thing", ShowWhen: "kind:" + KindRoot},
			ui.FormField{Field: "extra_roots", Label: "Extra roots", Type: "tags",
				Placeholder: "An absolute path",
				Help:        "Only needed for a symlink inside the tree that points somewhere this project should still reach. A registered directory already follows its own links.",
				ShowWhen:    "kind:" + KindRoot},
			ui.FormField{Field: "allowed_users", Label: "Also allow", Type: "tags",
				Placeholder: "A username. None: admins only.",
				Help:        "Named users can open and read this project. Editing it, and running its commands, stays with admins.",
				ShowWhen:    "kind:" + KindRoot},
		)
	}
	fields = append(fields,
		ui.FormField{Field: "reasoning", Label: "Reasoning", Type: "select",
			Default: ReasoningStandard,
			Options: []ui.SelectOption{
				{Value: ReasoningStandard, Label: "Standard: the local worker"},
				{Value: ReasoningQuick, Label: "Quick: the worker, thinking less"},
				{Value: ReasoningLead, Label: "Lead: escalate the reasoning turn"},
			},
			Help: "How hard to think on this project. Lead is worth it for work spanning many files and wasted on a one-line fix; Quick trades depth for speed. A private agent or a private turn stays on the worker whichever is chosen."},
		ui.FormField{Field: "instructions", Label: "Notes for the assistant", Type: "textarea", Rows: 5,
			Placeholder: "Anything true about working on this project here that isn't written down in the repository.",
			Help:        "Injected into every turn, alongside the project's own CLAUDE.md if it has one. Use it for what the repo cannot say: which build command works on this box, a package that is mid-migration, a staging URL."},
	)
	if editing {
		fields = append(fields,
			ui.FormField{Field: "check_command", Label: "Check", Type: "text",
				Placeholder: "The command that builds this project and runs its tests",
				Help:        "Runs in the sandbox, with this project's own package cache and downloads through gohort. After Anvil changes files it runs the check before it finishes, and keeps working while the check fails."},
			ui.FormField{Field: "check_manual", Label: "Run the check only when Anvil asks", Type: "toggle",
				Help: "For a project whose tests are slow: Anvil runs the check with its check tool, and not on its own after every change."},
			ui.FormField{Field: "run_without_asking", Label: "Run commands without asking", Type: "toggle", ShowWhen: "kind:" + KindClone,
				Help: "The assistant runs its commands (a build, a test) without asking you first. They run sandboxed, without network, in this checkout only. Asked about either way: deleting a file, and the commands set on this project."},
			dependenciesField(others), commandsField())
	}

	post := "api/projects"
	form := ui.FormPanel{
		PostURL:     post,
		Fields:      fields,
		SubmitLabel: submitLabel(editing),
		Invalidate:  []string{"api/projects"},
	}
	return form
}

// dependenciesField links other projects in as readable source.
//
// A picker over projects rather than a path box, and that is the security
// property rather than a convenience: you can only choose something you can
// already open, so linking grants nothing new. Naming a directory instead
// would be a read-anything-on-this-host control on any checkout a user owns.
func dependenciesField(others []ui.SelectOption) ui.FormField {
	return ui.FormField{
		Field: "dependencies", Label: "Dependencies", Type: "rows", AddLabel: "＋ Link a project",
		Help: "Other projects this one is built against, readable from here under a prefix, such as read_file(\"mylib/log/log.go\"). Only projects you can already open are listed.",
		Columns: []ui.FormField{
			{Field: "project_id", Label: "Project", Type: "select", Options: others},
			{Field: "prefix", Label: "Addressed as", Type: "text",
				Placeholder: "defaults to the project's name",
				Help:        "The first path segment that selects this tree."},
			{Field: "writable", Label: "Allow edits", Type: "toggle",
				Help: "Off by default. Reading a dependency to understand it is the common case; changing one should be deliberate. Cannot grant more than your access to that project already allows."},
		},
	}
}

// commandsField is the operator's command list — build, test, restart —
// rendered as a repeating group rather than a JSON textarea.
//
// This was the last thing in the project record with no UI at all: commands
// have been settable through the API since the app was written and there has
// never been a way to type one in. The gate is the field worth reading twice:
// naming another command here is what makes THIS one refuse to run when that
// one fails, which is how a restart is kept off a red build.
func commandsField() ui.FormField {
	return ui.FormField{
		Field: "actions", Label: "Commands", Type: "rows", AddLabel: "＋ Add a command",
		Help: "Shown under Run, and offered to the assistant when you let them be. They execute in the project root with a real environment: these are your commands, not the model's.",
		Columns: []ui.FormField{
			{Field: "label", Label: "Name", Type: "text", Placeholder: "Build"},
			{Field: "command", Label: "Command", Type: "text", Placeholder: "go build ./..."},
			{Field: "gate_id", Label: "Only if this passes", Type: "text",
				Placeholder: "id of another command",
				Help:        "That command runs first, and this one is refused when it fails. How a restart is kept off a red build."},
			{Field: "confirm", Label: "Ask first", Type: "text", Placeholder: "Restart the server?"},
			// A detached command is launched and not waited for, so it has no
			// timeout, and it is never offered to the assistant; the hint is
			// read only for a command the assistant may run.
			{Field: "timeout_sec", Label: "Timeout (s)", Type: "number", Min: 0, Max: 3600, ShowWhen: "!detached"},
			{Field: "detached", Label: "Detached", Type: "toggle",
				Help: "For a command that stops the server running it. It reports only that it launched, and is never offered to the assistant."},
			{Field: "expose_to_agent", Label: "Assistant may run it", Type: "toggle", ShowWhen: "!detached"},
			{Field: "agent_hint", Label: "Told to the assistant as", Type: "text", Placeholder: "runs the unit tests and returns failures", ShowWhen: "expose_to_agent;!detached"},
		},
	}
}

func submitLabel(editing bool) string {
	if editing {
		return "Save changes"
	}
	return "Add project"
}

func tokenPlaceholder(editing bool) string {
	if editing {
		return "Leave blank to keep the stored token"
	}
	return "Only for a private repository"
}

// lockKindWhenEditing stops the kind being changed on an existing project.
// The save handler refuses it anyway; locking the control says so before the
// user has typed the rest of a form that will be rejected.
func lockKindWhenEditing(editing bool) string {
	if editing {
		return "id"
	}
	return ""
}

func helpForKind(editing bool) string {
	if editing {
		return "A project's kind cannot change. Remove it and add it again to switch."
	}
	return "A repository is checked out for you. A directory is edited in place."
}

// anvilHead wires the browser half: the diff card renderer plus the three
// client actions. Built through ui.Head rather than a hand-written script
// blob, so the framework assembles the registration and its readiness guard.
//
// The one generated value is ANVIL_ACTIONS — each project's commands, so the
// Run modal can offer the right ones for whichever project the picker is on.
func anvilHead(projects []Project, user string, isAdmin bool) *ui.Head {
	var actions strings.Builder
	actions.WriteString("window.ANVIL_ACTIONS = {")
	for i, p := range projects {
		if i > 0 {
			actions.WriteString(",")
		}
		fmt.Fprintf(&actions, "%s:[", jsQuote(p.ID))
		if actionsAllowed(p, user, isAdmin) {
			for j, a := range p.Actions {
				if j > 0 {
					actions.WriteString(",")
				}
				fmt.Fprintf(&actions, "{id:%s,label:%s,confirm:%s,detached:%t}",
					jsQuote(a.ID), jsQuote(a.Label), jsQuote(a.Confirm), a.Detached)
			}
		}
		actions.WriteString("]")
	}
	actions.WriteString("};")

	return ui.NewHead().
		CSS(anvilCSS).
		JS(actions.String()).
		JS(anvilCurrentProjectJS).
		BlockRenderer("anvil_edit", anvilEditBlockJS).
		BlockRenderer(checkBlock, anvilCheckBlockJS).
		BlockRenderer(planBlock, anvilPlanBlockJS).
		BlockRenderer(commitBlock, anvilCommitBlockJS).
		JS(anvilStepSummariesJS).
		JS(anvilHabitsJS).
		ClientAction("anvil_run", anvilRunJS).
		ClientAction("anvil_history", anvilHistoryJS).
		ClientAction("anvil_download_changes", anvilDownloadJS("patch", "Making the patch...")).
		ClientAction("anvil_download_bundle", anvilDownloadJS("bundle", "Making the bundle...")).
		ClientAction("anvil_commit", anvilGitDialogJS("commit", "Commit")).
		ClientAction("anvil_publish", anvilGitDialogJS("publish", "Publish")).
		ClientAction("anvil_git_done", anvilGitDoneJS).
		ClientAction("anvil_app_preview", anvilAppPreviewJS).
		ClientAction("anvil_app_install", anvilAppInstallJS).
		ClientAction("anvil_app_history", anvilAppHistoryJS).
		ClientAction("anvil_app_install_new", anvilAppInstallNewJS).
		ClientAction("anvil_permissions", anvilPermissionsJS).
		ClientAction("anvil_notes", anvilNotesJS).
		ClientAction("anvil_settings", anvilSettingsJS).
		JS(anvilGitChipJS)
}

// jsQuote renders a Go string as a JS string literal. Hand-rolled rather than
// json.Marshal so the output is readable in view-source, and so a stray
// backtick in an operator's label cannot terminate a template literal — the
// generated script uses only single quotes.
func jsQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '<':
			b.WriteString("\\x3c") // never let a label close the script tag
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
