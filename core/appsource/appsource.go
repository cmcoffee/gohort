// Package appsource keeps where an imported app came from (docs/design/
// app-history.md, updates for imported apps): the copy its file was exported
// from, when that copy exported the version taken last, when it was taken,
// and a mark of the app as it was then. A later export of the same copy is
// an update to it.
package appsource

import (
	"strings"

	"github.com/cmcoffee/gohort/core"
)

// Source is where an imported app came from: the copy, when that copy
// exported the version taken last, when it was taken, and a mark of the app
// as it was then (for a deployment with no app history, where an update is
// taken only into an app nobody has changed since).
type Source struct {
	Copy     string `json:"copy"`
	Exported string `json:"exported"`
	Taken    string `json:"taken"`
	Mark     string `json:"mark,omitempty"`
}

const table = "app_sources"

func store(owner string) core.Database {
	owner = strings.TrimSpace(owner)
	if core.RootDB == nil || owner == "" {
		return nil
	}
	return core.UserDB(core.RootDB, owner)
}

// Load is where owner's app slug came from, when it was imported from a file
// that said.
func Load(owner, slug string) (Source, bool) {
	db := store(owner)
	if db == nil || strings.TrimSpace(slug) == "" {
		return Source{}, false
	}
	var s Source
	if !db.Get(table, strings.TrimSpace(slug), &s) || s.Copy == "" {
		return Source{}, false
	}
	return s, true
}

// Save records where owner's app slug came from.
func Save(owner, slug string, s Source) {
	if db := store(owner); db != nil && strings.TrimSpace(slug) != "" {
		db.Set(table, strings.TrimSpace(slug), s)
	}
}

func init() {
	// Where an app came from goes with it.
	core.RegisterAppSpecDeletedHook(func(owner, slug string) {
		if db := store(owner); db != nil {
			db.Unset(table, slug)
		}
	})
}
