// Package appadmin holds the operator's side of a data-driven app: the
// controls an admin sees for one, and the decisions they have made about it.
//
// A SUBPACKAGE rather than another file in core, because core is at its file
// and export ceiling and this needs neither the hub's types nor its state. It
// takes a narrow view of an app (App) and a three-method slice of the host's
// database (Store), so nothing here forces a caller to drag core in — which is
// what lets both the app host and the pipeline layer register controls without
// importing each other.
//
// What lives here is the operator half only. There are two kinds of knob on an
// app and they have different owners: what the app DOES (an endpoint, a
// threshold, a default) is the author's and belongs on their page; what the app
// is ALLOWED is the operator's and belongs in admin. The test for which is
// which: would the author be surprised to find it changed? A revoked link is a
// decision they can see the consequence of; a rewritten threshold is an edit to
// their app.
//
// See docs/custom-app-admin.md.
package appadmin

import (
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"
)

// Store is the slice of the host's database this package uses.
//
// A local interface, not core.Database: naming the hub's type would put the
// hub in the import graph of a package the hub's own callers use, which is the
// coupling this package was split out to avoid.
type Store interface {
	Get(table, key string, out interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
}

// App is what a control needs to know about the app it is rendering for.
//
// A narrow view rather than the stored spec: a control decides whether it
// applies and what to point at, and neither question needs the page, the
// scripts, or the records.
type App struct {
	Owner       string
	Slug        string
	Name        string
	Shared      bool
	Disabled    bool
	PublicToken string
	PipelineID  string
}

// Control is one operator control on an app's admin pane.
//
// A REGISTRY rather than a hand-assembled pane, for the reason the tunables
// and admin-section registries exist: a pane written by hand is a file that
// grows a case per feature, in a package that then has to know about every
// concern it renders. Here the app that owns a concern registers the control
// for it, and the admin surface stays ignorant of all of them.
type Control struct {
	Key   string // stable id, unique across controls
	Label string
	Help  string
	Group string // "Access" | "Exposure" | "Cost" | "Review"
	Order int    // within a group; ties break by registration order
	// Render builds the control for ONE app, or returns nil when it does not
	// apply to it.
	//
	// Nil is how a control with nothing to say DISAPPEARS instead of rendering
	// dead: a cost dial on an app with no pipeline is a dial for nothing, and a
	// present control that moves nothing is the failure this whole surface is
	// meant to avoid.
	Render func(app App) ui.Component
}

var (
	controls []Control
	regSeq   int
)

// Register adds a control to every app's admin pane. Call once at startup, the
// same self-registration pattern as the rest of the framework. A repeated key
// replaces, so a control is defined exactly once.
func Register(c Control) {
	if c.Key == "" || c.Render == nil {
		return
	}
	for i := range controls {
		if controls[i].Key == c.Key {
			controls[i] = c
			return
		}
	}
	regSeq++
	if c.Order == 0 {
		c.Order = regSeq
	}
	controls = append(controls, c)
}

// For returns the controls that APPLY to one app, rendered, grouped and
// ordered. A control whose Render returns nil is absent rather than empty.
func For(app App) []ui.Component {
	type built struct {
		group string
		order int
		body  ui.Component
	}
	var out []built
	for _, c := range controls {
		body := c.Render(app)
		if body == nil {
			continue
		}
		out = append(out, built{group: c.Group, order: c.Order, body: body})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].group != out[j].group {
			return out[i].group < out[j].group
		}
		return out[i].order < out[j].order
	})
	comps := make([]ui.Component, 0, len(out))
	for _, b := range out {
		comps = append(comps, b.body)
	}
	return comps
}

// Table holds operator decisions, keyed "<owner>/<slug>". Written to the
// deployment ROOT, not a per-user store: these are one person's decisions about
// somebody else's app, and filing them under the owner would put them where the
// owner edits.
const Table = "custom_app_operator"

// State is what an operator has decided about one app.
//
// Deliberately NOT part of the app's stored definition. That document is the
// author's: an operator writing into it would make the definition disagree with
// what its author last saved, and an export would carry one deployment's policy
// into another.
type State struct {
	// AllowedUsers narrows who may reach a SHARED app. Empty means every
	// authenticated user, which is what sharing has always meant — so an unset
	// allowlist changes nothing, and this can be added to a running deployment
	// without quietly locking anybody out of an app they had.
	AllowedUsers []string `json:"allowed_users,omitempty"`
	// DisabledBy records who turned the app off, when it was the operator
	// rather than the owner.
	//
	// One flag, two editors. A second "operator disabled" boolean beside the
	// author's would produce an app that is off in one place and on in another,
	// and a conversation about which one won. Recording WHO instead lets both
	// surfaces say "off, by the operator", which reads correctly to an author
	// who did not turn it off.
	DisabledBy string `json:"disabled_by,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
	Updated    string `json:"updated,omitempty"`
}

func key(owner, slug string) string {
	return strings.TrimSpace(owner) + "/" + strings.TrimSpace(slug)
}

// Load reads an app's operator state. A missing record is the zero value,
// which is "no operator decisions" — the state every app is in until somebody
// makes one.
func Load(db Store, owner, slug string) State {
	var st State
	if db == nil || owner == "" || slug == "" {
		return st
	}
	db.Get(Table, key(owner, slug), &st)
	return st
}

// Save writes it back.
func Save(db Store, owner, slug string, st State) {
	if db == nil || owner == "" || slug == "" {
		return
	}
	db.Set(Table, key(owner, slug), st)
}

// Drop removes it, for an app that no longer exists.
func Drop(db Store, owner, slug string) {
	if db != nil && owner != "" && slug != "" {
		db.Unset(Table, key(owner, slug))
	}
}

// UserMayReach reports whether a user may open a SHARED app.
//
// The owner always may: an allowlist is an operator narrowing who ELSE gets in,
// and locking an author out of their own app is not something anybody asked for
// by setting one. An empty list is every authenticated user, which is what
// sharing meant before this existed.
func UserMayReach(db Store, owner, slug, user string) bool {
	if user == "" {
		return false
	}
	if user == owner {
		return true
	}
	st := Load(db, owner, slug)
	if len(st.AllowedUsers) == 0 {
		return true
	}
	for _, u := range st.AllowedUsers {
		if strings.EqualFold(strings.TrimSpace(u), user) {
			return true
		}
	}
	return false
}
