// Operator administration of custom apps.
//
// A compiled app registers its dials at startup — a route stage, a tunable, an
// admin section — and the admin page renders them. A custom app registers
// nothing, because it does not exist at startup: it is a record somebody wrote
// at runtime, under their own name. So the operator questions had no home. Who
// may reach this app. Its public link is live, revoke it.
//
// What lives here is the operator half only. There are two kinds of knob on an
// app and they have different owners: what the app DOES (an endpoint, a
// threshold, a default) is the author's and belongs on their page, and what the
// app is ALLOWED is the operator's and belongs in admin. The test for which is
// which: would the author be surprised to find it changed? A revoked link is a
// decision they can see the consequence of; a rewritten threshold is an edit to
// their app.
//
// See docs/custom-app-admin.md.
package core

import (
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"
)

// CustomAppControl is one operator control on an app's admin pane.
//
// A REGISTRY rather than a hand-assembled pane, for the reason RegisterTunable
// and RegisterAdminSection are registries: a pane written by hand is a file
// that grows a case per feature, in a package that then has to know about every
// concern it renders. Here the app that owns a concern registers the control
// for it, and the admin surface stays ignorant of all of them.
type CustomAppControl struct {
	Key   string // stable id, unique across controls
	Label string
	Help  string
	Group string // "Access" | "Exposure" | "Cost" | "Review"
	Order int    // within a group; ties break by registration order
	// Render builds the control for ONE app, or returns nil when it does not
	// apply to it.
	//
	// Nil is how a control that has nothing to say disappears instead of
	// rendering dead — a cost dial on an app with no pipeline is a dial for
	// nothing, and a present control that moves nothing is the failure this
	// codebase keeps finding.
	Render func(spec AppSpec) ui.Component
}

var (
	customAppControls   []CustomAppControl
	customAppControlSeq int
)

// RegisterCustomAppControl adds a control to every custom app's admin pane.
// Call once at startup, the same self-registration pattern as the rest.
func RegisterCustomAppControl(c CustomAppControl) {
	if c.Key == "" || c.Render == nil {
		return
	}
	for i := range customAppControls {
		if customAppControls[i].Key == c.Key {
			customAppControls[i] = c // last registration wins, like a tunable
			return
		}
	}
	customAppControlSeq++
	if c.Order == 0 {
		c.Order = customAppControlSeq
	}
	customAppControls = append(customAppControls, c)
}

// CustomAppControlsFor returns the controls that APPLY to one app, already
// rendered, grouped and ordered. A control whose Render returns nil is absent
// rather than empty.
func CustomAppControlsFor(spec AppSpec) []ui.Component {
	type built struct {
		group string
		order int
		body  ui.Component
	}
	var out []built
	for _, c := range customAppControls {
		body := c.Render(spec)
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

// CustomAppOperatorTable holds operator decisions about custom apps, keyed
// "<owner>/<slug>". In the deployment ROOT, not a per-user store: these are the
// operator's decisions about somebody else's app, and filing them under the
// owner would put them where the owner edits.
const CustomAppOperatorTable = "custom_app_operator"

// CustomAppOperatorState is what an operator has decided about one app.
//
// Deliberately NOT part of AppSpec. The spec is the author's document; an
// operator writing into it would make the app's own definition disagree with
// what its author last saved, and an export would carry one deployment's
// policy into another.
type CustomAppOperatorState struct {
	// AllowedUsers narrows who may reach a SHARED app. Empty means every
	// authenticated user, which is what sharing has always meant — so an
	// unset allowlist changes nothing, and this can be added to a running
	// deployment without quietly locking anybody out of an app they had.
	AllowedUsers []string `json:"allowed_users,omitempty"`
	// DisabledBy records who turned the app off, when it was the operator
	// rather than the owner.
	//
	// One flag, two editors. A second "operator disabled" boolean beside the
	// author's would produce an app that is off in one place and on in
	// another, and a conversation about which one won; recording WHO instead
	// lets both surfaces say "off, by the operator", which reads correctly to
	// an author who did not turn it off.
	DisabledBy string `json:"disabled_by,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
	Updated    string `json:"updated,omitempty"`
}

func customAppOperatorKey(owner, slug string) string {
	return strings.TrimSpace(owner) + "/" + strings.TrimSpace(slug)
}

// LoadCustomAppOperatorState reads an app's operator state. A missing record is
// the zero value, which is "no operator decisions" — the state every app is in
// until somebody makes one.
func LoadCustomAppOperatorState(owner, slug string) CustomAppOperatorState {
	var st CustomAppOperatorState
	db := RootDB
	if db == nil || owner == "" || slug == "" {
		return st
	}
	db.Get(CustomAppOperatorTable, customAppOperatorKey(owner, slug), &st)
	return st
}

// SaveCustomAppOperatorState writes it back.
func SaveCustomAppOperatorState(owner, slug string, st CustomAppOperatorState) {
	db := RootDB
	if db == nil || owner == "" || slug == "" {
		return
	}
	db.Set(CustomAppOperatorTable, customAppOperatorKey(owner, slug), st)
}

// DropCustomAppOperatorState removes it, for an app that no longer exists.
func DropCustomAppOperatorState(owner, slug string) {
	if db := RootDB; db != nil && owner != "" && slug != "" {
		db.Unset(CustomAppOperatorTable, customAppOperatorKey(owner, slug))
	}
}

// CustomAppUserMayReach reports whether a user may open a SHARED app.
//
// The owner always may: an allowlist is the operator narrowing who else gets
// in, and locking an author out of their own app is not a thing an operator
// asked for by setting one. An empty list is every authenticated user, which
// is what sharing meant before this existed.
func CustomAppUserMayReach(owner, slug, user string) bool {
	if user == "" {
		return false
	}
	if user == owner {
		return true
	}
	st := LoadCustomAppOperatorState(owner, slug)
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
