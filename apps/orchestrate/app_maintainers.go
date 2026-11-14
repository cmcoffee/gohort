package orchestrate

// App maintainers: the people who keep somebody else's app working.
//
// An app shared with people gives them the app, not the record. A maintainer
// (AppSpec.Maintainers, named on the app's Share dialog) changes its sections
// and wording through Builder, in the owner's copy, the one there is, and the
// version they replace is kept under their name.
//
// What a maintainer may change is a LIST, applyAppMaintainerEdit, not a list
// of what they may not: every field off it is copied from the owner's record,
// so a field added to apps later is the owner's until somebody decides
// otherwise. Off it are the app's scripts (they run in the owner's sandbox,
// with the owner's credentials), its page code (it runs for everyone who
// opens the app), its reach and its settings. docs/design/app-maintainers.md.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/core/peershare"
)

// MaintainedAppsTable indexes app maintainers: maintainer -> (owner, slug).
const MaintainedAppsTable = "maintained_apps"

func init() {
	// The index follows the record on every save, so a maintainer named or
	// dropped on the Share dialog, or taken off with a deleted account, is
	// found (or not) at once. Every read re-checks the record.
	RegisterAppSpecSavedHook(func(s AppSpec) {
		if RootDB != nil && s.Owner != "" && s.Slug != "" {
			peershare.SetRecipients(RootDB, MaintainedAppsTable, s.Owner, s.Slug, s.Maintainers)
		}
	})
	RegisterAppSpecDeletedHook(func(owner, slug string) {
		if RootDB != nil {
			peershare.DropAll(RootDB, MaintainedAppsTable, owner, slug)
		}
	})
}

// maintainedApps returns the apps user maintains for other people, read from
// each owner's store and re-checked against the app's own list.
func maintainedApps(user string) []AppSpec {
	if RootDB == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	var out []AppSpec
	for _, ref := range SharedRefsFor(RootDB, MaintainedAppsTable, user) {
		if s, ok := LoadAppSpec(ref.Owner, ref.ID); ok && s.MaintainedBy(user) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// maintainedApp finds the app at slug that user maintains for its owner.
// False when they maintain none there.
func maintainedApp(user, slug string) (AppSpec, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return AppSpec{}, false
	}
	for _, s := range maintainedApps(user) {
		if s.Slug == slug {
			return s, true
		}
	}
	return AppSpec{}, false
}

// appForTurn is the app at slug this turn's person works on: their own, or
// one they maintain for its owner (maintained true).
func (t *chatTurn) appForTurn(slug string) (spec AppSpec, maintained, ok bool) {
	if s, found := LoadAppSpec(t.user, slug); found {
		return s, false, true
	}
	if s, found := maintainedApp(t.user, slug); found {
		return s, true, true
	}
	return AppSpec{}, false, false
}

// appMaintainerActions are the app_def actions a maintainer may take on an
// app they maintain. Everything else is the owner's.
var appMaintainerActions = map[string]bool{
	"get": true, "update": true, "update_section": true, "add_section": true, "remove_section": true,
}

// appMaintainerUpdateKeys are the update keys a maintainer may send: the
// app's sections, its words, its layout, and the call's own bookkeeping.
var appMaintainerUpdateKeys = map[string]bool{
	"action": true, "id": true, "slug": true, "name": true, "description": true, "notes": true,
	"note": true, "sections": true, "full_width": true, "narrow": true, "confirm_rewrite": true, "_reason": true,
	"checks": true,
}

// appMaintainerRefusal is why a maintainer may not take this action on the
// app they maintain, or "".
func appMaintainerRefusal(action string, args map[string]any, spec AppSpec) string {
	owner := spec.Owner
	if !appMaintainerActions[action] {
		return fmt.Sprintf("NOT DONE: you maintain %q for %s. A maintainer changes its sections and wording (update_section, add_section, remove_section, or update with name, description, notes or sections) and reads it (get). %s is %s's: ask them.",
			spec.Slug, owner, appActionNoun(action), owner)
	}
	if action == "update" {
		var off []string
		for k := range args {
			if !appMaintainerUpdateKeys[k] {
				off = append(off, k)
			}
		}
		if len(off) > 0 {
			sort.Strings(off)
			return fmt.Sprintf("NOT SAVED: you maintain %q for %s, and %s %s theirs to change: its scripts, page code, sharing and settings stay with its owner. Send name, description, notes or sections only, or ask %s.",
				spec.Slug, owner, strings.Join(off, ", "), map[bool]string{true: "is", false: "are"}[len(off) == 1], owner)
		}
	}
	return ""
}

// appActionNoun names what an action changes, for the refusal.
func appActionNoun(action string) string {
	switch action {
	case "patch_html", "patch", "replace_function", "patch_function":
		return "Its page code"
	case "delete":
		return "Deleting it"
	case "revert", "undo", "restore":
		return "Restoring an earlier version"
	case "add_asset", "remove_asset":
		return "Its files"
	default:
		return "That"
	}
}

// applyAppMaintainerEdit is prior with what a maintainer may change taken from
// next, and everything else kept as the owner has it. It refuses an edit that
// would change the app's page code or point a section at another pipeline:
// both run as more than the words on the page.
func applyAppMaintainerEdit(prior, next AppSpec) (AppSpec, error) {
	out := prior
	// The app's words, layout and sections, and what the host derives from
	// them.
	out.Name, out.Desc, out.Notes, out.ChangeNote = next.Name, next.Desc, next.Notes, next.ChangeNote
	out.FullWidth, out.Narrow = next.FullWidth, next.Narrow
	out.Sections, out.Page = next.Sections, next.Page
	out.RecordFields, out.Sample, out.Verify = next.RecordFields, next.Sample, next.Verify
	out.Updated = next.Updated
	// Checks: a maintainer adds and changes them, and removes none, so a
	// check their change breaks cannot be cleared on the way.
	for _, had := range prior.Checks {
		if !appHasCheck(next.Checks, had.Name) {
			return prior, fmt.Errorf("NOT SAVED: this removes the check %q from %q. Removing a check is %s's: change it if the feature changed on purpose, or ask them", had.Name, prior.Slug, prior.Owner)
		}
	}
	out.Checks = next.Checks
	if !sameStringSet(prior.PageCode(), out.PageCode()) {
		return prior, fmt.Errorf("NOT SAVED: this changes the page code of %q (an html or card section's HTML, or script in the page). That runs for everyone who opens the app, so it is %s's to change: ask them", prior.Slug, prior.Owner)
	}
	if p := appSectionPipelines(out); !subsetOf(p, append(appSectionPipelines(prior), prior.PipelineID)) {
		return prior, fmt.Errorf("NOT SAVED: a section of %q now runs a pipeline the app did not run before. Which pipelines the app runs is %s's to change: ask them", prior.Slug, prior.Owner)
	}
	return out, nil
}

// appSectionPipelines is every pipeline_id a section of spec names.
func appSectionPipelines(spec AppSpec) []string {
	secs, err := appAuthoringSections(spec)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range secs {
		if id := strings.TrimSpace(mapStr(m, "pipeline_id")); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// sameStringSet reports whether a and b hold the same strings, as many times
// each.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
}

// subsetOf reports whether every string in a is in b.
func subsetOf(a, b []string) bool {
	for _, s := range a {
		if !slices.Contains(b, s) {
			return false
		}
	}
	return true
}

// appHasCheck reports whether checks has one named name.
func appHasCheck(checks []appcheck.Check, name string) bool {
	for _, c := range checks {
		if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}
