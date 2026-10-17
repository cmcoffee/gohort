// The admin surface: a table of registered stores plus an add form.
//
// Deliberately plain. A store is four fields, an operator sets one up
// once, and the only thing that needs care is telling them immediately
// when a path is wrong — which the save endpoint does by returning the
// validator's own sentence rather than a status code.

package filestore

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/ui"
)

// Every URL here is ABSOLUTE. The section renders on the ADMIN page, not
// on this app's own, so a relative "api/stores" resolves against /admin
// and 404s.
func (T *FileStoreApp) adminSection() ui.Section {
	return ui.Section{
		Group:    "Files",
		Title:    "File stores",
		Wide:     true,
		Subtitle: "Folders on this server agents can search and read, and act on only through the tools you give them.",
		Detail:   "Attach a store to an agent, under Sources, and it gets tools to list what is there, search it by regular expression, and read a window around a hit, never a whole file.\n\nSubfolders are optional: a parent whose subfolders are per-ticket or per-run works, and so does a flat folder of files. Logs are the obvious case, but anything you would grep rather than embed belongs here: config trees, exports, source dumps.\n\nTo RUN something against a folder, to download into it, unpack an archive or run an extractor, register the binary as a command on the store's row. A person runs it from there; to let agents run it, map it with Builder under Map with Builder and turn Agents on, and an agent can then run only the lines you approved. Files move between stores by Pull, and an app gets a store only when you grant it one under Apps that may use it.",
		Body: ui.Stack{Children: []ui.Component{
			ui.Table{
				Source: "/filestore/api/stores",
				RowKey: "slug",
				// What tells stores apart on the first line; what describes
				// one on the second; everything else in Manage, under About.
				// Eleven facts across one line were unreadable.
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "path", Mute: true, Flex: 2},
					// Folder count is the fact that says whether the path is
					// right. A store reading "unreadable" here is a typo
					// caught before an agent is attached to it.
					{Field: "folders", Label: "Subfolders", Mute: true},
					{Field: "keeps", Label: "Keeps", Mute: true},
					{Field: "description", Label: "Notes", Mute: true, Flex: 3, Line: 2},
					{Field: "connected", Label: "Connected", Mute: true, Flex: 2, Line: 2},
				},
				RowActions: []ui.RowAction{
					// ONE expander, not four. Everything about a folder lives on
					// the folder: its settings, the commands registered against
					// it, the toolboxes those commands were mapped into, and who
					// may reach it.
					//
					// It used to be four separate row actions with no visible
					// relationship, and the result of mapping a command landed
					// somewhere else entirely — in the global tool list, attached
					// to nothing, reading as orphaned, waiting for an admin to
					// find a second expander and bind it. Four expanders on one
					// subject is what made that possible: nothing on screen said
					// these were parts of one thing.
					ui.Expand("Manage", ui.Stack{Children: []ui.Component{
						ui.Card{HTML: folderPanelHeading("About", "")},
						ui.DisplayPanel{
							Source: "/filestore/api/stores?slug={slug}&view=about",
							Pairs: []ui.DisplayPair{
								// The handle, because the tool names are built
								// from it and NOT from the name. Renaming a store
								// leaves search_<original> in place, which is
								// correct (attachments and frozen path_scope refs
								// are keyed on it) and looks broken unless the
								// page says so.
								{Label: "Agent tools", Field: "tools", Mono: true},
								// What this folder CARRIES, as opposed to what it
								// mints: a mapped command is a capability of a
								// folder, not a tool of the installation.
								{Label: "Agent commands", Field: "carries"},
								{Label: "Who may reach it", Field: "assigned"},
								{Label: "Apps that may use it", Field: "apps"},
								{Label: "Connected", Field: "connected"},
								// Stated in full because it is the one setting
								// whose effect is destructive and whose default
								// (forever) is invisible.
								{Label: "Keeps", Field: "retention"},
							},
						},

						ui.Card{HTML: folderPanelHeading("Commands",
							"Binaries registered against this folder. A person runs one from Files. Map a command with Builder and it becomes a tool as well: turn Agents on and every agent that reaches this folder can call it; turn it off and the mapping is kept but nothing can use it.")},
						ui.Table{
							Source: "/filestore/api/commands?slug={slug}",
							RowKey: "cmd",
							Columns: []ui.Col{
								{Field: "label", Label: "Command", Flex: 1},
								{Field: "command", Mute: true, Flex: 2},
								{Field: "phases", Label: "Asks for input", Mute: true},
								// What mapping produced, on the command it came
								// from. There is nowhere else it could be: the
								// actions are fields on this record.
								{Field: "mapped", Label: "As a tool", Mute: true, Flex: 2},
								{Field: "uses", Label: "Uses", Mute: true, Flex: 2, Line: 2},
							},
							RowActions: []ui.RowAction{
								// The approval, on the row it approves. Shown
								// only once there is something to approve — a
								// switch offering to enable nothing is a
								// question the reader cannot answer.
								{Type: "toggle", Field: "approved", Label: "Agents",
									OnlyIf: "mapped_ok",
									PostTo: "/filestore/api/commands/approve?id={cmd}"},
								// How an agent's run of it goes, beside whether
								// agents may run it: on, the agent hears it has
								// started and is woken with the result. Not an
								// approval, so flipping it leaves Agents as is.
								{Type: "toggle", Field: "background_on", Label: "Background",
									OnlyIf: "mapped_ok",
									PostTo: "/filestore/api/commands/background?id={cmd}"},
								//
								// Addressed by {cmd}, never by {name}. A nested
								// table's placeholders are filled TWICE: once at
								// expand time against the store row, and again
								// per inner row. Whatever the store row can
								// answer, it answers first — and a store row has
								// a name, so {name} here arrives as the FOLDER's
								// name and the command is never found. {cmd} is
								// the composite slug/name the row already
								// carries, and no store row defines it. Enforced
								// by TestInnerRowKeysAreNotShadowedByTheStoreRow.
								// Read before rewrite. The row can only say
								// THAT something is mapped; this says what an
								// agent would actually run, which is the
								// question "do I need to re-map this?" is
								// really asking. Absolute URL, not relative:
								// this panel opens inside a table on whatever
								// page mounted it, and a relative source would
								// resolve against THAT page.
								// How Builder and agents may use it: Open, or
								// only the lines written here.
								ui.Expand("Uses", ui.FormPanel{
									Source:      "/filestore/api/commands/uses?id={cmd}",
									PostURL:     "/filestore/api/commands/uses?id={cmd}",
									SubmitLabel: "Save uses",
									Fields:      commandUsesFields(),
									Invalidate:  []string{"/filestore/api/commands"},
								}),
								ui.Expand("Mapping", ui.DisplayPanel{
									Source: "/filestore/api/commands/mapping?id={cmd}",
									Pairs: []ui.DisplayPair{
										{Label: "Tool name", Field: "tool_name", Mono: true},
										{Label: "State", Field: "state", StatusField: "state_status"},
										{Label: "What it is for", Field: "tool_desc"},
										{Label: "Binary", Field: "binary", Mono: true},
										// One entry per action: what an agent
										// calls, the line it runs, where it
										// runs, and which parameter carries
										// the folder.
										{Label: "Actions", Field: "actions", Items: []ui.DisplayPair{
											{Field: "name", Mono: true},
											{Label: "runs", Field: "command", Mono: true, Block: true},
											{Label: "in", Field: "runs_in", Mono: true},
											{Label: "takes", Field: "params", Mono: true},
											{Label: "desc", Field: "description"},
											{Label: "disabled", Field: "disabled"},
										}},
									},
								}),
								// The admin runs the command to show how it is
								// used; each run becomes an example Map learns
								// from (command_examples.go).
								ui.Expand("Show it how", ui.Stack{Children: []ui.Component{
									ui.FormPanel{
										Source:    "/filestore/api/map/try-args?id={cmd}",
										TestURL:   "/filestore/api/map/try?id={cmd}",
										TestLabel: "Run",
										Fields: []ui.FormField{
											{Field: "args", Type: "text", Label: "Arguments", Placeholder: "download --src=/Diag/dump.zip --dst=RUN-1",
												Help:   "What you would type after the command. Run executes it in this store's folder, with no shell, and shows what it prints. Builder learns from each run.",
												Detail: "Each run is kept as an example for Map with Builder: it reads what you ran and what it printed, and works out the syntax from it, turning the parts that change into placeholders. Quote a value with spaces in it. Nothing is expanded: no wildcards, variables or pipes. Builder can try the command itself too; these runs show it how you use it."},
											{Field: "step", Type: "select", Label: "This run is", Options: []ui.SelectOption{
												{Value: "new", Label: "A new use of the command"},
												{Value: "next", Label: "The next step of the last use"},
											},
												Help: "A command used in steps (it prints a challenge, then takes the response) is run once per step: the first as a new use, each following one as its next step. Builder sees them as one use, in order."},
											{Field: "note", Type: "text", Label: "Note (optional)", Placeholder: "prints the challenge",
												Help: "What this run shows, for Builder: which step it is, what a value means. Worth it for a command used in steps, like a challenge and then its response."},
										},
									},
									// The runs kept, to review before Builder
									// learns from them: a mistaken run, removed
									// here, teaches it nothing.
									ui.Table{
										Source:        "/filestore/api/map/examples?id={cmd}",
										RowKey:        "eid",
										AutoRefreshMS: 4000,
										Columns: []ui.Col{
											{Field: "where", Label: "Run", Mute: true},
											{Field: "args", Label: "Arguments", Flex: 3},
											{Field: "exit", Label: "Exit", Mute: true},
											{Field: "note", Label: "Note", Mute: true, Flex: 2, Line: 2},
											{Field: "output", Label: "Ended with", Mute: true, Flex: 3, Line: 2},
										},
										RowActions: []ui.RowAction{
											ui.Expand("Edit", ui.FormPanel{
												Source:      "/filestore/api/map/examples?id={cmd}&eid={eid}",
												PostURL:     "/filestore/api/map/examples?id={cmd}&eid={eid}",
												SubmitLabel: "Save",
												Fields: []ui.FormField{
													{Field: "note", Type: "textarea", Label: "Note", Rows: 2,
														Help: "Context for Builder: which step this is, what a value means, what to watch for."},
													{Field: "use", Type: "number", Label: "Use", Min: 1,
														Help: "Which use this run is a step of. Change it to move the run into another use."},
												},
												Invalidate: []string{"/filestore/api/map/examples"},
											}),
											{Type: "button", Label: "Remove", Method: "DELETE",
												PostTo:     "/filestore/api/map/examples?id={cmd}&eid={eid}",
												Variant:    "danger",
												Confirm:    "Remove this run? Builder will no longer learn from it.",
												Optimistic: true},
										},
										EmptyText: "No runs yet. Run the command above to show how it is used.",
									},
								}}),
								ui.Expand("Map with Builder", ui.AgentLoopPanel{
									SendURL: "/filestore/api/map/chat/send?id={cmd}",
									// This command's mapping conversations, to go
									// back to one. {id} is the session the panel
									// opens, which is why the row is keyed {cmd}.
									ListURL:   "/filestore/api/map/chat/sessions?id={cmd}",
									LoadURL:   "/filestore/api/map/chat/sessions/{id}?cmd={cmd}",
									DeleteURL: "/filestore/api/map/chat/sessions/{id}?cmd={cmd}",
									CancelURL: "/filestore/api/map/chat/cancel?id={cmd}",
									// A mapping lands on this command's row:
									// the Agents switch appears once there is
									// something to approve. The table refetches
									// when this panel closes.
									Invalidate:   []string{"/filestore/api/commands?slug={slug}"},
									ListTitle:    "Earlier mappings",
									NewLabel:     "New",
									ListPosition: "top",
									Markdown:     true,
									LockActivity: true,
									// A pane, not a page. This opens inside a row
									// of a table inside a row of another table;
									// the viewport-tall default buried the folder
									// it belongs to.
									Height: "420px",
									// The three things worth saying, as buttons.
									// Mapping has a known shape — look, map,
									// correct — and asking someone to type "map
									// out this command" is asking them to guess
									// the wording of a job the page already knows.
									// The composer stays for everything after.
									Actions: []ui.ToolbarAction{
										{Label: "Map it", Variant: "primary",
											Title:  "Read its help, try what it needs to, and write down what it can do",
											Prompt: "Map this command. Read its help, try whatever you need to understand what it does, then write down what it can do. Say what you verified and what you did not."},
										{Label: "Show its help",
											Title:  "Just run the help flag and show the output",
											Prompt: "Run this command's help flag and show me exactly what it printed. Do not propose anything yet."},
										{Label: "Learn from my examples",
											Title:  "Map the command from the runs made in Show it how",
											Prompt: "Read the examples I ran (admin_examples) and map the command from them: one action per kind of use you see, with the parts that change between runs as placeholders. Say what each example taught you and what you are unsure of."},
										{Label: "Refine",
											Title:  "Check what you wrote against what the command really does, and correct it",
											Prompt: "Check what you wrote against what the command actually does (run it again where you were guessing), and propose a corrected version."},
									},
									EmptyText: "Builder maps this command: what it works out lands on this row as the actions an agent can call. " +
										"It reads the runs you made in Show it how, and can run the command with any arguments to learn it, asking you before a run that would change anything. " +
										"Nothing it writes can be called until you turn Agents on for this command; that switch is the approval.",
									Placeholder: "Or ask Builder something…",
								}),
								{Type: "button", Label: "Delete", Method: "DELETE",
									PostTo:     "/filestore/api/commands?id={cmd}",
									Variant:    "danger",
									Confirm:    "Remove this command? Its mapping goes with it; the binary is left alone.",
									Optimistic: true},
							},
							EmptyText: "No commands registered against this folder.",
						},
						ui.ModalButton{
							Label:    "+ Add a command",
							Title:    "Register a command against this folder",
							Subtitle: "An absolute path on this server, run as `<command> <folder>` with no shell.",
							Width:    "620px",
							Body: ui.FormPanel{
								PostURL:     "/filestore/api/commands?slug={slug}",
								SubmitLabel: "Add command",
								Fields:      actionFormFields(),
								Invalidate:  []string{"/filestore/api/commands"},
							},
						},

						ui.Card{HTML: folderPanelHeading("Pull files and folders in",
							"Copies a file or folder in from the store set under Settings, Pull from another store. The copy lands whole or not at all, and the other store keeps its own. Pulling a folder that is already here adds to it: new files arrive, changed ones update, nothing is removed. A file already here is replaced with the newer copy.")},
						ui.ActionList{
							Source:         "/filestore/api/pull/items?slug={slug}",
							LabelField:     "item",
							DescField:      "desc",
							HistoryField:   "history",
							PostTo:         "/filestore/api/pull?slug={slug}&item={item}",
							ButtonText:     "Pull",
							ProgressSource: "/filestore/api/pull/progress?slug={slug}&item={item}",
							CancelTo:       "/filestore/api/pull/cancel?slug={slug}&item={item}",
							EmptyText:      "Nothing to pull: either no store is set under Settings, Pull from another store, or that store is empty.",
							Invalidate:     []string{"/filestore/api/stores"},
						},

						ui.Card{HTML: folderPanelHeading("Evidence index",
							"With Index folders as evidence on (Settings), each folder is indexed for its summary and timeline as it arrives, and again when it changes. Index now does it at once, for a folder that changed by hand.")},
						ui.ActionList{
							Source:         "/filestore/api/evidence/items?slug={slug}",
							LabelField:     "item",
							DescField:      "desc",
							HistoryField:   "history",
							PostTo:         "/filestore/api/evidence/index?slug={slug}&item={item}",
							ButtonText:     "Index now",
							ProgressSource: "/filestore/api/evidence/progress?slug={slug}&item={item}",
							CancelTo:       "/filestore/api/evidence/cancel?slug={slug}&item={item}",
							EmptyText:      "Not indexing: turn on Index folders as evidence in this store's Settings, or it has no folders yet.",
						},

						ui.Card{HTML: folderPanelHeading("Apps that may use it",
							"An app granted here can read this folder from its scripts, and, as ticked, write into it (add, change and delete files), run its commands, and pull files and folders into it. The grant is the app's: it covers everyone who uses the app, whatever the list below says.")},
						ui.Table{
							Source: "/filestore/api/app-grants?slug={slug}",
							RowKey: "app",
							Columns: []ui.Col{
								{Field: "label", Label: "App", Flex: 1},
								{Field: "rights", Label: "May", Mute: true, Flex: 2},
							},
							RowActions: []ui.RowAction{
								{Type: "button", Label: "Remove", Method: "DELETE",
									PostTo:     "/filestore/api/app-grants?slug={slug}&app={app}",
									Variant:    "danger",
									Confirm:    "Remove this app's access to the store? Its scripts lose the folder and its commands at once.",
									Optimistic: true},
							},
							EmptyText: "No app may use this store.",
						},
						ui.ModalButton{
							Label:    "+ Grant an app",
							Title:    "Let an app use this store",
							Subtitle: "Reading is always included. Tick what else it may do.",
							Width:    "520px",
							Body: ui.FormPanel{
								PostURL:     "/filestore/api/app-grants?slug={slug}",
								SubmitLabel: "Grant",
								Fields: []ui.FormField{
									{Field: "app", Type: "select", Label: "App", OptionsSource: "/filestore/api/app-options"},
									{Field: "write", Type: "toggle", Label: "Write into it",
										Help: "Add, change and delete files in its folders, for a download landing here or an extraction writing beside its input."},
									{Field: "commands", Type: "toggle", Label: "Run its commands",
										Help: "The commands registered on this store, two-phase ones included (the app shows the challenge and sends the response)."},
									{Field: "pull", Type: "toggle", Label: "Pull files and folders in",
										Help: "Copy files and folders into this store from the store it pulls from (Settings, Pull from another store)."},
								},
								Invalidate: []string{"/filestore/api/app-grants"},
							},
						},

						ui.Subsection{
							Title:    "Who may reach it",
							Subtitle: "Nobody, until you add them. Each person starts Read-Only; press their level to give Full Access.",
							Detail: "Read-Only: browse, search and read the store's files. An agent of theirs attached to the store gets its list, search and read tools, and the evidence tools when the store indexes evidence.\n\n" +
								"Full Access: everything Read-Only allows, and also upload files and run the store's commands. An agent of theirs also gets the store's approved commands, and pulling from the store this one pulls from, but only when the agent's attachment allows changes.\n\n" +
								"Add people, a team, or Everyone (every account on this server). Admins are not let in by being admins: add yourself to reach the files. Apps are separate: an app's rights are granted under Apps that use it.",
							Body: ui.ACLPicker(ui.ACLPickerConfig{
								OptionsSource: "/filestore/api/access-candidates",
								RecordSource:  "/filestore/api/stores?slug={slug}",
								Field:         "members",
								// PATCH sends the lists alone. A POST carries the
								// whole record as it stood when the row opened,
								// which put back any setting saved since.
								PostTo:       "/filestore/api/stores?slug={slug}",
								Method:       "PATCH",
								Noun:         "users",
								EmptyText:    "No people or teams to add yet.",
								FlagField:    "full_access",
								FlagLabel:    "Full Access",
								FlagOffLabel: "Read-Only",
								FlagHelp:     "Read-Only browses, searches and reads. Full Access also uploads and runs the store's commands, for them and their agents. Press to switch.",
								Invalidate:   []string{"/filestore/api/stores"},
							}),
						},

						ui.Card{HTML: folderPanelHeading("Settings", "")},
						ui.FormPanel{
							// view=form leaves allowed_users out: the form posts
							// back everything it loaded, so the list would be
							// saved as it stood when the row opened.
							Source:      "/filestore/api/stores?slug={slug}&view=form",
							PostURL:     "/filestore/api/stores",
							SubmitLabel: "Save changes",
							Fields:      storeFormFields(),
						},
					}}),
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/filestore/api/stores?slug={slug}",
						Variant:    "danger",
						Confirm:    "Remove this file store? The folder and its files are left alone. Its registered commands are removed, and agents attached to it lose its tools.",
						Optimistic: true},
				},
				EmptyText: "No file stores yet. Add a folder on this server for an agent to search.",
			},
			ui.ModalButton{
				Label:    "Add file store",
				Title:    "Add a file store",
				Subtitle: "A folder on this server. Its subfolders, if it has any, become the groups an agent can scope a search to.",
				Variant:  "primary",
				Width:    "560px",
				Body: ui.FormPanel{
					PostURL:     "/filestore/api/stores",
					SubmitLabel: "Add store",
					Fields:      storeFormFields(),
				},
			},
		}},
	}
}

