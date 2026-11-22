// Package userprofile is how an administrator shapes gohort for a set of
// people: a profile names who it is for (people, or whole teams), what gohort
// is called for them, where it opens, which apps it shows, which of their
// computer's tools the desktop bridge lends their agents, and what it takes
// away from them altogether.
//
// What it shows (the name, the start page, the apps) is presentation, and
// applies in the desktop app always and in a browser when the profile says
// so (AlsoWeb): the server knows a request is the desktop app's
// (core.DesktopClientUser) and applies it there, per request, so a change
// reaches everyone on their next page with nothing rebuilt or reinstalled.
// What it removes (Removes) is enforced, wherever the person reaches gohort
// from: a section it takes away is gone from their pages and refused at its
// endpoints, and a feature it removes is denied as Feature Access denies one.
// A profile only ever takes away: what a person may do is still their grants'
// to decide.
//
// Profiles began as desktop profiles; they are still kept in that table, so
// the ones made then load as they were (desktop only, removing nothing).
//
// A leaf: the database arrives through SetStore, the people a profile names
// are usernames, its teams are team ids, and who is on a team is the
// caller's to say (For).
package userprofile

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Store is the slice of the database this package needs; core's Database
// satisfies it.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// table is where profiles are kept: the name they had as desktop profiles.
const table = "desktop_profiles"

var (
	mu    sync.Mutex
	store Store
)

// resolve finds the profile that shapes a person: core's, which knows their
// teams and whether they are an administrator (SetResolver).
var resolve func(user string) (Profile, bool)

// SetResolver is how Of finds a person's profile. core sets it at start; until
// then no one has one.
func SetResolver(fn func(user string) (Profile, bool)) {
	mu.Lock()
	resolve = fn
	mu.Unlock()
}

// Of is the profile that shapes user, ok false for none (or for an
// administrator, whom no profile shapes).
func Of(user string) (Profile, bool) {
	mu.Lock()
	fn := resolve
	mu.Unlock()
	if fn == nil || strings.TrimSpace(user) == "" {
		return Profile{}, false
	}
	return fn(user)
}

// TakesFrom reports whether user's profile takes away what (a Remove
// constant, or FeaturePrefix + a feature's key).
func TakesFrom(user, what string) bool {
	p, ok := Of(user)
	return ok && p.Takes(what)
}

// Refusal is what a person is told when their profile takes away what they
// asked for.
const Refusal = "your profile does not include this: ask an administrator (Admin > People > User profiles)"

// SetStore is where profiles are kept. Until it is called there are none.
func SetStore(s Store) {
	mu.Lock()
	store = s
	mu.Unlock()
}

// What a profile can take away (Profile.Removes): the Account page's
// sections by these names, or a Feature Access feature as FeaturePrefix + its
// key.
const (
	RemoveAPIKeys           = "api_keys"           // personal API keys: made, listed or used
	RemoveConnectedAccounts = "connected_accounts" // keys and sign-ins integrations use
	RemoveYourData          = "your_data"          // exporting and importing everything one owns
	FeaturePrefix           = "feature:"
)

// Sections are the parts of a person's pages a profile can take away, with
// what each is called on the admin form.
var Sections = []struct{ Key, Label string }{
	{RemoveAPIKeys, "API keys (making, listing and using personal keys)"},
	{RemoveConnectedAccounts, "Connected accounts (keys and sign-ins for integrations)"},
	{RemoveYourData, "Your data (export everything, import)"},
}

// Profile is one shape of gohort, for the people it names.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"` // what the app is called for them
	// StartPath is where the app opens: a page of this deployment ("/apps/desk/"),
	// in place of the dashboard. Empty opens the dashboard.
	StartPath string `json:"start_path,omitempty"`
	// Apps are the apps' paths the dashboard shows ("/apps", "/anvil"); empty
	// shows every app the person may use. A list narrows what is shown, not
	// what they may open: their grants still decide that.
	Apps []string `json:"apps,omitempty"`
	// Tools are the computer's tools the bridge lends their agents, by name
	// as the bridge announces them ("filesystem_read"); empty lends them all.
	Tools []string `json:"tools,omitempty"`
	// Users are the people it is for, by username, and Teams the teams, by
	// id. A profile naming no one and no team is the default: it applies to
	// everyone no other profile names.
	Users []string `json:"users,omitempty"`
	Teams []string `json:"teams,omitempty"`
	// AlsoWeb shapes a browser as well as the desktop app: the name, the start
	// page and the apps. Off, a profile shapes only the desktop app, as every
	// profile made before this did. Removes holds either way.
	AlsoWeb bool `json:"also_web,omitempty"`
	// Removes is what the profile takes away from its people (the Remove
	// constants, or FeaturePrefix + a feature's key), enforced wherever they
	// reach gohort from.
	Removes []string `json:"removes,omitempty"`
}

// IsDefault reports whether p is the default profile: it names no one and
// no team.
func (p Profile) IsDefault() bool { return len(p.Users) == 0 && len(p.Teams) == 0 }

// Takes reports whether p takes away what (a Remove constant, or
// FeaturePrefix + a feature's key).
func (p Profile) Takes(what string) bool { return names(p.Removes, what) }

