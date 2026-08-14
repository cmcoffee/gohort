// Package filestore makes a folder of files on the gohort host
// searchable by an agent, without ever handing a model a whole file.
//
// It exists because there was no good way to give the system a folder of
// logs — but nothing about the problem is log-shaped. A Collection is
// the wrong tool for any corpus you want to GREP rather than embed:
// semantic chunking destroys the line structure that makes a log, a
// config tree, a source dump or a CSV export searchable, and embedding a
// gigabyte of stack traces buys nothing. An attachment is one file at a
// time. A shell tool is an unbounded read waiting to happen.
//
// So this is a file store: an admin-registered root, read-only, reached
// by exact and regular-expression search with hard caps on everything
// that comes back.
//
// Three pieces, and only the middle one is new:
//
//   - An admin-registered STORE: a root directory. Deployment config, so
//     it lives in the root store and only an admin edits it (store.go).
//   - Bounded READING: search, read, list, with caps that are not
//     configurable (search.go).
//   - A ReferenceSource so an agent attaches a store through the picker
//     it already has, and gets named per-store tools (source.go).
//
// Related but deliberately separate:
//
//   - tools/files is the SESSION WORKSPACE: ephemeral, per-session,
//     read AND write. This is persistent, admin-registered, read-only.
//     Same mechanics (contained resolution, bounded grep, line windows),
//     different mount and different trust. Folding the two onto one
//     engine is a real lift-to-core candidate; it was not worth doing
//     mid-build against a tool that already works.
//   - Running COMMANDS is servitor's. A local command appliance
//     (Type=="command", WorkDir on the folder) already mints frozen
//     command templates behind an owner approval gate. A second command
//     path here would be a weaker copy. The two compose: this answers
//     "where does X appear", the appliance answers "run the extractor".
package filestore

import (
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registeredFileStoreApp is the instance the registrations below close
// over. Held in a var so a test can point the path-scope registry at a
// fixture rather than at the process-wide instance, which has no DB.
var registeredFileStoreApp *FileStoreApp

func init() {
	app := new(FileStoreApp)
	registeredFileStoreApp = app
	RegisterApp(app)
	// Registered against the same instance so the source reads T.DB at
	// request time, after startup wires it — the same pattern the account
	// section and servitor's source use.
	RegisterReferenceSource(storeSource{app: app})
	RegisterAdminSection(AdminSectionEntry{Section: app.adminSection(), Head: adminHeadHTML()})
	// The visibility half. The reference source says what an agent CAN be
	// given; this says what one HOLDS, on the agent editor where somebody
	// asks "what can this thing reach". Without it a file store was the
	// one grant with no answer on that page: servitor's machines were
	// listed, and a folder of logs was not.
	//
	// EVERY grantor renders, including one granting nothing — a row
	// reading "none" is what tells an owner the capability exists and
	// this agent does not hold it.
	RegisterAgentGrantor(AgentGrantor{
		Name: "filestore", Label: "File stores",
		Granted: func(user, agentID string) []AgentGrant {
			return registeredFileStoreApp.grantsFor(user, agentID)
		},
	})
	// A path scope lets ANOTHER app's tool take a subfolder of a store as
	// a parameter and have it checked when the tool runs — the case a
	// frozen enum cannot cover, because the whole point of a drop folder
	// is that new names appear without ceremony. See core/path_scope.go.
	// The obvious caller is a servitor command tool pointed at a bundle.
	// Bound through the package var rather than the local, so the
	// resolvers follow whichever instance is live.
	RegisterPathScope("files", PathScope{
		Resolve: func(u, n, v string) (string, error) { return registeredFileStoreApp.resolveScope(u, n, v) },
		Values:  func(u, n string) []string { return registeredFileStoreApp.listScope(u, n) },
		Roots:   func(u string) []PathScopeRoot { return registeredFileStoreApp.scopeRoots(u) },
	})
}

// resolveScope proves a caller-supplied name is a subfolder of one store
// and returns its absolute path.
//
// SubRoot does the work, which is the point of routing through here
// rather than reimplementing it: it resolves symlinks rather than
// trusting the textual path, and refuses anything not strictly inside
// the root.
func (T *FileStoreApp) resolveScope(user, storeSlug, value string) (string, error) {
	st, ok := LoadStore(T.DB, storeSlug)
	// Same answer for "no such store" and "not yours": a path scope is
	// reachable from a minted command tool, and telling a caller that a
	// store exists but is not theirs is a fact they can do nothing with
	// and should not have.
	if !ok || !st.AllowsUser(user) {
		return "", Error("there is no file store called " + storeSlug + " you can reach")
	}
	if strings.TrimSpace(value) == "" {
		return "", Error("name a folder in " + st.Name)
	}
	dir, err := SubRoot(st.Path, value)
	if err != nil {
		// Worded for the model that supplied the value: what was refused
		// and what would change the answer.
		return "", Error(err.Error() + " — use one of the folders in " + st.Name +
			", exactly as it was listed")
	}
	return dir, nil
}

// scopeRoots advertises each store as a constraint a tool parameter can
// declare. Without this a tool author cannot discover the constraint
// exists, and an unadvertised constraint is one nobody uses.
func (T *FileStoreApp) scopeRoots(user string) []PathScopeRoot {
	var out []PathScopeRoot
	for _, st := range StoresForUser(T.DB, user) {
		out = append(out, PathScopeRoot{
			Ref: "files:" + st.Slug, Label: st.Name, Detail: strings.TrimSpace(st.Description),
		})
	}
	return out
}

// listScope names the folders currently in a store, so a tool
// description can say what the valid values are right now.
func (T *FileStoreApp) listScope(user, storeSlug string) []string {
	st, ok := LoadStore(T.DB, storeSlug)
	if !ok || !st.AllowsUser(user) {
		return nil
	}
	folders, err := ListFolders(st.Path)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		out = append(out, f.Name)
	}
	return out
}

