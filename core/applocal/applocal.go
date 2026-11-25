// Package applocal is how this copy of an app runs on this deployment, never
// part of the app itself and never exported (docs/design/app-history.md,
// stage 2): what its import needs are mapped to here (pieces F2), who asked
// it not to remember them (piece E1), and whether its collections keep
// themselves current (piece E2), and how it lends its owner's credentials to
// the people using it (docs/design/app-credentials.md).
package applocal

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core"
)

// AppMemoryNamespace is the memory of owner's app slug, kept in the store of
// the person it is about (docs/design/app-history.md, piece E): what the
// app's agents learn about each person who uses it, shared by all of them.
func AppMemoryNamespace(owner, slug string) string {
	return "app:" + owner + "/" + slug
}

// appMemoryOptOutTable is, in a person's store, the apps they asked not to
// remember them, keyed owner/slug.
const appMemoryOptOutTable = "app_memory_optout"

// AppMemoryOptedOut reports whether the person whose store db is asked
// owner's app slug not to remember them.
func AppMemoryOptedOut(db core.Database, owner, slug string) bool {
	var off bool
	return db != nil && db.Get(appMemoryOptOutTable, owner+"/"+slug, &off) && off
}

// SetAppMemoryOptOut records, in the person's store db, that owner's app
// slug is not to remember them (off), or that it may again.
func SetAppMemoryOptOut(db core.Database, owner, slug string, off bool) {
	if db == nil {
		return
	}
	if off {
		db.Set(appMemoryOptOutTable, owner+"/"+slug, true)
		return
	}
	db.Unset(appMemoryOptOutTable, owner+"/"+slug)
}

// appCurationOffTable holds, in core.RootDB, the apps whose owner turned off
// keeping their collections current on this deployment, keyed owner/slug.
const appCurationOffTable = "app_curation_off"

// AppCurationOff reports whether owner's app slug's collections are kept
// from refreshing on their own here: the app's setting (its Settings page),
// how this copy runs, never part of its blueprint.
func AppCurationOff(owner, slug string) bool {
	var off bool
	return core.RootDB != nil && core.RootDB.Get(appCurationOffTable, owner+"/"+slug, &off) && off
}

// AppCurationChanged, when set, is told that owner's app slug's setting
// changed, to schedule its collections' refreshes or stop them.
var AppCurationChanged func(owner, slug string)

// SetAppCurationOff records owner's choice for app slug: its collections
// not refreshed on their own (off), or as their blueprint says.
func SetAppCurationOff(owner, slug string, off bool) {
	if core.RootDB == nil {
		return
	}
	if off {
		core.RootDB.Set(appCurationOffTable, owner+"/"+slug, true)
	} else {
		core.RootDB.Unset(appCurationOffTable, owner+"/"+slug)
	}
	if AppCurationChanged != nil {
		AppCurationChanged(owner, slug)
	}
}

// AppNeed is one thing an app names that only a deployment has (a credential,
// a tool it calls, a voice, a file store; docs/design/app-history.md, piece
// F), and how it is answered on this deployment: mapped to one of the
// owner's here (Map), or gone without (Off). Neither is unanswered. How this
// copy runs, never part of the app: it is not exported.
type AppNeed struct {
	Kind string `json:"kind"` // credential | tool | voice | store
	Name string `json:"name"` // as the app names it
	Map  string `json:"map,omitempty"`
	Off  bool   `json:"off,omitempty"`
	// Store is what a file store need asks of it (read always), as the
	// exporting deployment granted it.
	Write    bool `json:"write,omitempty"`
	Commands bool `json:"commands,omitempty"`
	Pull     bool `json:"pull,omitempty"`
}

// Answered reports whether the need has an answer: mapped, or gone without.
func (n AppNeed) Answered() bool { return n.Off || strings.TrimSpace(n.Map) != "" }

const appNeedsTable = "app_needs"

// LoadAppNeeds is how owner's app slug's needs are answered here.
func LoadAppNeeds(owner, slug string) []AppNeed {
	var out []AppNeed
	if core.RootDB != nil {
		core.RootDB.Get(appNeedsTable, owner+"/"+slug, &out)
	}
	return out
}

// SaveAppNeeds records how owner's app slug's needs are answered here.
func SaveAppNeeds(owner, slug string, needs []AppNeed) {
	if core.RootDB == nil {
		return
	}
	if len(needs) == 0 {
		core.RootDB.Unset(appNeedsTable, owner+"/"+slug)
		return
	}
	core.RootDB.Set(appNeedsTable, owner+"/"+slug, needs)
}

