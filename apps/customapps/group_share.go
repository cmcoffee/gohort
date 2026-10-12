package customapps

// Sharing an app with a group.
//
// Sharing is three steps everywhere in gohort: yours alone, shared with people
// you choose, and the whole deployment. Apps had only the first and the last,
// and the last needs an administrator, because it reaches people the owner
// never chose. This is the middle one for apps: the owner shares with a group
// they are in, with no administrator in the share.
//
// Which groups is the administrator's call, made once per group rather than
// once per share: a group shares only when its "members can share" switch is
// on. Groups began as app bundles, and a broad bundle is close to the whole
// deployment, so the switch is off until somebody decides a group is a team.
//
// Membership is read when the app is opened, so somebody who joins the group
// later gets the app and somebody who leaves loses it. The owner has to still
// be in the group too: "my group" stops being theirs to share with when they
// leave it.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appadmin"
)

// userGroupIDs is the groups a user is assigned.
func userGroupIDs(user string) []string {
	if AuthDB == nil || user == "" {
		return nil
	}
	u, ok := AuthGetUser(AuthDB(), user)
	if !ok {
		return nil
	}
	return u.Groups
}

func inGroup(user, groupID string) bool {
	for _, g := range userGroupIDs(user) {
		if g == groupID {
			return true
		}
	}
	return false
}

// shareableGroup is a group an owner may share with now: it exists, its
// administrator let members share, and the owner is in it.
func shareableGroup(owner, groupID string) (AppGroup, bool) {
	if AuthDB == nil {
		return AppGroup{}, false
	}
	g, ok := LoadAppGroup(AuthDB(), groupID)
	if !ok || !g.Shareable || !inGroup(owner, groupID) {
		return AppGroup{}, false
	}
	return g, true
}

// shareableGroupsOf lists the groups owner may share with, by name.
func shareableGroupsOf(owner string) []AppGroup {
	var out []AppGroup
	for _, id := range userGroupIDs(owner) {
		if g, ok := shareableGroup(owner, id); ok {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// groupShareReaches says whether user reaches spec through one of the groups
// its owner shared it with.
func groupShareReaches(spec AppSpec, user string) bool {
	for _, id := range spec.SharedGroups {
		if _, ok := shareableGroup(spec.Owner, id); ok && inGroup(user, id) {
			return true
		}
	}
	return false
}

// appReaches is whether user may open another owner's app: shared with
// everyone signed in (an administrator approved it, and may have narrowed
// it), or shared with a group user is in. The one rule the page, My Apps and
// the dashboard all ask, so they cannot disagree about who sees what.
func appReaches(spec AppSpec, user string) bool {
	if spec.Shared && appadmin.UserMayReach(RootDB, spec.Owner, spec.Slug, user) {
		return true
	}
	return groupShareReaches(spec, user)
}

// sharedGroupNames names the groups spec reaches now, and the ones it was
// shared with that no longer carry it (switched off, gone, or the owner left).
func sharedGroupNames(spec AppSpec) (live, lapsed []string) {
	for _, id := range spec.SharedGroups {
		if g, ok := shareableGroup(spec.Owner, id); ok {
			live = append(live, g.Name)
			continue
		}
		name := id
		if AuthDB != nil {
			if g, ok := LoadAppGroup(AuthDB(), id); ok {
				name = g.Name
			}
		}
		lapsed = append(lapsed, name)
	}
	return live, lapsed
}

// groupMemberCount counts the users assigned a group.
func groupMemberCount(groupID string) int {
	if AuthDB == nil {
		return 0
	}
	n := 0
	for _, u := range AuthListUsers(AuthDB()) {
		for _, g := range u.Groups {
			if g == groupID {
				n++
				break
			}
		}
	}
	return n
}

// shareGroupsJSON is the Share dialog's group list for one app: every group
// the owner may share with, and whether this app is shared with it.
func shareGroupsJSON(spec AppSpec) string {
	on := map[string]bool{}
	for _, id := range spec.SharedGroups {
		on[id] = true
	}
	type row struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Members int    `json:"members"`
		On      bool   `json:"on"`
	}
	rows := []row{}
	for _, g := range shareableGroupsOf(spec.Owner) {
		rows = append(rows, row{ID: g.ID, Name: g.Name, Members: groupMemberCount(g.ID), On: on[g.ID]})
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

// setGroupShare shares the owner's app with one group, or stops. The slug
// index carries a group-shared app the way it carries a shared one, so a
// member's request finds its owner; it goes when nothing shares the app.
func (T *CustomApps) setGroupShare(owner, slug, groupID string, on bool) error {
	spec, ok := loadSpec(owner, slug)
	if !ok {
		return Error("no app " + slug + " for " + owner)
	}
	if on {
		if _, ok := shareableGroup(owner, groupID); !ok {
			return Error("that group is not one you can share with: you need to be in it, and its administrator has to let members share")
		}
		if other, shared := LookupSharedOwner(T.DB, sharedAppsIndex, slug); shared && other != owner {
			return Error("another user already shares an app at this slug: rename yours to share it")
		}
	}
	kept := spec.SharedGroups[:0:0]
	for _, id := range spec.SharedGroups {
		if id != groupID {
			kept = append(kept, id)
		}
	}
	if on {
		kept = append(kept, groupID)
	}
	spec.SharedGroups = kept
	SaveAppSpec(spec)
	if len(kept) > 0 {
		SetSharedOwner(T.DB, sharedAppsIndex, slug, owner, true)
	} else if !spec.Shared {
		T.unshareOwned(owner, slug)
	}
	return nil
}

// handleShareGroup is POST _app/share_group?slug=&group=&on=true|false, for an
// app the requester owns. No administrator: which groups may be shared with
// was theirs to decide, once, on the group.
func (T *CustomApps) handleShareGroup(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	slug, group := strings.TrimSpace(q.Get("slug")), strings.TrimSpace(q.Get("group"))
	if slug == "" || group == "" {
		http.Error(w, "send slug and group", http.StatusBadRequest)
		return
	}
	on := q.Get("on") != "false"
	if err := T.setGroupShare(user, slug, group, on); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	Log("[customapps] %s %s app %q with group %q", user, map[bool]string{true: "shared", false: "unshared"}[on], slug, group)
	writeJSON(w, map[string]any{"ok": true, "on": on})
}
