// Package appstores joins custom apps to the file stores an admin has granted
// them.
//
// A file store is deployment configuration, so only an admin decides which
// app may use one, on the store's own page, and the grant belongs to the APP:
// it covers whoever uses the app, the way a tool carries what its author was
// allowed. The app's scripts then find the granted stores' folders mounted in
// their sandbox (read, or read and write) and can run the store's registered
// commands and pull files and folders into it, as the grant says.
//
// The file-store app answers the questions (Host); the app runner asks them.
// Neither imports the other, which this package exists to allow.
package appstores

import (
	"context"
	"strings"
)

// Grant is what one app may do with one store, resolved to its folder.
type Grant struct {
	Store    string `json:"store"` // the store's handle
	Name     string `json:"name"`  // its display name
	Path     string `json:"path"`  // its folder on this server
	Read     bool   `json:"read"`
	Write    bool   `json:"write"`    // add, change and delete files (implies Read)
	Commands bool   `json:"commands"` // run its registered commands
	Pull     bool   `json:"pull"`     // pull files and folders into it from the store it pulls from
}

// Host is the file-store side.
type Host interface {
	// GrantsFor is every store granted to the app owner/slug.
	GrantsFor(owner, slug string) []Grant
	// RunCommand runs store's command on folder for the app, when granted.
	// input "" is a two-phase command's first phase.
	RunCommand(ctx context.Context, owner, slug, store, folder, command, input string) (string, error)
	// Pull copies a file or folder into store for the app, when granted.
	Pull(ctx context.Context, owner, slug, store, item string, replace bool) (string, error)
}

// Provider is the registered host, nil where no file-store app is compiled in.
var Provider Host

// StoreInfo is one file store as an app's author sees it before building:
// what it holds, the commands registered on it, where it pulls from, and what
// the app (if one is named) is granted on it.
type StoreInfo struct {
	Store       string        `json:"store"`
	Name        string        `json:"name"`
	About       string        `json:"about,omitempty"`
	PullsFrom   string        `json:"pulls_from,omitempty"` // the store it copies files and folders in from
	PullPart    string        `json:"pull_part,omitempty"`  // the part of a folder a pull copies
	Temporary   bool          `json:"temporary,omitempty"`
	Commands    []CommandInfo `json:"commands,omitempty"`
	Granted     *Grant        `json:"granted,omitempty"` // nil: the app has no grant on it
	FolderCount int           `json:"folders"`
}

// CommandInfo is one command registered on a store, as an app calls it:
// store_command(store, folder, Name, input=...) runs `<binary> <folder>
// [input]`.
type CommandInfo struct {
	Name       string `json:"name"`
	Label      string `json:"label,omitempty"`
	TwoPhase   bool   `json:"two_phase,omitempty"`
	InputLabel string `json:"input_label,omitempty"`
	Note       string `json:"note,omitempty"`
	Guide      string `json:"guide,omitempty"` // the admin's markdown guide to using it
}

// Catalog is the file-store side's answer to "what stores are there", for an
// app's author. Optional beside Host: a host that does not offer it simply
// lists nothing.
type Catalog interface {
	// Stores is every store owner may reach, with slug's grant on each when
	// slug names an app ("" for none yet).
	Stores(owner, slug string) []StoreInfo
}

// GrantRequestKind is the approvals-queue kind for an app asking for access
// to a store (core/promotion). The request's name says what is asked for, in
// GrantRequestName's shape, so the admin reads it and the approver applies
// exactly that.
const GrantRequestKind = "store_grant"

// GrantRequestName is the name a store-grant request is filed under:
// "<store>/<app slug>: read, write, run commands, pull". Read is always part
// of a grant; the rest are as asked.
func GrantRequestName(store, slug string, write, commands, pull bool) string {
	rights := []string{"read"}
	if write {
		rights = append(rights, "write")
	}
	if commands {
		rights = append(rights, "run commands")
	}
	if pull {
		rights = append(rights, "pull")
	}
	return store + "/" + slug + ": " + strings.Join(rights, ", ")
}

// ParseGrantRequestName reads a GrantRequestName back.
func ParseGrantRequestName(name string) (store, slug string, write, commands, pull, ok bool) {
	head, rights, found := strings.Cut(name, ": ")
	if !found {
		return "", "", false, false, false, false
	}
	store, slug, found = strings.Cut(head, "/")
	if !found || strings.TrimSpace(store) == "" || strings.TrimSpace(slug) == "" {
		return "", "", false, false, false, false
	}
	for _, r := range strings.Split(rights, ", ") {
		switch strings.TrimSpace(r) {
		case "write":
			write = true
		case "run commands":
			commands = true
		case "pull":
			pull = true
		}
	}
	return store, slug, write, commands, pull, true
}