// actionFormFields is the add form for a store action.
func actionFormFields() []ui.FormField {
	return append([]ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "decrypt",
			Help: "Short handle for the command, snake_case. It is how the endpoint names it."},
		{Field: "label", Type: "text", Label: "Button label", Placeholder: "Unpack archive",
			Help:   "What the button says.",
			Detail: "Name it for what it DOES to the folder, since that is what the person clicking it is deciding."},
		{Field: "command", Type: "text", Label: "Command", Placeholder: "/opt/bin/unpack",
			Help:   "Absolute path. It is called as `<command> <folder>`.",
			Detail: "For a two-phase command it is called a second time as `<command> <folder> <input>`. It runs with NO shell, so quoting and metacharacters have nowhere to happen."},
		{Field: "two_phase", Type: "toggle", Label: "Two phases (asks for input)",
			Help:   "Whether the command runs once, or twice with a value from the person in between.",
			Detail: "On, the first run prints something (a challenge, a summary, a prompt) which is shown to the person, who supplies a value; the command then runs again with it. Off, one call and the folder is ready.\n\nUse two phases when a value has to come from OUTSIDE this system and nothing here can obtain it."},
		{Field: "input_label", Type: "text", Label: "Input label", Placeholder: "Response key", ShowWhen: "two_phase",
			Help: "What the box asks for on the second phase."},
		{Field: "help", Type: "textarea", Label: "Note", Rows: 2,
			Help: "Optional. Shown beside the button: say what it does and when to reach for it."},
	}, commandUsesFields()...)
}