// AppNeedAlias is what the kind name owner's app slug names means here: the
// name it is mapped to, or name itself when it is not mapped; off when it is
// gone without.
func AppNeedAlias(owner, slug, kind, name string) (to string, off bool) {
	if strings.TrimSpace(name) == "" {
		return name, false
	}
	for _, n := range LoadAppNeeds(owner, slug) {
		if n.Kind == kind && n.Name == name {
			if n.Off {
				return "", true
			}
			if m := strings.TrimSpace(n.Map); m != "" {
				return m, false
			}
		}
	}
	return name, false
}

// AppNeedRefusal is the error for an app that goes without the kind name it
// names here.
func AppNeedRefusal(owner, slug, kind, name string) error {
	app := slug
	if spec, ok := core.LoadAppSpec(owner, slug); ok && spec.Name != "" {
		app = spec.Name
	}
	return fmt.Errorf("the app %q runs here without the %s %q (it was not mapped when the app was imported): map it under the app's \u2699, Connections", app, kind, name)
}

// AppCredentialAlias is a ToolSession.CredentialAlias for owner's app slug.
func AppCredentialAlias(owner, slug string) func(string) (string, error) {
	return func(name string) (string, error) {
		to, off := AppNeedAlias(owner, slug, "credential", name)
		if off {
			return "", AppNeedRefusal(owner, slug, "credential", name)
		}
		return to, nil
	}
}

// historyNote records an event in an app's history (orchestrate's app
// history wires it); nil where nothing is wired.
var historyNote func(owner, slug, by, message string)

// SetHistoryNote wires what records events in an app's history.
func SetHistoryNote(fn func(owner, slug, by, message string)) { historyNote = fn }

// HistoryNote records in owner's app slug's history what by did to it that
// changes none of its files: an administrator switching it, or one of its
// tools, off or back on. Nothing where apps keep no history.
func HistoryNote(owner, slug, by, message string) {
	if historyNote != nil {
		historyNote(owner, slug, by, message)
	}
}

// AppCredentials is how owner's app lends their credentials to the people
// using it here (docs/design/app-credentials.md): a choice per credential
// (Lend: "read", the default, "own", "write" or "withhold"), the ones kept
// out of the app altogether, their own runs included (Off), and the ones its
// runs have used (Used, name to first use). How this copy runs, never part
// of the app: credentials are this deployment's, so it is not exported.
type AppCredentials struct {
	Lend map[string]string    `json:"lend,omitempty"`
	Off  []string             `json:"off,omitempty"`
	Used map[string]time.Time `json:"used,omitempty"`
}

// Choice is how the app lends credential name: "read" when nothing was
// chosen.
func (a AppCredentials) Choice(name string) string {
	if v := strings.TrimSpace(a.Lend[name]); v != "" {
		return v
	}
	return "read"
}

// IsOff reports whether credential name is kept out of the app.
func (a AppCredentials) IsOff(name string) bool {
	for _, n := range a.Off {
		if n == name {
			return true
		}
	}
	return false
}

const appCredentialsTable = "app_credentials"

var appCredentialsMu sync.Mutex

// LoadAppCredentials is how owner's app slug lends their credentials here.
func LoadAppCredentials(owner, slug string) AppCredentials {
	var out AppCredentials
	if core.RootDB != nil {
		core.RootDB.Get(appCredentialsTable, owner+"/"+slug, &out)
	}
	return out
}

// UpdateAppCredentials changes how owner's app slug lends their credentials,
// under one lock so a choice and a first use never overwrite each other.
func UpdateAppCredentials(owner, slug string, fn func(*AppCredentials)) {
	if core.RootDB == nil {
		return
	}
	appCredentialsMu.Lock()
	defer appCredentialsMu.Unlock()
	a := LoadAppCredentials(owner, slug)
	fn(&a)
	core.RootDB.Set(appCredentialsTable, owner+"/"+slug, a)
}

// usedNoted keeps a first use from being written again on every call.
var usedNoted sync.Map

// NoteAppCredentialUsed records that a run of owner's app slug went out
// through their credential name, the first time it does.
func NoteAppCredentialUsed(owner, slug, name string) {
	key := owner + "/" + slug + "/" + name
	if _, seen := usedNoted.LoadOrStore(key, true); seen {
		return
	}
	if _, ok := LoadAppCredentials(owner, slug).Used[name]; ok {
		return
	}
	UpdateAppCredentials(owner, slug, func(a *AppCredentials) {
		if a.Used == nil {
			a.Used = map[string]time.Time{}
		}
		if _, ok := a.Used[name]; !ok {
			a.Used[name] = time.Now()
		}
	})
}

// DropAppCredentials forgets how a deleted app lent credentials.
func DropAppCredentials(owner, slug string) {
	if core.RootDB != nil {
		core.RootDB.Unset(appCredentialsTable, owner+"/"+slug)
	}
}