// grantsFor lists the stores linked to one agent, for the editor's grant
// rows.
//
// Asks the agent-record app through core.AgentHoldsReference rather than
// reading an agent record — this package cannot see one, which is the
// same wall the path-scope gate goes through. With no such app wired the
// row honestly reads "none": nothing here can hold a link.
func (T *FileStoreApp) grantsFor(user, agentID string) []AgentGrant {
	if T == nil || T.DB == nil || AgentHoldsReference == nil {
		return nil
	}
	var out []AgentGrant
	for _, st := range StoresForUser(T.DB, user) {
		if !AgentHoldsReference(user, agentID, "files", st.Slug) {
			continue
		}
		// Read-only is the fact worth repeating here: it is the whole
		// posture of a store, and the row is read by someone deciding
		// whether an attachment is safe to leave in place.
		detail := "read-only"
		if folders, err := ListFolders(st.Path); err == nil {
			detail = strconv.Itoa(len(folders)) + " folders, read-only"
		}
		out = append(out, AgentGrant{Label: st.Name, Detail: detail})
	}
	return out
}

// FileStoreApp is the entry point. There is no hub tab and no dashboard
// card: a store is configured once in admin and then consumed by agents,
// so a browsing surface of its own would be a page nobody opens twice.
type FileStoreApp struct {
	AppCore
}

func (T *FileStoreApp) Name() string         { return "filestore" }
func (T *FileStoreApp) SystemPrompt() string { return "" }
func (T *FileStoreApp) Desc() string {
	return "Apps: File stores — make a folder on this server searchable by an agent."
}
func (T *FileStoreApp) Init() error { return T.Flags.Parse() }
func (T *FileStoreApp) Main() error {
	Log("File stores are configured from the admin page. Start with:\n  gohort serve :8080")
	return nil
}

// WebPath serves the admin section's save/delete endpoints. WebHidden
// keeps it off the dashboard: the section is reached from admin.
func (T *FileStoreApp) WebPath() string { return "/filestore" }
func (T *FileStoreApp) WebHidden() bool { return true }
func (T *FileStoreApp) WebName() string { return "File stores" }
func (T *FileStoreApp) WebDesc() string { return "Folders on this server an agent can search." }