// commandUsesFields are what agents may run of the command, and the admin's
// guide to it.
func commandUsesFields() []ui.FormField {
	return []ui.FormField{
		{Field: "background", Type: "select", Label: "When an agent runs it",
			Options: []ui.SelectOption{
				{Value: "", Label: "Wait, then background it if it is slow"},
				{Value: "always", Label: "Always run it in the background"}},
			Help:   "Always: the agent hears it has started and carries on, and is woken with the result when it ends.",
			Detail: "For a command that is always slow, a download or an extraction. With the first choice an agent waits for it up to the detach time (Admin, Timeouts) before it moves to the background. Either way a run is stopped as stuck only when it has printed nothing and written nothing to its folders for the no-output limit, so a quiet download that keeps writing keeps going."},
		{Field: "guide", Type: "textarea", Label: "How to use it", Rows: 6,
			Placeholder: "What it is for, the order of its steps, what each value means, where its output goes, what to watch for.",
			Help:        "Optional, in markdown. Builder reads it to map the command, and agents read it where they list this store's commands.",
			Detail:      "Runs in Show it how teach Builder the syntax; this says what runs cannot: that a signature is answered by someone in support, that a response is single-use, that output lands in a folder named after the archive. Changing it does not switch Agents off: it changes what agents are told, not what they can run."},
		{Field: "allowed_uses", Type: "textarea", Label: "Allowed uses", Rows: 3,
			Placeholder: "download --src={source} --dst={folder}",
			Help:        "One per line: the arguments after the command, with {placeholders} for what the agent fills in. Builder adds the lines it maps.",
			Detail:      "Write lines here to tell Builder what to map, or leave it empty and let Builder work them out with you. Each line becomes one tool an agent can call, running exactly that line, and an agent can run nothing else. It runs directly as the server's account with no shell, the way a person's click runs, so a binary with its own saved login works. Each placeholder is filled with one value, never an option; a placeholder that names a file or folder in this store is proved to be inside it. A line with no placeholders is a fixed call.\n\nNothing reaches an agent until you turn Agents on. Changing these lines, or a re-map, turns it off again."},
	}
}

func storeFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "Build logs",
			Help:   "Shown in the agent's Sources picker, and in the tool descriptions an attached agent reads.",
			Detail: "The tool NAMES come from a handle minted from this name the first time the store is saved (\"Build logs\" becomes search_build_logs), and that handle does not change afterwards. RENAMING A STORE CHANGES THE LABEL, NOT THE TOOL NAMES.\n\nIt has to work that way: the handle is what agent attachments are keyed on and what a minted command tool's frozen path_scope names, so moving it would break every approved tool pointed at this store. The Agent tools column shows the names in force."},
		{Field: "path", Type: "text", Label: "Folder", Placeholder: "/var/log/builds",
			Help:   "Absolute path on this server. Read-only: nothing here ever writes to it.",
			Detail: "The folder itself is what an agent attaches to; its subfolders, if any, are what a search can be scoped to."},
		{Field: "evidence", Type: "toggle", Label: "Index folders as evidence",
			Help:   "Index each subfolder like an evidence bundle, and give attached agents a summary and a timeline of it. Off by default.",
			Detail: "Each subfolder (a case, a run, a dump) is indexed as it arrives: format, line count, the period it covers, which hosts wrote it and how many errors and warnings it holds, with timestamps parsed so files can be merged in time order. Attached agents get summarize_<store> and timeline_<store>, which take the folder by name.\n\nThe folder itself is never changed. The index is a second copy, kept encrypted, of up to 8 GB per folder: a changed folder is indexed again, a deleted one's index is wiped, and turning this off wipes them all. Folders that change some other way (a command's download, a copy by hand) are picked up within 15 minutes, or at once when an agent asks about one."},
		{Field: "ephemeral", Type: "toggle", Label: "Temporary store",
			Help:   "Delete each folder automatically once nothing in it has changed for a set time.",
			Detail: "For a working area whose contents are copied onward, such as raw downloads that are extracted and pulled into another store. Folders go on a timer, checked every 15 minutes, with no admin sweep. A folder that a command, a copy or an upload is using is never removed, whatever its age.\n\nLeave it off for a store that holds the only copy of something: those wait for an admin."},
		{Field: "lifetime_hours", Type: "number", Label: "Keep each folder for (hours)", Min: 1, Placeholder: "24", ShowWhen: "ephemeral",
			Help: "After its last change. 24 when left empty."},
		{Field: "retention_days", Type: "number", Label: "Delete folders older than (days)", Min: 0, ShowWhen: "!ephemeral",
			Help:   "0 keeps everything forever, which is the default.",
			Detail: "A number makes a subfolder ELIGIBLE for deletion once nothing in it has changed for that long. Nothing is deleted on a timer: an admin runs the sweep from Maintenance, where a dry run lists exactly what the delete would remove.\n\nAge is read from the folder and its immediate contents, so a write buried deep inside may not refresh it. Leave headroom rather than setting this to the exact age you care about."},
		{Field: "max_gb", Type: "number", Label: "Size cap (GB)", Min: 0,
			Help:   "0 is no cap. Work that would take the store past it is refused.",
			Detail: "Uploads, commands and copies into the store are refused once it is full, saying how full it is. Nothing is deleted to make room. Separate from the free-space floor every write on the disk keeps (Admin, Limits)."},
		{Field: "pull_from", Type: "select", Label: "Pull from another store", OptionsSource: "/filestore/api/store-options",
			Help:   "Optional: another store this one copies files and folders in from.",
			Detail: "For a store agents read that takes what it holds from a private working store. The link is the permission: an app granted pulling here, or an attached agent of someone with Full Access, can copy a file or folder from that store by name, seeing that store's names but never its contents. Copies, never moves: the other store keeps its own until its lifetime or retention removes it."},
		{Field: "pull_subpath", Type: "text", Label: "Part of each folder to copy", Placeholder: "prepared", ShowWhen: "pull_from",
			Help: "When a folder is pulled, the part of it to copy: the subfolder holding what was prepared. prepared when empty; a single dot copies the whole folder. A file is always copied whole."},
		{Field: "description", Type: "textarea", Label: "What lands here", Rows: 2,
			Help:   "Optional, and worth writing: it tells the agent when to reach for THIS store.",
			Detail: "It is pasted into the agent's tool descriptions. For example: \"Build logs, one folder per CI run, copied in by the build server.\""},
		{Field: "slug", Type: "hidden"},
	}
}

