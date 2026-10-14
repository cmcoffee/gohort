// Package appstores joins custom apps to the file stores an admin has granted
// them.
//
// A file store is deployment configuration, so only an admin decides which
// app may use one, on the store's own page, and the grant belongs to the APP:
// it covers whoever uses the app, the way a tool carries what its author was
// allowed. The app's scripts then find the granted stores' folders mounted in
// their sandbox (read, or read and write) and can run the store's registered
// commands and pull cases into it, as the grant says.
//
// The file-store app answers the questions (Host); the app runner asks them.
// Neither imports the other, which this package exists to allow.
package appstores

import "context"

// Grant is what one app may do with one store, resolved to its folder.
type Grant struct {
	Store    string `json:"store"` // the store's handle
	Name     string `json:"name"`  // its display name
	Path     string `json:"path"`  // its folder on this server
	Read     bool   `json:"read"`
	Write    bool   `json:"write"`    // add, change and delete files (implies Read)
	Commands bool   `json:"commands"` // run its registered commands
	Pull     bool   `json:"pull"`     // pull cases into it from the store it pulls from
}

// Host is the file-store side.
type Host interface {
	// GrantsFor is every store granted to the app owner/slug.
	GrantsFor(owner, slug string) []Grant
	// RunCommand runs store's command on folder for the app, when granted.
	// input "" is a two-phase command's first phase.
	RunCommand(ctx context.Context, owner, slug, store, folder, command, input string) (string, error)
	// Pull copies a case into store for the app, when granted.
	Pull(ctx context.Context, owner, slug, store, caseName string, replace bool) (string, error)
}

// Provider is the registered host, nil where no file-store app is compiled in.
var Provider Host
