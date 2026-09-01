// Toolboxes mapped from local commands, and the folders they may run in.
//
// Step 2 of docs/local-command-tools.md. The model is three nouns, each owning
// the next: a STORE is a folder, a COMMAND is a binary registered against one,
// and a TOOLBOX is what mapping a command produces — the actions that binary can
// perform, under one name.
//
// The toolbox is stored ONCE and attached to the folders it may run in. That is
// the correction to the arrangement reverted in v0.6.538, where a tool was
// minted into a single store and then appeared as a loose record in the global
// tool list, reachable by nothing until an admin found a second expander and
// bound it. Two things follow from getting it this way round:
//
// A second folder of the same kind costs an ATTACHMENT, not a re-mapping. One
// `/opt/bin/cap` mapped once serves every folder of captures.
//
// Nothing is ever orphaned. A toolbox is reached from the command it was mapped
// from, which is reached from the store that command belongs to. The worst state
// it can be in is "attached to no folder yet", which reads as not attached —
// next to the command it maps, not adrift in a catalog.

package filestore

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/tools/temptool"
)

// toolboxesTable holds mapped toolboxes, keyed by name.
//
// Keyed by NAME and not by store, because the toolbox belongs to the binary
// rather than to any one folder — which is the whole point of being able to
// attach it to several. Deployment-wide, so two admins do not map the same
// binary twice under two names.
const toolboxesTable = "file_store_toolboxes"

// StoreToolbox is a toolbox mapped from a local command, plus where it came
// from.
//
// Provenance is not decoration: it is what lets the interface show the toolbox
// on the row of the command it maps, and what tells a reader looking at an
// attached toolbox on some other folder where to go to change it.
type StoreToolbox struct {
	// Tool is the toolbox itself — Mode: toolbox, with one action per thing the
	// command can do. BoundOnly, always: a toolbox mapped from one deployment's
	// capture binary has no business in every chat, and the whole reason a
	// folder's bundle can be narrow is that its members opt out of the catalog.
	Tool TempTool `json:"tool"`
	// FromStore / FromCommand name the command this was mapped from. Kept even
	// when that command is later deleted — a toolbox that outlives its origin
	// still ran against something, and blanking the field would leave a reader
	// with no thread to pull.
	FromStore   string    `json:"from_store,omitempty"`
	FromCommand string    `json:"from_command,omitempty"`
	Created     time.Time `json:"created"`
}

// SaveToolbox records a mapped toolbox. Inert on arrival: it runs nowhere until
// attached to a store, and attaching is a person's act.
func SaveToolbox(db Database, tb StoreToolbox) (StoreToolbox, error) {
	tb.Tool.Name = RefToolSlug(strings.TrimSpace(tb.Tool.Name))
	switch {
	case tb.Tool.Name == "":
		return tb, Error("give the toolbox a name — a short handle like \"capture_tools\"")
	case strings.TrimSpace(tb.Tool.Description) == "":
		return tb, Error("give the toolbox a description: it is what another agent reads before opening it")
	case len(tb.Tool.Actions) == 0:
		return tb, Error("a toolbox with no actions does nothing — map at least one thing the command can do")
	}
	for i, a := range tb.Tool.Actions {
		if strings.TrimSpace(a.Name) == "" {
			return tb, Error("every action needs a name")
		}
		if strings.TrimSpace(a.CommandTemplate) == "" {
			return tb, Error("action " + a.Name + " has no command to run")
		}
		// A local command mapped into an HTTP action is a mistake worth catching
		// here rather than at the first call, where it would read as a network
		// failure.
		if strings.TrimSpace(a.URLTemplate) != "" {
			return tb, Error("action " + a.Name + " declares a url_template; a mapped command is local, not an HTTP call")
		}
		tb.Tool.Actions[i].Name = strings.TrimSpace(a.Name)
	}
	tb.Tool.Mode = TempToolModeToolbox
	tb.Tool.BoundOnly = true
	if tb.Created.IsZero() {
		tb.Created = time.Now()
	}
	// A name already taken by a DIFFERENT command is refused rather than
	// overwritten. Two binaries answering to one name would make an attachment
	// mean something other than what the admin who made it read.
	if prev, ok := LoadToolbox(db, tb.Tool.Name); ok {
		if prev.FromStore != tb.FromStore || prev.FromCommand != tb.FromCommand {
			return tb, Error("a toolbox called " + tb.Tool.Name + " is already mapped from " +
				prev.FromCommand + " on " + prev.FromStore + " — pick another name, or re-map that one")
		}
		tb.Created = prev.Created // a re-map keeps its original date
	}
	db.Set(toolboxesTable, tb.Tool.Name, tb)
	return tb, nil
}

