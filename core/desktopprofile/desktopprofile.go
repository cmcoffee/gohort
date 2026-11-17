// Package desktopprofile is how an administrator shapes the desktop app for
// a set of people without building a client of its own: a profile names who
// it is for, what the app is called for them, where it opens, which apps it
// shows, and which of their computer's tools the bridge lends their agents.
//
// The desktop app is one signed client for everyone. The server knows a
// request comes from it (core.DesktopClientUser, from the key its window's
// proxy adds), so the profile is applied there, per request: the dashboard,
// the start page, the bridge's tools. A profile change reaches every client
// on its next page, with nothing rebuilt or reinstalled.
//
// A leaf: the database arrives through SetStore, and the people a profile
// names are usernames.
package desktopprofile

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

const table = "desktop_profiles"

var (
	mu    sync.Mutex
	store Store
)

// SetStore is where profiles are kept. Until it is called there are none.
func SetStore(s Store) {
	mu.Lock()
	store = s
	mu.Unlock()
}

// Profile is one shape of the desktop app.
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
	// Users are the people it is for, by username. A profile naming nobody is
	// the default: it applies to everyone no other profile names.
	Users []string `json:"users,omitempty"`
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
	return nil
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

// Save keeps p, checked; it needs an ID. A person named on another profile
// is refused: each person gets one shape of the app.
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
		if len(p.Users) == 0 && len(other.Users) == 0 {
			return fmt.Errorf("%q is already the default profile (it names nobody): name the people this one is for", other.Name)
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

// For is the profile user's desktop app takes: the one that names them,
// else the default (the one naming nobody). ok is false when neither exists,
// and the app is the ordinary one.
func For(user string) (Profile, bool) {
	user = strings.TrimSpace(user)
	if user == "" {
		return Profile{}, false
	}
	var def *Profile
	for _, p := range List() {
		p := p
		if names(p.Users, user) {
			return p, true
		}
		if len(p.Users) == 0 && def == nil {
			def = &p
		}
	}
	if def != nil {
		return *def, true
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
