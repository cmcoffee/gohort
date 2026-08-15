// The admin surface: a table of registered stores plus an add form.
//
// Deliberately plain. A store is four fields, an operator sets one up
// once, and the only thing that needs care is telling them immediately
// when a path is wrong — which the save endpoint does by returning the
// validator's own sentence rather than a status code.

package filestore

import (
	"encoding/json"
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
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/filestore/api/stores?slug={slug}",
						Variant:    "danger",
						Confirm:    "Remove this file store? The folder and its files are left alone; agents attached to it lose their search tools.",
						Optimistic: true},
				},
				EmptyText: "No file stores yet. Add a folder on this server for an agent to search.",
			},
			ui.Table{
				Source: "/filestore/api/actions",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "store", Label: "Store", Flex: 1},
					{Field: "label", Label: "Action", Flex: 1},
					{Field: "command", Mute: true, Flex: 2},
					{Field: "phases", Label: "Asks for input", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "/filestore/api/actions?id={id}",
						Variant:    "danger",
						Confirm:    "Remove this action? The binary is left alone; the button for it disappears.",
						Optimistic: true},
				},
				EmptyText: "No actions yet. An action is a command that runs against ONE folder — decrypt it, redact it, unpack a proprietary container, build an index — after which the files are ready to read.",
			},
			ui.ModalButton{
				Label:    "Add action",
				Title:    "Add a file action",
				Subtitle: "A command that runs against one folder of a store. It receives the folder as its first argument, and (for a two-phase action) whatever the person supplies as its second. No shell: nothing in either argument can become syntax.",
				Width:    "560px",
				Body: ui.FormPanel{
					PostURL:     "/filestore/api/actions",
					SubmitLabel: "Add action",
					Fields:      actionFormFields(),
				},
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
		{Field: "slug", Type: "text", Label: "Store (handle)", Placeholder: "support_bundles",
			Help: "The store's handle, from the Agent tools column above — not its display name."},
		{Field: "name", Type: "text", Label: "Name", Placeholder: "decrypt",
			Help: "Short handle for the action, snake_case. It is how the endpoint names it."},
		{Field: "label", Type: "text", Label: "Button label", Placeholder: "Decrypt bundle",
			Help: "What the button says. Name it for what it DOES to the folder, since that is what the person clicking it is deciding."},
		{Field: "command", Type: "text", Label: "Command", Placeholder: "/opt/bin/diag_decrypt",
			Help: "Absolute path. It is called as `<command> <folder>`, and for a two-phase action a second time as `<command> <folder> <input>`. Run with NO shell, so quoting and metacharacters have nowhere to happen."},
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
		{Field: "allowed_users", Type: "tags", Label: "Assigned to",
			Help: "Usernames who may reach this store. Leave EMPTY for every user. A folder of customer captures is rarely something every account should hold, and configuring a store is already admin-only — without this the cheap half was gated and the reading was not. Applies to admins too: admin manages the list, membership decides reach."},
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
	T.HandleFunc("/api/stores", T.handleStores)
	T.HandleFunc("/api/upload", T.handleUpload)
	T.HandleFunc("/api/action", T.handleAction)
	T.HandleFunc("/api/actions", T.handleActions)
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
		var st Store
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
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
		if list, err := ListFolders(st.Path); err == nil {
			folders = strconv.Itoa(len(list))
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

// handleActions is the admin CRUD behind the actions table.
//
// Admin-only, and for a sharper reason than the store list: this names a
// BINARY the server will execute. Registering one is the same decision
// as registering the directory it runs against, and neither is a user's
// to make.
func (T *FileStoreApp) handleActions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if !adminOnly(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows := make([]map[string]any, 0)
		for _, a := range ListStoreActions(T.DB) {
			store := a.Slug
			if st, found := LoadStore(T.DB, a.Slug); found {
				store = st.Name
			} else {
				store = a.Slug + " (missing)"
			}
			phases := "no"
			if a.TwoPhase {
				phases = chFirstNonEmpty(a.InputLabel, "yes")
			}
			rows = append(rows, map[string]any{
				"id": actionKey(a.Slug, a.Name), "store": store, "slug": a.Slug,
				"name": a.Name, "label": a.Label, "command": a.Command,
				"phases": phases, "two_phase": a.TwoPhase,
				"input_label": a.InputLabel, "help": a.Help,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var a StoreAction
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		saved, err := SaveStoreAction(T.DB, a)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[filestore] %s registered action %q on %s → %s", user, saved.Name, saved.Slug, saved.Command)
		writeJSON(w, saved)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		slug, name, _ := strings.Cut(id, "/")
		DeleteStoreAction(T.DB, slug, name)
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
