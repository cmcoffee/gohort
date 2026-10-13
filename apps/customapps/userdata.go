package customapps

// An account's apps when the account is deleted: the administrator moves them
// to somebody else or deletes them, the same choice every app offers through
// the user-data step on the delete page.
//
// Never left with no owner. An app runs its scripts as its owner, so a shared
// app left behind by a deleted account kept serving everyone it was shared
// with, running as a name that no longer existed and with its credentials
// revoked; and the next account given that name would have inherited it.

import (
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appadmin"
)

type appsUserData struct{ app *CustomApps }

func (h *appsUserData) AppName() string { return "apps" }

// Describe counts the account's apps, and how many reach other people: those
// are the ones whose deletion somebody else would notice.
func (h *appsUserData) Describe(uid string) UserDataSummary {
	sum := UserDataSummary{AppName: "apps", Counts: map[string]int{}, Actions: []string{"reassign", "purge"}}
	specs := listSpecs(uid)
	if len(specs) == 0 {
		return sum
	}
	sum.Counts["apps"] = len(specs)
	for _, s := range specs {
		if appSharedAnyWay(s) {
			sum.Counts["shared"]++
		}
	}
	return sum
}

// Reassign moves every app the account owns to another owner.
func (h *appsUserData) Reassign(from, to string) error {
	var failed []string
	for _, s := range listSpecs(from) {
		if err := h.app.moveApp(s, to); err != nil {
			failed = append(failed, s.Slug+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return errors.New("could not move " + strings.Join(failed, "; "))
	}
	return nil
}

func (h *appsUserData) Anonymize(string) error { return ErrUserDataActionNotSupported }

// Purge deletes every app the account owns, as the delete button would.
func (h *appsUserData) Purge(uid string) error {
	for _, s := range listSpecs(uid) {
		h.app.deleteApp(uid, s.Slug)
	}
	forgetAppReader(uid)
	return nil
}

// forgetAppReader takes a deleted account off every shared app's "who can
// open it" list, whoever owns the app. Empty there means every signed-in
// user, so a list it was the only name on keeps a marker that matches nobody
// instead of opening the app to everyone.
func forgetAppReader(uid string) {
	if RootDB == nil || uid == "" {
		return
	}
	// The apps shared with them by name, whoever owns them.
	if AuthDB != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			for _, spec := range listSpecs(u.Username) {
				if !personShareReaches(spec, uid) {
					continue
				}
				kept := spec.SharedWith[:0:0]
				for _, p := range spec.SharedWith {
					if !strings.EqualFold(p, uid) {
						kept = append(kept, p)
					}
				}
				spec.SharedWith = kept
				SaveAppSpec(spec)
			}
		}
	}
	for _, k := range RootDB.Keys(appadmin.Table) {
		var st appadmin.State
		if !RootDB.Get(appadmin.Table, k, &st) {
			continue
		}
		kept := st.AllowedUsers[:0:0]
		for _, u := range st.AllowedUsers {
			if u != uid {
				kept = append(kept, u)
			}
		}
		if len(kept) == len(st.AllowedUsers) {
			continue
		}
		if len(kept) == 0 {
			kept = []string{"(removed account: " + uid + ")"}
		}
		st.AllowedUsers = kept
		RootDB.Set(appadmin.Table, k, st)
	}
}

// moveApp gives one app to a new owner, with what makes it the same app: the
// owner's own records, its asset files, its sharing and its operator settings.
// The people it is shared with keep their own copies, which are filed under
// them, not the owner. Scripts run as the new owner from here on, with the new
// owner's credentials.
//
// A new owner who already has an app at this slug gets this one under a free
// one ("<slug>-2"): their own app is never overwritten. Readers of a renamed
// shared app then start fresh copies, since copies are filed by slug.
func (T *CustomApps) moveApp(spec AppSpec, to string) error {
	from, slug := spec.Owner, spec.Slug
	if to == "" || to == from {
		return errors.New("no one to move it to")
	}
	newSlug := slug
	for n := 2; ; n++ {
		if _, taken := loadSpec(to, newSlug); !taken {
			if other, shared := LookupSharedOwner(T.DB, sharedAppsIndex, newSlug); !shared || other == from || other == to {
				break
			}
		}
		newSlug = fmt.Sprintf("%s-%d", slug, n)
	}
	moved := spec
	moved.Owner, moved.Slug = to, newSlug
	if spec.PrivateDB && spec.DataOwner == "" {
		moved.DataOwner = from // the file stays where the data is
	}

	// What the app keeps under its owner: their records, every shared
	// collection (everyone's entries live on the owner's side), and the owner's
	// settings. Then the spec, then the old one goes.
	src, dst := T.recordBase(spec, from), T.recordBase(moved, to)
	if src != nil && dst != nil {
		moveRows := func(from, to string) {
			for _, k := range src.Keys(from) {
				var rec map[string]any
				if src.Get(from, k, &rec) {
					dst.Set(to, k, rec)
				}
			}
			src.Drop(from)
		}
		moveRows(recTable(slug), recTable(newSlug))
		for _, name := range spec.SharedCollections {
			moveRows(sharedTable(slug, name), sharedTable(newSlug, name))
		}
		if vals := loadSettingValues(src, slug); len(vals) > 0 {
			dst.Set(settingsTable, newSlug, vals)
			src.Unset(settingsTable, slug)
		}
	}
	if names, err := ListAppAssets(from, slug); err == nil {
		for _, name := range names {
			if data, _, err := ReadAppAsset(from, slug, name); err == nil {
				SaveAppAsset(to, newSlug, name, data)
			}
		}
	}
	if st := appadmin.Load(RootDB, from, slug); len(st.AllowedUsers) > 0 || st.DisabledBy != "" {
		appadmin.Save(RootDB, to, newSlug, st)
	}
	SaveAppSpec(moved) // arms its schedules for the new owner
	wasShared := appSharedAnyWay(spec)
	T.deleteApp(from, slug)
	if wasShared {
		SetSharedOwner(T.DB, sharedAppsIndex, newSlug, to, true)
	}
	Log("[customapps] moved app %q from %s to %s as %q", slug, from, to, newSlug)
	return nil
}