// TakesFeature reports whether p removes the Feature Access feature key.
func (p Profile) TakesFeature(key string) bool {
	return strings.TrimSpace(key) != "" && p.Takes(FeaturePrefix+strings.TrimSpace(key))
}

// Normalize trims the fields, drops blanks and duplicates, and makes each
// path start with "/".
func (p Profile) Normalize() Profile {
	p.ID, p.Name = strings.TrimSpace(p.ID), strings.TrimSpace(p.Name)
	p.StartPath = cleanPath(p.StartPath)
	var apps []string
	for _, a := range p.Apps {
		if a = cleanPath(a); a != "" {
			apps = append(apps, a)
		}
	}
	p.Apps = dedupe(apps)
	p.Tools = dedupe(p.Tools)
	p.Users = dedupe(p.Users)
	p.Teams = dedupe(p.Teams)
	p.Removes = dedupe(p.Removes)
	return p
}

func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	return out
}

// Check says what is wrong with p, nil when nothing is.
func Check(p Profile) error {
	p = p.Normalize()
	if p.Name == "" {
		return fmt.Errorf("give the profile a name: it is what the app is called for its people")
	}
	if p.StartPath != "" && (strings.Contains(p.StartPath, "://") || strings.HasPrefix(p.StartPath, "//")) {
		return fmt.Errorf("the start page is a page of this deployment, as a path such as /apps/desk/, not an address elsewhere")
	}
	for _, r := range p.Removes {
		if !knownRemove(r) {
			return fmt.Errorf("%q is not something a profile can take away", r)
		}
	}
	return nil
}

// knownRemove reports whether r is something a profile can take away.
func knownRemove(r string) bool {
	if k, ok := strings.CutPrefix(r, FeaturePrefix); ok {
		return strings.TrimSpace(k) != ""
	}
	for _, s := range Sections {
		if s.Key == r {
			return true
		}
	}
	return false
}

// List is every profile, by name.
func List() []Profile {
	mu.Lock()
	s := store
	mu.Unlock()
	if s == nil {
		return nil
	}
	var out []Profile
	for _, k := range s.Keys(table) {
		var p Profile
		if s.Get(table, k, &p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get is the profile with id.
func Get(id string) (Profile, bool) {
	mu.Lock()
	s := store
	mu.Unlock()
	var p Profile
	if s == nil || id == "" || !s.Get(table, id, &p) {
		return Profile{}, false
	}
	return p, true
}

// Save keeps p, checked; it needs an ID. A person or a team named on another
// profile is refused, and so is a second default: each person gets one shape
// of gohort, and which one is never a matter of order.
func Save(p Profile) error {
	p = p.Normalize()
	if p.ID == "" {
		return fmt.Errorf("a profile needs an id")
	}
	if err := Check(p); err != nil {
		return err
	}
	for _, other := range List() {
		if other.ID == p.ID {
			continue
		}
		for _, u := range p.Users {
			if names(other.Users, u) {
				return fmt.Errorf("%s is already on the profile %q: each person gets one", u, other.Name)
			}
		}
		for _, team := range p.Teams {
			if names(other.Teams, team) {
				return fmt.Errorf("that team is already on the profile %q: each team gets one", other.Name)
			}
		}
		if p.IsDefault() && other.IsDefault() {
			return fmt.Errorf("%q is already the default profile (it names nobody): name the people or teams this one is for", other.Name)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return fmt.Errorf("profiles are not available here")
	}
	store.Set(table, p.ID, p)
	return nil
}

// Delete removes the profile with id.
func Delete(id string) {
	mu.Lock()
	defer mu.Unlock()
	if store != nil && id != "" {
		store.Unset(table, id)
	}
}

// For is the profile user gets: the one that names them, else the first, by
// name, for a team inTeam says they are on, else the default (the one naming
// no one and no team). ok is false when none applies. inTeam may be nil, for
// a caller that knows no teams.
func For(user string, inTeam func(team string) bool) (Profile, bool) {
	user = strings.TrimSpace(user)
	if user == "" {
		return Profile{}, false
	}
	all := List()
	for _, p := range all {
		if names(p.Users, user) {
			return p, true
		}
	}
	if inTeam != nil {
		for _, p := range all {
			for _, team := range p.Teams {
				if inTeam(team) {
					return p, true
				}
			}
		}
	}
	for _, p := range all {
		if p.IsDefault() {
			return p, true
		}
	}
	return Profile{}, false
}

// ShowsApp reports whether p's dashboard shows the app at path: every app
// when p lists none, else the ones it lists (a listed path covers what is
// under it).
func (p Profile) ShowsApp(path string) bool {
	if len(p.Apps) == 0 {
		return true
	}
	for _, a := range p.Apps {
		a = strings.TrimSuffix(a, "/")
		if path == a || strings.HasPrefix(path, a+"/") {
			return true
		}
	}
	return false
}

// LendsTool reports whether p lends the bridge tool called name to agents:
// every tool when p lists none.
func (p Profile) LendsTool(name string) bool {
	if len(p.Tools) == 0 {
		return true
	}
	return names(p.Tools, name)
}

func names(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}