// LoadToolbox returns one mapped toolbox.
func LoadToolbox(db Database, name string) (StoreToolbox, bool) {
	var tb StoreToolbox
	if db == nil || strings.TrimSpace(name) == "" {
		return tb, false
	}
	ok := db.Get(toolboxesTable, RefToolSlug(strings.TrimSpace(name)), &tb)
	return tb, ok
}

// ListToolboxes returns every mapped toolbox, by name.
func ListToolboxes(db Database) []StoreToolbox {
	var out []StoreToolbox
	if db == nil {
		return out
	}
	for _, k := range db.Keys(toolboxesTable) {
		var tb StoreToolbox
		if db.Get(toolboxesTable, k, &tb) {
			out = append(out, tb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	return out
}

// DeleteToolbox drops a mapped toolbox. Attachments naming it are left alone: a
// name in a store's Toolboxes that resolves to nothing is skipped, and tidying
// the list is the admin's business rather than something a delete should do
// behind them.
func DeleteToolbox(db Database, name string) {
	if db != nil {
		db.Unset(toolboxesTable, RefToolSlug(strings.TrimSpace(name)))
	}
}

// ToolboxesMappedFrom returns the toolboxes mapped from one store's commands —
// what the command rows on that folder show.
func ToolboxesMappedFrom(db Database, slug string) []StoreToolbox {
	var out []StoreToolbox
	for _, tb := range ListToolboxes(db) {
		if tb.FromStore == slug {
			out = append(out, tb)
		}
	}
	return out
}

// attachedToolboxDefs renders the toolboxes attached to a store as agent tools,
// so they arrive with the folder and nowhere else.
//
// Resolution goes through the same path servitor's appliance toolset uses — the
// tools carried on a session into temptool.BuildAgentToolDefs — so an attached
// toolbox behaves at call time exactly as it does when called directly, rather
// than through a second implementation that drifts from the first.
//
// A name that resolves to nothing is skipped rather than reported: the
// attachment outliving the toolbox is an authoring problem to fix where
// toolboxes are managed, and refusing to hand over the other three because one
// was deleted would take a working folder down with it.
func (s storeSource) attachedToolboxDefs(sess *ToolSession, user string, st Store) []AgentToolDef {
	if len(st.Toolboxes) == 0 {
		return nil
	}
	// AuthDB is a function VARIABLE, nil until startup wires it. Calling it
	// unguarded panics — on a path reached from a tool catalog, which is a
	// worse way to find out than a toolbox quietly not resolving.
	var authDB Database
	if AuthDB != nil {
		authDB = AuthDB()
	}
	bound := &ToolSession{Username: user, DB: authDB, Ctx: sess.Context()}
	if ws, err := EnsureWorkspaceDir(user); err == nil {
		bound.WorkspaceDir = ws
	}
	seen := map[string]bool{}
	for _, name := range st.Toolboxes {
		name = RefToolSlug(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tb, ok := LoadToolbox(s.app.DB, name)
		if !ok {
			continue
		}
		t := tb.Tool
		bound.TempTools = append(bound.TempTools, &t)
	}
	if len(bound.TempTools) == 0 {
		return nil
	}
	return temptool.BuildAgentToolDefs(bound)
}

// storeHasToolbox reports whether a folder carries a toolbox by name.
func storeHasToolbox(st Store, name string) bool {
	for _, n := range st.Toolboxes {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			return true
		}
	}
	return false
}

// countOf renders a count with its noun, so a row reads as a sentence rather
// than as a number beside a label.
//
// Both forms are passed rather than derived: "toolbox" takes -es, and a rule
// that appends -s would print "2 toolboxs" on the stores list. Spelling the
// plural is cheaper than a rule that is wrong for the second noun it meets.
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// handleToolboxes serves the Toolboxes list on a folder (GET ?slug=) and
// detaches one (DELETE ?slug=&name=).
//
// Detaching removes the ATTACHMENT, never the toolbox: the mapping was work
// somebody did, and a folder deciding it no longer wants a capability is not a
// statement that the capability was wrong. The toolbox stays on the row of the
// command it was mapped from, which is where deleting it belongs.
func (T *FileStoreApp) handleToolboxes(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	st, found := LoadStore(T.DB, slug)
	if !found || !st.AllowsUser(user) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows := make([]map[string]any, 0, len(st.Toolboxes))
		for _, name := range st.Toolboxes {
			tb, ok := LoadToolbox(T.DB, name)
			if !ok {
				// The attachment outlived the toolbox. Shown rather than
				// hidden: a name that resolves to nothing is skipped at call
				// time, so the folder is quietly missing a capability, and the
				// only place that can be noticed is here.
				rows = append(rows, map[string]any{
					"name": name, "actions": "—",
					"origin": "missing — the toolbox this names has been deleted",
				})
				continue
			}
			origin := "mapped here, from " + tb.FromCommand
			if tb.FromStore != slug {
				origin = "mapped on another folder, from " + tb.FromCommand
			}
			rows = append(rows, map[string]any{
				"name": tb.Tool.Name, "actions": countOf(len(tb.Tool.Actions), "action", "actions"),
				"origin": origin, "description": firstLine(tb.Tool.Description),
			})
		}
		writeJSON(w, rows)
	case http.MethodDelete:
		if !adminOnly(w, r) {
			return
		}
		name := RefToolSlug(strings.TrimSpace(r.URL.Query().Get("name")))
		kept := st.Toolboxes[:0]
		for _, n := range st.Toolboxes {
			if !strings.EqualFold(RefToolSlug(strings.TrimSpace(n)), name) {
				kept = append(kept, n)
			}
		}
		st.Toolboxes = kept
		if _, err := SaveStore(T.DB, st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleToolboxCandidates lists every mapped toolbox for the Attach picker,
// saying where each came from so a name means something to whoever is choosing.
func (T *FileStoreApp) handleToolboxCandidates(w http.ResponseWriter, r *http.Request) {
	type opt struct {
		Value string `json:"value"`
		Label string `json:"label"`
		Desc  string `json:"desc,omitempty"`
	}
	out := []opt{}
	for _, tb := range ListToolboxes(T.DB) {
		where := tb.FromCommand
		if st, ok := LoadStore(T.DB, tb.FromStore); ok {
			where += " on " + st.Name
		}
		out = append(out, opt{
			Value: tb.Tool.Name, Label: tb.Tool.Name,
			Desc: countOf(len(tb.Tool.Actions), "action", "actions") + " · from " + where,
		})
	}
	writeJSON(w, out)
}

// firstLine trims a description to its opening sentence for a row's subtitle —
// the rest is written for a model, not for a table.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

// storeCarriesLabel summarises the toolboxes attached to a folder, for the
// stores list.
//
// It exists because a mapped toolbox is deliberately NOT in the deployment tool
// list: it is a capability of a folder, not a tool of the installation. That
// makes the folder the only place it can be seen, and a stores list that said
// nothing would render a folder holding four capabilities identically to one
// holding none.
//
// A stale attachment is counted separately rather than quietly excluded. At call
// time such a name is skipped, so the folder is missing a capability somebody
// thinks it has, and a count that hid it would be the reason nobody noticed.
func storeCarriesLabel(db Database, st Store) string {
	if len(st.Toolboxes) == 0 {
		return "—"
	}
	live, missing := 0, 0
	seen := map[string]bool{}
	for _, name := range st.Toolboxes {
		name = RefToolSlug(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := LoadToolbox(db, name); ok {
			live++
		} else {
			missing++
		}
	}
	switch {
	case live == 0 && missing == 0:
		return "—"
	case missing == 0:
		return countOf(live, "toolbox", "toolboxes")
	case live == 0:
		return fmt.Sprintf("⚠ %s missing", countOf(missing, "toolbox", "toolboxes"))
	}
	return fmt.Sprintf("%s · ⚠ %d missing", countOf(live, "toolbox", "toolboxes"), missing)
}