// adminHeadHTML is empty: the section uses only stock components. Kept
// as a function so adding a client action later does not change the
// registration site.
func adminHeadHTML() string { return "" }

// --- endpoints --------------------------------------------------------

// Routes registers the store CRUD the admin section talks to.
func (T *FileStoreApp) Routes() {
	// Routes runs once at startup with T.DB wired, which is the earliest point
	// the stored commands can be reached — so it is where the table rename gets
	// finished. See MigrateStoreCommands.
	MigrateStoreCommands(T.DB)
	MigrateToolboxesOntoCommands(T.DB)
	MigrateCommandUses(T.DB)
	// Temporary stores clean up after themselves; see sweepTemporaryStores.
	go T.sweepTemporaryStores()
	// Evidence indexes follow their folders; see evidence.go.
	go T.sweepEvidenceLoop()
	T.HandleFunc("/api/stores", T.handleStores)
	T.HandleFunc("/api/upload", T.handleUpload)
	T.HandleFunc("/api/commands/run", T.handleCommand)
	T.HandleFunc("/api/commands", T.handleCommands)
	T.HandleFunc("/api/map/chat/send", T.handleMapChat)
	T.HandleFunc("/api/map/chat/sessions", T.handleMapSessions)
	T.HandleFunc("/api/map/chat/sessions/", T.handleMapSessions)
	T.HandleFunc("/api/map/chat/cancel", T.handleMapCancel)
	T.HandleFunc("/api/commands/approve", T.handleCommandApprove)
	T.HandleFunc("/api/commands/background", T.handleCommandBackground)
	T.HandleFunc("/api/commands/uses", T.handleCommandUses)
	T.HandleFunc("/api/map/try", T.handleTryCommand)
	T.HandleFunc("/api/map/try-args", T.handleTryArgs)
	T.HandleFunc("/api/map/examples", T.handleExamples)
	T.HandleFunc("/api/commands/mapping", T.handleCommandMapping)
	T.HandleFunc("/api/folders", T.handleFolders)
	T.HandleFunc("/api/pull", T.handlePull)
	T.HandleFunc("/api/evidence/", T.handleEvidence)
	T.HandleFunc("/api/store-options", T.handleStoreOptions)
	T.HandleFunc("/api/access-candidates", T.handleAccessCandidates)
	T.HandleFunc("/api/app-grants", T.handleAppGrants)
	T.HandleFunc("/api/app-options", T.handleAppOptions)
	// App scripts reach the stores they were granted through this.
	appstores.Provider = storeHost{T}
	registerGrantRequests(T)
	T.HandleFunc("/api/pull/", T.handlePull)
}

