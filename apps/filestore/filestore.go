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
	. "github.com/cmcoffee/gohort/core"
)

func init() {
	app := new(FileStoreApp)
	RegisterApp(app)
	// Registered against the same instance so the source reads T.DB at
	// request time, after startup wires it — the same pattern the account
	// section and servitor's source use.
	RegisterReferenceSource(storeSource{app: app})
	RegisterAdminSection(AdminSectionEntry{Section: app.adminSection(), Head: adminHeadHTML()})
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
