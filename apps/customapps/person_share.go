package customapps

// Sharing an app with people by name.
//
// Every other kind of thing in gohort can be shared with the people its owner
// names: agents, skills, collections, tools, pipelines, machines. Apps could
// be shared with a group the owner is in, or published to everyone through an
// administrator, but not with one colleague. This is that: the owner's call,
// no administrator in it, use-only like a group share (each person runs their
// own copy, and the owner's scripts run with the owner's credentials for them).

import (
	"encoding/json"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// personShareReaches says whether the owner shared spec with user by name.
func personShareReaches(spec AppSpec, user string) bool {
	for _, u := range spec.SharedWith {
		if strings.EqualFold(strings.TrimSpace(u), strings.TrimSpace(user)) {
			return true
		}
	}
	return false
}

// appSharedAnyWay reports whether anything still shares spec: published,
// with a group, or with a person. The slug index carries the app while any
// of them does, so a recipient's request finds its owner.
func appSharedAnyWay(spec AppSpec) bool {
	return spec.Shared || len(spec.SharedGroups) > 0 || len(spec.SharedWith) > 0
}

// setPersonShare shares the owner's app with one person, or stops.
func (T *CustomApps) setPersonShare(owner, slug, person string, on bool) error {
	spec, ok := loadSpec(owner, slug)
	if !ok {
		return Error("no app " + slug + " for " + owner)
	}
	person = strings.TrimSpace(person)
	if person == "" {
		return Error("say who to share it with")
	}
	if on {
		if strings.EqualFold(person, owner) {
			return Error("it is already yours")
		}
		if AuthDB == nil {
			return Error("no user store")
		}
		u, ok := AuthGetUser(AuthDB(), person)
		if !ok {
			return Error("no user named " + person)
		}
		person = u.Username
		if other, shared := LookupSharedOwner(T.DB, sharedAppsIndex, slug); shared && other != owner {
			return Error("another user already shares an app at this slug: rename yours to share it")
		}
	}
	kept := spec.SharedWith[:0:0]
	for _, u := range spec.SharedWith {
		if !strings.EqualFold(u, person) {
			kept = append(kept, u)
		}
	}
	if on {
		kept = append(kept, person)
		sort.Strings(kept)
	}
	spec.SharedWith = kept
	SaveAppSpec(spec)
	if appSharedAnyWay(spec) {
		SetSharedOwner(T.DB, sharedAppsIndex, slug, owner, true)
	} else {
		T.unshareOwned(owner, slug)
	}
	return nil
}

// handleSharePerson is POST _app/share_person?slug=&user=&on=true|false, for
// an app the requester owns.
func (T *CustomApps) handleSharePerson(c *jsonapi.Call, _ struct{}) (any, error) {
	user, q := c.User, c.R.URL.Query()
	slug, person := strings.TrimSpace(q.Get("slug")), strings.TrimSpace(q.Get("user"))
	if slug == "" || person == "" {
		return nil, jsonapi.BadRequest("send slug and user")
	}
	on := q.Get("on") != "false"
	if err := T.setPersonShare(user, slug, person, on); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[customapps] %s %s app %q with %q", user, map[bool]string{true: "shared", false: "unshared"}[on], slug, person)
	return map[string]any{"ok": true, "on": on}, nil
}

// sharePeopleJSON is the Share dialog's people section for one app: who it
// is shared with, and everyone else it could be.
func sharePeopleJSON(spec AppSpec) string {
	out := struct {
		With   []string `json:"with"`
		Others []string `json:"others"`
	}{With: append([]string{}, spec.SharedWith...), Others: []string{}}
	if AuthDB != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			if strings.EqualFold(u.Username, spec.Owner) || personShareReaches(spec, u.Username) {
				continue
			}
			out.Others = append(out.Others, u.Username)
		}
	}
	sort.Strings(out.Others)
	b, _ := json.Marshal(out)
	return string(b)
}