// handleStoreOptions lists the stores for the Pull from another store picker, with a
// first choice of none.
// handleAccessCandidates is who a store's access lists can name: Everyone,
// each approved person, and each team that can carry a share (the teams a
// person's access is looked up under, ShareRecipientKeys).
//
// The admin's own account says so: reaching the files takes an entry, admins
// included, so the one they most often need to add is themselves.
func (T *FileStoreApp) handleAccessCandidates(w http.ResponseWriter, r *http.Request) {
	me, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	out := []map[string]any{{"value": accessEveryone, "label": "Everyone", "desc": "every account on this server"}}
	if AuthDB == nil {
		writeJSON(w, out)
		return
	}
	var people []map[string]any
	json.Unmarshal(UserCandidatesJSON(AuthDB(), ""), &people)
	for _, p := range people {
		if v, _ := p["value"].(string); strings.EqualFold(v, me) {
			label, _ := p["label"].(string)
			if label == "" {
				label = v
			}
			p["label"] = label + " (your account)"
			// Right after Everyone, where it is found without looking.
			out = append(out[:1], append([]map[string]any{p}, out[1:]...)...)
			continue
		}
		out = append(out, p)
	}
	for _, g := range LoadAppGroups(AuthDB()) {
		if g.Shareable {
			out = append(out, map[string]any{"value": "group:" + g.ID, "label": "Team: " + g.Name})
		}
	}
	writeJSON(w, out)
}

func (T *FileStoreApp) handleStoreOptions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok || !adminOnly(w, r) {
		return
	}
	out := []map[string]any{{"value": "", "label": "(none)"}}
	for _, st := range ListStores(T.DB) {
		label := st.Name
		if st.Ephemeral {
			label += " (temporary)"
		}
		out = append(out, map[string]any{"value": st.Slug, "label": label})
	}
	writeJSON(w, out)
}

