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
	"github.com/cmcoffee/gohort/core/ui"
)

// Every URL here is ABSOLUTE. The section renders on the ADMIN page, not
// on this app's own, so a relative "api/stores" resolves against /admin
// and 404s. Same reason apps/prompts writes /prompts/api/... in its
// admin section.
func (T *FileStoreApp) adminSection() ui.Section {
	return ui.Section{
		Group:    "Files",
		Title:    "File stores",
		Wide:     true,
		Subtitle: "Folders on this server an agent can SEARCH and READ, never write. Attach a store to an agent (Sources) and it gets tools to list what is there, search it by regular expression, and read a window around a hit — never a whole file. Subfolders are optional: a parent whose subfolders are per-ticket or per-run works, and so does a flat folder of files. Logs are the obvious case, but anything you would grep rather than embed belongs here: config trees, exports, source dumps. To RUN commands against a folder (unpack an archive, run an extractor), add a servitor appliance of type \"command\" with its Work Dir set to the same path — that path already mints approved command tools, and this one deliberately does not duplicate it.",
		Body: ui.Stack{Children: []ui.Component{
			ui.Table{
				Source: "/filestore/api/stores",
				RowKey: "slug",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "path", Mute: true, Flex: 2},
					// Folder count is the fact that says whether the path is
					// right. A store reading "unreadable" here is a typo
					// caught before an agent is attached to it.
					{Field: "folders", Label: "Subfolders", Mute: true},
					// The handle, because the tool names are built from it
					// and NOT from the name above it. Renaming a store
					// leaves search_<original> in place, which is correct
					// (attachments and frozen path_scope refs are keyed on
					// it) and looks broken unless the page says so.
					{Field: "tools", Label: "Agent tools", Mute: true, Flex: 2},
					{Field: "assigned", Label: "Assigned to", Mute: true},
					{Field: "retention", Label: "Retention", Mute: true},
					{Field: "description", Label: "Notes", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					ui.Expand("Edit", ui.FormPanel{
						Source:      "/filestore/api/stores?slug={slug}",
						PostURL:     "/filestore/api/stores",
						SubmitLabel: "Save changes",
						Fields:      storeFormFields(),
					}),
					// Who may reach it, picked from the accounts that exist
					// rather than typed from memory. Its own action because
					// the candidate list is LIVE: the section is built once at
					// startup, so a list baked into the form above would be the
					// users who existed when the process started. The picker
					// fetches instead, and a user added this morning is there.
					//
					// ui.ACLPicker is the shared shape for exactly this — the
					// same editor credentials, tools and shared agents use, so
					// "+ Add user" means one thing across the admin page.
					// Added from the STORE it belongs to, so the handle comes
					// from the row. It used to be a page-level button whose
					// first field asked for the handle, with help telling you
					// to read it off the Agent tools column and copy it — a
					// value already on screen, retyped, and wrong when mistyped.
					ui.Expand("Add command", ui.FormPanel{
						PostURL:     "/filestore/api/commands?slug={slug}",
						SubmitLabel: "Add command",
						Fields:      actionFormFields(),
						// The save broadcasts its own PostURL, and the slug in
						// the query means that is never the actions table's
						// source. Without this the action was added and the
						// table below went on listing what was there before.
						Invalidate: []string{"/filestore/api/commands"},
					}),
					ui.Expand("Assigned to", ui.ACLPicker(ui.ACLPickerConfig{
						OptionsSource: "/admin/api/user-candidates",
						RecordSource:  "/filestore/api/stores?slug={slug}",
						Field:         "allowed_users",
						PostTo:        "/filestore/api/stores",
						Method:        "POST",
						Noun:          "user",
						Intro: "Who may reach this store. Empty means EVERY user — a folder of customer captures is rarely something every account should hold, " +
							"and configuring a store is already admin-only, so without this the cheap half was gated and the reading was not. " +
							"Applies to admins too: admin manages the list, membership decides reach.",
						EmptyText: "No approved users to assign yet.",
						// The row this picker opened from renders the list it
						// writes, in "Assigned to". A picker broadcasts nothing
						// on its own, so the column kept the old answer.
						Invalidate: []string{"/filestore/api/stores"},
					})),
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/filestore/api/stores?slug={slug}",
						Variant:    "danger",
						Confirm:    "Remove this file store? The folder and its files are left alone; agents attached to it lose their search tools.",
						Optimistic: true},
				},
				EmptyText: "No file stores yet. Add a folder on this server for an agent to search.",
			},
			// Named for what it holds. "Action" is the most overloaded word on this
			// page — every table row here has RowActions, and ui.Table's own
			// column type is an action — so a column headed Action listing
			// binaries, beside a row action that deletes them, was two meanings
			// of one word within an inch of each other. The file that defines
			// these opens by calling them "admin-registered commands"; the form's
			// primary field is Command; the subtitle above points at servitor's
			// appliances of type "command" for the model-invoked equivalent. The
			// label was the last place still saying action.
			ui.Card{HTML: `<h3 style="margin:1.4rem 0 0.4rem;font-size:0.95rem">Commands</h3>`},
			ui.Table{
				Source: "/filestore/api/commands",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "store", Label: "Store", Flex: 1},
					{Field: "label", Label: "Button", Flex: 1},
					{Field: "command", Mute: true, Flex: 2},
					{Field: "phases", Label: "Asks for input", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/filestore/api/commands?id={id}",
						Variant:    "danger",
						Confirm:    "Remove this command? The binary is left alone; the button for it disappears.",
						Optimistic: true},
				},
				EmptyText: "No commands yet — add one from a store's row above. A command is a registered binary run against ONE folder — decrypt it, redact it, unpack a proprietary container, build an index — after which the files are ready to read. It is run for a person who clicks it, never called by an agent.",
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
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "decrypt",
			Help: "Short handle for the command, snake_case. It is how the endpoint names it."},
		{Field: "label", Type: "text", Label: "Button label", Placeholder: "Decrypt bundle",
			Help: "What the button says. Name it for what it DOES to the folder, since that is what the person clicking it is deciding."},
		{Field: "command", Type: "text", Label: "Command", Placeholder: "/opt/bin/diag_decrypt",
			Help: "Absolute path. It is called as `<command> <folder>`, and for a two-phase command a second time as `<command> <folder> <input>`. Run with NO shell, so quoting and metacharacters have nowhere to happen."},
		{Field: "two_phase", Type: "toggle", Label: "Two phases (asks for input)",
			Help: "On: the first run prints something — a challenge, a summary, a prompt — which is shown to the person, who supplies a value; the command then runs again with it. Off: one call and the folder is ready. Use two phases when a value has to come from OUTSIDE this system and nothing here can obtain it."},
		{Field: "input_label", Type: "text", Label: "Input label", Placeholder: "Response key",
			Help: "What the box asks for on the second phase. Only used when two phases is on."},
		{Field: "help", Type: "textarea", Label: "Note", Rows: 2,
			Help: "Optional. Shown beside the button — say what it does and when to reach for it."},
	}
}

func storeFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "Support bundles",
			Help: "Shown in the agent's Sources picker and in the tool descriptions an attached agent reads. The tool NAMES come from a handle minted from this name the first time the store is saved (\"Support bundles\" → search_support_bundles), and that handle does not change afterwards: RENAMING A STORE CHANGES THE LABEL, NOT THE TOOL NAMES. It has to work that way — the handle is what agent attachments are keyed on and what a minted command tool's frozen path_scope names, so moving it would break every approved tool pointed at this store. The Agent tools column shows the names in force."},
		{Field: "path", Type: "text", Label: "Folder", Placeholder: "/var/log/bundles",
			Help: "Absolute path on this server. The folder itself is what an agent attaches to; its subfolders (if any) are what a search can be scoped to. Read-only: nothing here ever writes to it."},
		{Field: "allow_uploads", Type: "toggle", Label: "Let assigned users upload",
			Help: "Off by default. Reaching a store and WRITING into it are different grants: the common case is a log directory something else fills, and pointing at it should not let every account with an agent add files to it. Turn this on for a drop store people are meant to feed. An admin can upload either way."},
		{Field: "retention_days", Type: "number", Label: "Delete folders older than (days)", Min: 0,
			Help: "0 keeps everything forever, which is the default. A number makes a subfolder ELIGIBLE for deletion once nothing in it has changed for that long — nothing is deleted on a timer. An admin runs the sweep from Maintenance, where a dry run lists exactly what the delete would remove. Age is read from the folder and its immediate contents, so a write buried deep inside may not refresh it: leave headroom rather than setting this to the exact age you care about."},
		{Field: "description", Type: "textarea", Label: "What lands here", Rows: 2,
			Help: "Optional, and worth writing: it is pasted into the agent's tool descriptions, so it is what tells the agent when to reach for THIS store rather than another. \"Customer support bundles, one folder per ticket, uploaded by the support team.\""},
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
	T.HandleFunc("/api/stores", T.handleStores)
	T.HandleFunc("/api/upload", T.handleUpload)
	T.HandleFunc("/api/commands/run", T.handleCommand)
	T.HandleFunc("/api/commands", T.handleCommands)
	T.HandleFunc("/api/folders", T.handleFolders)
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
			writeJSON(w, st)
			return
		}
		writeJSON(w, T.storeRows())
	case http.MethodPost:
		// Decoded ONTO the stored record, not into a blank one. Two editors
		// now write this record — the form (name, path, retention…) and the
		// assignment picker (allowed_users) — and neither sends the other's
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
		if s := storeSlugOf(raw, slug); s != "" {
			if existing, found := LoadStore(T.DB, s); found {
				st = existing
			}
		}
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
		writeJSON(w, saved)
	case http.MethodDelete:
		DeleteStore(T.DB, slug)
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
	for _, st := range ListStores(T.DB) {
		// Folder count is the fact that says whether the path is right. A
		// store reading "unreadable" here is a typo caught before an agent
		// is attached to it.
		folders := "unreadable"
		if n, err := CountFolders(st.Path); err == nil {
			folders = strconv.Itoa(n)
		}
		assigned := "everyone"
		if len(st.AllowedUsers) > 0 {
			assigned = strings.Join(st.AllowedUsers, ", ")
		}
		rows = append(rows, map[string]any{
			"slug": st.Slug, "name": st.Name, "path": st.Path,
			"description": st.Description, "folders": folders,
			"assigned": assigned, "allowed_users": st.AllowedUsers,
			// From the same definition ItemTools builds from. These are
			// the names in force, which after a rename are not the names
			// the Name column would suggest.
			"tools": strings.Join(storeToolNames(st.Slug), ", "),
			// Stated on the row because it is the one setting whose effect
			// is destructive and whose default (forever) is invisible.
			"retention": retentionLabel(st),
		})
	}
	return rows
}

// retentionLabel says what the store's window means in words, including
// the upload posture, since both live on the same row.
func retentionLabel(st Store) string {
	window := "keep forever"
	if st.RetentionDays > 0 {
		window = "delete after " + strconv.Itoa(st.RetentionDays) + "d"
	}
	if st.AllowUploads {
		return window + ", uploads on"
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
			rows = append(rows, map[string]any{
				"id": commandKey(a.Slug, a.Name), "store": store, "slug": a.Slug,
				"name": a.Name, "label": a.Label, "command": a.Command,
				"phases": phases, "two_phase": a.TwoPhase,
				"input_label": a.InputLabel, "help": a.Help,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var a StoreCommand
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
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
// clicked, and how the same call in apps/prompts silently did nothing.
func adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if RequestIsAdmin(r) {
		return true
	}
	http.Error(w, "File stores are admin-only: these paths name and read directories on the server, so the list of them is a deployment decision.", http.StatusForbidden)
	return false
}
