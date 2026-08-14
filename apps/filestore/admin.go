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
					{Field: "assigned", Label: "Assigned to", Mute: true},
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

func storeFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Type: "text", Label: "Name", Placeholder: "Support bundles",
			Help: "Shown in the agent's Sources picker, and folded into the tool names an attached agent gets (a store named \"Support bundles\" produces search_support_bundles)."},
		{Field: "path", Type: "text", Label: "Folder", Placeholder: "/var/log/bundles",
			Help: "Absolute path on this server. The folder itself is what an agent attaches to; its subfolders (if any) are what a search can be scoped to. Read-only: nothing here ever writes to it."},
		{Field: "allowed_users", Type: "tags", Label: "Assigned to",
			Help: "Usernames who may reach this store. Leave EMPTY for every user. A folder of customer captures is rarely something every account should hold, and configuring a store is already admin-only — without this the cheap half was gated and the reading was not. Applies to admins too: admin manages the list, membership decides reach."},
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
		rows := make([]map[string]any, 0)
		for _, st := range ListStores(T.DB) {
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
			})
		}
		writeJSON(w, rows)
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