// handleStores serves the table (GET), the editor (GET with slug),
// upserts (POST) and removal (DELETE).
//
// Admin-only: these paths name and read arbitrary server directories, so
// the gate is the same one the admin page itself uses.
func (T *FileStoreApp) handleStores(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if !adminOnly(w, r) {
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))

	switch r.Method {
	case http.MethodGet:
		if slug != "" {
			st, found := LoadStore(T.DB, slug)
			if !found {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("view") == "about" {
				writeJSON(w, T.storeAbout(st))
				return
			}
			if r.URL.Query().Get("view") == "form" {
				// The settings form's load, without the list the picker
				// beside it owns (see the form's Source).
				var out map[string]any
				raw, _ := json.Marshal(st)
				_ = json.Unmarshal(raw, &out)
				delete(out, "members")
				delete(out, "full_access")
				delete(out, "app_grants") // the grants section owns these
				writeJSON(w, out)
				return
			}
			writeJSON(w, st)
			return
		}
		writeJSON(w, T.storeRows())
	case http.MethodPost, http.MethodPatch:
		// Decoded ONTO the stored record, not into a blank one. Two editors
		// now write this record — the form (name, path, retention…) and the
		// access picker (members, full_access) — and neither sends the other's
		// fields. Into a blank Store, whichever saved second would silently
		// erase what the first had just set: json.Unmarshal leaves absent
		// keys alone, so starting from what is stored makes a partial save
		// mean "change these", which is what both callers intend.
		//
		// A key that IS present still wins, empty included, so unticking the
		// last user clears the list rather than being read as "unchanged".
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxStoreBodyBytes))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		st := Store{}
		found := false
		if s := storeSlugOf(raw, slug); s != "" {
			st, found = LoadStore(T.DB, s)
		}
		// A PATCH changes a store that exists; it never creates one.
		if r.Method == http.MethodPatch && !found {
			http.NotFound(w, r)
			return
		}
		wasEvidence := st.Evidence
		if err := json.Unmarshal(raw, &st); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		saved, err := SaveStore(T.DB, st)
		if err != nil {
			// The validator's sentence IS the message: "that path is a
			// file, not a folder of bundles" is what fixes the mistake,
			// and a 400 with no body is what makes someone guess.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[filestore] %s saved store %q (%s)", user, saved.Name, saved.Path)
		// The index is a second copy of the folder: with the switch off it
		// goes, rather than lingering encrypted where nothing reads it.
		if wasEvidence && !saved.Evidence {
			if n := wipeStoreEvidence(T.DB, saved.Slug); n > 0 {
				Log("[filestore] %s: evidence indexing off, %d folder index(es) wiped", saved.Slug, n)
			}
		}
		writeJSON(w, saved)
	case http.MethodDelete:
		DeleteStore(T.DB, slug)
		wipeStoreEvidence(T.DB, slug)
		Log("[filestore] %s removed store %q", user, slug)
		writeJSON(w, map[string]any{"deleted": slug})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maxStoreBodyBytes caps a store save. A store is a handful of short fields;
// anything past this is a mistake or an attempt, and neither deserves memory.
const maxStoreBodyBytes = 1 << 20

// storeSlugOf finds which store a save is FOR: the query parameter when the
// caller addressed one (?slug=), otherwise the slug carried in the body (the
// edit form posts it as a hidden field). Empty means a new store, which has
// nothing to merge onto.
func storeSlugOf(body []byte, fromQuery string) string {
	if s := strings.TrimSpace(fromQuery); s != "" {
		return s
	}
	var probe struct {
		Slug string `json:"slug"`
	}
	if json.Unmarshal(body, &probe) == nil {
		return strings.TrimSpace(probe.Slug)
	}
	return ""
}

// storeRows renders the admin table. Its own method because the row is
// where two facts have to be stated that are nowhere else on the page:
// who may reach a store, and what its tools are actually called.
func (T *FileStoreApp) storeRows() []map[string]any {
	rows := make([]map[string]any, 0)
	all := ListStores(T.DB)
	for _, st := range all {
		// Folder count is the fact that says whether the path is right. A
		// store reading "unreadable" here is a typo caught before an agent
		// is attached to it.
		folders := "unreadable"
		if n, err := CountFolders(st.Path); err == nil {
			folders = strconv.Itoa(n)
		}
		rows = append(rows, map[string]any{
			"slug": st.Slug, "name": st.Name, "path": st.Path,
			"description": st.Description, "folders": folders,
			"assigned": st.accessText(),
			// From the same definition ItemTools builds from. These are
			// the names in force, which after a rename are not the names
			// the Name column would suggest.
			"tools": strings.Join(storeToolsInForce(st), ", "),
			// What this folder CARRIES, as opposed to what it mints. A mapped
			// command is deliberately absent from the deployment tool list — it
			// is a capability of a folder, not a tool of the installation — so
			// the folder is the only place it can be seen, and a stores list
			// that did not say would make a folder holding four capabilities
			// look identical to one holding none.
			"carries": storeCarriesLabel(T.DB, st.Slug),
			// Stated on the row because it is the one setting whose effect
			// is destructive and whose default (forever) is invisible.
			"retention": retentionLabel(st),
			"keeps":     keepsLabel(st),
			// The stores and apps this one works with, read from both ends: a
			// pull link is set on the store that pulls, and the store it pulls
			// from would otherwise look unconnected, as if it could be changed
			// or removed on its own.
			"connected": connectedLabel(st, all),
		})
	}
	return rows
}

// connectedLabel says which stores this one pulls from and feeds, and which
// apps may use it: "pulls prepared/ from Intake; used by Diag Prep". Empty
// when it stands alone.
func connectedLabel(st Store, all []Store) string {
	var parts []string
	if strings.TrimSpace(st.PullFrom) != "" {
		from := st.PullFrom + " (missing)"
		for _, o := range all {
			if o.Slug == st.PullFrom {
				from = o.Name
			}
		}
		what := st.pullSubpath() + "/"
		if what == "/" {
			what = "whole folders and files"
		}
		parts = append(parts, "pulls "+what+" from "+from)
	}
	var feeds []string
	for _, o := range all {
		if o.Slug != st.Slug && o.PullFrom == st.Slug {
			feeds = append(feeds, o.Name)
		}
	}
	if len(feeds) > 0 {
		parts = append(parts, "feeds "+strings.Join(feeds, ", "))
	}
	var apps []string
	for _, g := range st.AppGrants {
		apps = append(apps, appLabel(g.App))
	}
	if len(apps) > 0 {
		parts = append(parts, "used by "+strings.Join(apps, ", "))
	}
	return strings.Join(parts, "; ")
}

// keepsLabel is the retention in a few words, for the table's row:
// "forever", "90 days", "temporary, 24h". retentionLabel says it in full.
func keepsLabel(st Store) string {
	switch {
	case st.Ephemeral:
		return "temporary, " + strconv.Itoa(int(st.lifetime().Hours())) + "h"
	case st.RetentionDays > 0:
		return strconv.Itoa(st.RetentionDays) + " days"
	}
	return "forever"
}

// storeAbout is the store's details for the About panel in Manage.
func (T *FileStoreApp) storeAbout(st Store) map[string]any {
	assigned := st.accessText()
	var apps []string
	for _, g := range st.AppGrants {
		apps = append(apps, appLabel(g.App)+": "+g.rights())
	}
	appText := "none"
	if len(apps) > 0 {
		appText = strings.Join(apps, "; ")
	}
	connected := connectedLabel(st, ListStores(T.DB))
	if connected == "" {
		connected = "none"
	}
	carries := storeCarriesLabel(T.DB, st.Slug)
	if strings.TrimSpace(carries) == "" {
		carries = "none"
	}
	return map[string]any{
		"tools":     strings.Join(storeToolsInForce(st), ", "),
		"carries":   carries,
		"assigned":  assigned,
		"apps":      appText,
		"connected": connected,
		"retention": retentionLabel(st),
	}
}

// retentionLabel says what the store's window means in words, including
// the upload posture, since both live on the same row.
func retentionLabel(st Store) string {
	window := "keep forever"
	if st.RetentionDays > 0 {
		window = "delete after " + strconv.Itoa(st.RetentionDays) + "d"
	}
	if st.Ephemeral {
		window = "temporary: each folder kept " + strconv.Itoa(int(st.lifetime().Hours())) + "h after its last change"
	}
	if limit := st.capBytes(); limit > 0 {
		window += ", " + HumanSize(st.usage()) + " of " + HumanSize(limit) + " cap"
	}
	return window
}

// handleCommands is the admin CRUD behind the actions table.
//
// Admin-only, and for a sharper reason than the store list: this names a
// BINARY the server will execute. Registering one is the same decision
// as registering the directory it runs against, and neither is a user's
// to make.
func (T *FileStoreApp) handleCommands(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// READING the list is not an admin act. A page that offers a user the
	// buttons for a store has to know which buttons exist, and the answer
	// is a name and a label — the same thing they are about to click.
	// WRITING one names a binary the server will execute, which is.
	if r.Method != http.MethodGet && !adminOnly(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Filtered to what the caller can reach, and to one store when
		// asked. An admin listing everything is the same call with no
		// filter, because being an admin already means reaching every
		// store they are assigned to.
		only := strings.TrimSpace(r.URL.Query().Get("slug"))
		rows := make([]map[string]any, 0)
		for _, a := range ListStoreCommands(T.DB) {
			if only != "" && a.Slug != only {
				continue
			}
			st, found := LoadStore(T.DB, a.Slug)
			if !found || !st.AllowsUser(user) {
				continue
			}
			store := st.Name
			phases := "no"
			if a.TwoPhase {
				phases = chFirstNonEmpty(a.InputLabel, "yes")
			}
			// What mapping produced, on the command's own row — and whether
			// it is switched on. Mapped-but-off is the state worth naming:
			// the work is done and nothing can use it, which looks identical
			// to unmapped from any distance.
			mapped := mappingState(a)
			rows = append(rows, map[string]any{
				"cmd": commandKey(a.Slug, a.Name), "store": store, "slug": a.Slug,
				"name": a.Name, "label": a.Label, "command": a.Command,
				"phases": phases, "two_phase": a.TwoPhase, "mapped": mapped,
				// mapped_ok gates the Agents switch: a toggle offering to
				// enable nothing is a question the reader cannot answer.
				"mapped_ok": a.Mapped(), "approved": a.Approved,
				"background": a.Background, "background_on": a.Background == "always",
				"input_label": a.InputLabel, "help": a.Help, "uses": a.usesLabel(),
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		// The form sends allowed_uses as one block of lines; the record keeps
		// a list. Either arrives here.
		var raw map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&raw); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if lines, ok := raw["allowed_uses"].(string); ok {
			raw["allowed_uses"] = strings.Split(lines, "\n")
		}
		var a StoreCommand
		if b, err := json.Marshal(raw); err != nil || json.Unmarshal(b, &a) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// The store comes from the ROW the action is being added on, so
		// nobody types a handle. A body slug still works — the API is
		// addressable on its own — but the URL wins, because a caller that
		// named a store in the path has said which one more explicitly than
		// a field carried along inside the payload.
		if s := strings.TrimSpace(r.URL.Query().Get("slug")); s != "" {
			a.Slug = s
		}
		saved, err := SaveStoreCommand(T.DB, a)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[filestore] %s registered action %q on %s → %s", user, saved.Name, saved.Slug, saved.Command)
		writeJSON(w, saved)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		slug, name, _ := strings.Cut(id, "/")
		DeleteStoreCommand(T.DB, slug, name)
		Log("[filestore] %s removed action %q from %s", user, name, slug)
		writeJSON(w, map[string]any{"deleted": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// adminOnly gates a handler on the caller being an admin, and answers
// with what would change the answer rather than a bare status.
//
// core.RequestIsAdmin is the one correct implementation: it reads
// AuthDB(), and treats a deployment with no users configured as open —
// with no users there is no admin to be, and gating on a role nobody
// holds locks the page for everyone.
//
// The trap it avoids: an app's T.DB is global.db.Bucket("<app>"), a
// namespaced substore, so AuthIsAdmin(T.DB, r) finds no auth table and
// refuses everyone. That is how this app failed the first time it was
// clicked, and how the same call in the old prompt editor silently did nothing.
func adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if RequestIsAdmin(r) {
		return true
	}
	http.Error(w, "File stores are admin-only: these paths name and read directories on the server, so the list of them is a deployment decision.", http.StatusForbidden)
	return false
}

// folderPanelHeading renders a section heading inside the folder panel. The
// panel stacks four subjects that used to be four separate expanders, and
// without headings they read as one long form.
func folderPanelHeading(title, blurb string) string {
	h := `<h3 style="margin:1.3rem 0 0.2rem;font-size:var(--fs-lg, 0.95rem)">` + HTMLEscape(title) + `</h3>`
	if strings.TrimSpace(blurb) != "" {
		h += `<p style="margin:0 0 0.6rem;font-size:var(--fs-sm, 0.8rem);opacity:0.72;max-width:62ch">` + HTMLEscape(blurb) + `</p>`
	}
	return h
}
