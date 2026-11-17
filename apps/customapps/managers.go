package customapps

// An app's managers: the people its owner trusts to run it for its users
// (AppSpec.Managers). The app keeps for them what it marks managers_only (an
// action, a data source, a section), and its scripts see what each person is
// to the app as caller_role, so "a manager may remove anyone's suggestion" is
// one check in the script. The host does the refusing: a script never sees a
// username, and a setting could neither hide a section nor stop a call.

import (
	"encoding/json"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// managersOnlyRefusal is what an ordinary user asking for a managers' part
// of an app is told.
const managersOnlyRefusal = "this part of the app is for its managers"

// appPageFor is the page user sees: the app's own, or, for an ordinary user
// of an app that keeps something for managers, the page without it.
func appPageFor(spec AppSpec, user string) json.RawMessage {
	if len(spec.UserPage) > 0 && spec.Role(user) == "user" {
		return spec.UserPage
	}
	return spec.Page
}

// forUser is spec without what it keeps for managers, when user is an
// ordinary user of it: what the app's chat agent gets while answering them.
func forUser(spec AppSpec, user string) AppSpec {
	if spec.Role(user) != "user" {
		return spec
	}
	out := spec
	out.Actions, out.DataSources = nil, nil
	for _, a := range spec.Actions {
		if !a.ManagersOnly {
			out.Actions = append(out.Actions, a)
		}
	}
	for _, d := range spec.DataSources {
		if !d.ManagersOnly {
			out.DataSources = append(out.DataSources, d)
		}
	}
	return out
}

// setManager names one person a manager of the owner's app, or stops. As
// with a maintainer, naming one shares the app with them; taking one off
// leaves them able to use it.
func (T *CustomApps) setManager(owner, slug, person string, on bool) error {
	spec, ok := loadSpec(owner, slug)
	if !ok {
		return Error("no app " + slug + " for " + owner)
	}
	person = strings.TrimSpace(person)
	if person == "" {
		return Error("say who manages it")
	}
	if !on {
		spec.Managers = withoutPerson(spec.Managers, person)
		SaveAppSpec(spec)
		return nil
	}
	if strings.EqualFold(person, owner) {
		return Error("it is already yours")
	}
	if AuthDB == nil {
		return Error("no user store")
	}
	u, found := AuthGetUser(AuthDB(), person)
	if !found {
		return Error("no user named " + person)
	}
	if !personShareReaches(spec, u.Username) {
		if err := T.setPersonShare(owner, slug, u.Username, true); err != nil {
			return err
		}
		if spec, ok = loadSpec(owner, slug); !ok {
			return Error("no app " + slug + " for " + owner)
		}
	}
	spec.Managers = append(withoutPerson(spec.Managers, u.Username), u.Username)
	sort.Strings(spec.Managers)
	SaveAppSpec(spec)
	return nil
}

// handleShareManager is POST _app/share_manager?slug=&user=&on=true|false,
// for an app the requester owns.
func (T *CustomApps) handleShareManager(c *jsonapi.Call, _ struct{}) (any, error) {
	user, q := c.User, c.R.URL.Query()
	slug, person := strings.TrimSpace(q.Get("slug")), strings.TrimSpace(q.Get("user"))
	if slug == "" || person == "" {
		return nil, jsonapi.BadRequest("send slug and user")
	}
	on := q.Get("on") != "false"
	if err := T.setManager(user, slug, person, on); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[customapps] %s %s %q as a manager of app %q", user, map[bool]string{true: "named", false: "removed"}[on], person, slug)
	return map[string]any{"ok": true, "on": on}, nil
}

// shareManagersJSON is the Share dialog's managers section for one app: who
// manages it, and everyone else who could.
func shareManagersJSON(spec AppSpec) string {
	out := struct {
		With   []string `json:"with"`
		Others []string `json:"others"`
	}{With: append([]string{}, spec.Managers...), Others: []string{}}
	if AuthDB != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			if strings.EqualFold(u.Username, spec.Owner) || spec.ManagedBy(u.Username) {
				continue
			}
			out.Others = append(out.Others, u.Username)
		}
	}
	sort.Strings(out.With)
	sort.Strings(out.Others)
	b, _ := json.Marshal(out)
	return string(b)
}
