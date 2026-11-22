package admin

// User profiles (core/userprofile): gohort shaped for a set of people, by an
// administrator, with nothing rebuilt. A profile names its people and teams,
// what gohort is called for them, where it opens, which apps it shows, which
// of their computer's tools the desktop bridge lends their agents, and what it
// takes away from them. What it shows applies in the desktop app, and in a
// browser when the profile says so; what it takes away is enforced wherever
// they reach gohort from.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/userprofile"
)

const userProfilesSource = "api/user-profiles"

func (a *AdminApp) registerUserProfileRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+userProfilesSource, jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleUserProfiles, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleUserProfileSave, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleUserProfileDelete, a.gate()),
	}.Serve)
	sub.HandleFunc("/"+userProfilesSource+"/one", jsonapi.Get(a.db, a.handleUserProfileOne, a.gate()))
	sub.HandleFunc("/"+userProfilesSource+"/people", jsonapi.Get(a.db, a.handleUserProfilePeople, a.gate()))
}

// userProfileRow is one profile as the table lists it.
type userProfileRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Who     string `json:"who"`
	Where   string `json:"where"`
	Opens   string `json:"opens"`
	Apps    string `json:"apps"`
	Removes string `json:"removes"`
}

// teamNames are the deployment's teams, by id.
func teamNames() map[string]string {
	out := map[string]string{}
	if AuthDB == nil {
		return out
	}
	for _, g := range LoadAppGroups(AuthDB()) {
		out[g.ID] = g.Name
	}
	return out
}

// removeLabels are what a profile can take away, by key, as the form names
// them: the sections, then each feature Feature Access decides.
func removeLabels() []ui.SelectOption {
	var out []ui.SelectOption
	for _, s := range userprofile.Sections {
		out = append(out, ui.SelectOption{Value: s.Key, Label: s.Label})
	}
	for _, f := range ShareableFeatures() {
		out = append(out, ui.SelectOption{Value: userprofile.FeaturePrefix + f.Key, Label: "Feature: " + f.Label})
	}
	return out
}

// whoOf is who a profile is for, as a person reads it.
func whoOf(p userprofile.Profile, teams map[string]string) string {
	if p.IsDefault() {
		return "Everyone no other profile names"
	}
	parts := append([]string{}, p.Users...)
	for _, id := range p.Teams {
		name := teams[id]
		if name == "" {
			name = "a removed team"
		}
		parts = append(parts, "team "+name)
	}
	return strings.Join(parts, ", ")
}

func (a *AdminApp) handleUserProfiles(c *jsonapi.Call) ([]userProfileRow, error) {
	teams := teamNames()
	labels := map[string]string{}
	for _, o := range removeLabels() {
		labels[o.Value] = o.Label
	}
	out := []userProfileRow{}
	for _, p := range userprofile.List() {
		row := userProfileRow{ID: p.ID, Name: p.Name, Who: whoOf(p, teams),
			Where: "Desktop app", Opens: p.StartPath, Apps: strings.Join(p.Apps, ", ")}
		if p.AlsoWeb {
			row.Where = "Desktop app and browser"
		}
		if row.Opens == "" {
			row.Opens = "The dashboard"
		}
		if row.Apps == "" {
			row.Apps = "Every app they may use"
		}
		var takes []string
		for _, r := range p.Removes {
			if l := labels[r]; l != "" {
				takes = append(takes, strings.SplitN(l, " (", 2)[0])
			} else {
				takes = append(takes, r)
			}
		}
		row.Removes = strings.Join(takes, ", ")
		if row.Removes == "" {
			row.Removes = "Nothing"
		}
		out = append(out, row)
	}
	return out, nil
}

// userProfileForm is the form's record: the profile with its lists as the
// form's fields hold them.
type userProfileForm struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	StartPath string   `json:"start_path"`
	Apps      []string `json:"apps"`
	Tools     []string `json:"tools"`
	Users     []string `json:"users"`
	Teams     []string `json:"teams"`
	AlsoWeb   bool     `json:"also_web"`
	Removes   []string `json:"removes"`
}

func (a *AdminApp) handleUserProfileOne(c *jsonapi.Call) (userProfileForm, error) {
	p, ok := userprofile.Get(strings.TrimSpace(c.R.URL.Query().Get("id")))
	if !ok {
		return userProfileForm{}, jsonapi.NotFound("no such profile")
	}
	return userProfileForm{ID: p.ID, Name: p.Name, StartPath: p.StartPath, Apps: p.Apps, Tools: p.Tools,
		Users: p.Users, Teams: p.Teams, AlsoWeb: p.AlsoWeb, Removes: p.Removes}, nil
}

func (a *AdminApp) handleUserProfileSave(c *jsonapi.Call, f userProfileForm) (map[string]any, error) {
	p := userprofile.Profile{ID: strings.TrimSpace(f.ID), Name: f.Name, StartPath: f.StartPath, Apps: f.Apps, Tools: f.Tools,
		Users: f.Users, Teams: f.Teams, AlsoWeb: f.AlsoWeb, Removes: f.Removes}.Normalize()
	if p.ID == "" {
		p.ID = UUIDv4()
	}
	// A name that is nobody's, or a team that is not one, would shape gohort
	// for no one, unseen. Names are matched without case, kept as the account
	// spells them.
	if AuthDB != nil {
		users := AuthListUsers(AuthDB())
		for i, u := range p.Users {
			found := ""
			for _, acct := range users {
				if strings.EqualFold(acct.Username, u) {
					found = acct.Username
					break
				}
			}
			if found == "" {
				return nil, jsonapi.BadRequest("there is no user named " + u)
			}
			p.Users[i] = found
		}
		teams := teamNames()
		for _, id := range p.Teams {
			if teams[id] == "" {
				return nil, jsonapi.BadRequest("there is no such team (it may have been removed)")
			}
		}
	}
	if err := userprofile.Save(p); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "saved the user profile %q", p.Name)
	return map[string]any{"ok": true, "id": p.ID}, nil
}

func (a *AdminApp) handleUserProfileDelete(c *jsonapi.Call, _ struct{}) (map[string]any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	p, ok := userprofile.Get(id)
	if !ok {
		return nil, jsonapi.NotFound("no such profile")
	}
	userprofile.Delete(id)
	adminChange(c.R, activity.Settings, "deleted the user profile %q", p.Name)
	return map[string]any{"ok": true}, nil
}

// userProfilePerson is one person and the profile they get, with why.
type userProfilePerson struct {
	User    string `json:"user"`
	Profile string `json:"profile"`
	Why     string `json:"why"`
}

// handleUserProfilePeople is every person and the profile they get, so an
// overlap of a name and a team, or a team and the default, is seen rather
// than guessed at.
func (a *AdminApp) handleUserProfilePeople(c *jsonapi.Call) ([]userProfilePerson, error) {
	out := []userProfilePerson{}
	if AuthDB == nil {
		return out, nil
	}
	teams := teamNames()
	for _, u := range AuthListUsers(AuthDB()) {
		row := userProfilePerson{User: u.Username, Profile: "None", Why: "No profile applies"}
		if u.Admin {
			row.Why = "An administrator: no profile shapes them"
			out = append(out, row)
			continue
		}
		if p, ok := userprofile.Of(u.Username); ok {
			row.Profile = p.Name
			row.Why = whyProfile(p, u, teams)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].User) < strings.ToLower(out[j].User) })
	return out, nil
}

// whyProfile is how u came to get p: by name, by one of their teams, or as
// the default.
func whyProfile(p userprofile.Profile, u AuthUser, teams map[string]string) string {
	for _, n := range p.Users {
		if strings.EqualFold(n, u.Username) {
			return "Named on it"
		}
	}
	for _, id := range p.Teams {
		for _, g := range u.Groups {
			if g == id {
				return "On the team " + teams[id]
			}
		}
	}
	return "The default"
}

// userProfileAppOptions are the apps a profile may show, as the dashboard
// knows them.
func userProfileAppOptions() []ui.SelectOption {
	var out []ui.SelectOption
	for _, wa := range AllWebApps() {
		if h, ok := wa.(interface{ WebHidden() bool }); (ok && h.WebHidden()) || wa.WebPath() == "/admin" {
			continue
		}
		out = append(out, ui.SelectOption{Value: wa.WebPath(), Label: wa.WebName()})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// userProfileTeamOptions are the deployment's teams, for the form.
func userProfileTeamOptions() []ui.SelectOption {
	var out []ui.SelectOption
	if AuthDB == nil {
		return out
	}
	for _, g := range LoadAppGroups(AuthDB()) {
		out = append(out, ui.SelectOption{Value: g.ID, Label: g.Name})
	}
	return out
}

// userProfileFields are the profile form's fields.
func userProfileFields() []ui.FormField {
	return []ui.FormField{
		{Field: "id", Type: "hidden"},
		{Field: "name", Label: "Name", Type: "text", Required: true,
			Help: "What gohort is called for these people: the desktop app's window, and the top of their dashboard."},
		{Field: "users", Label: "For", Type: "tags", Placeholder: "usernames",
			Help: "The people it is for. Leave this and Teams empty to make it the default, for everyone no other profile names. Administrators are never shaped by a profile."},
		{Field: "teams", Label: "Teams", Type: "checklist", Options: userProfileTeamOptions(),
			Help: "Everyone on a ticked team gets it, unless a profile names them by name."},
		{Field: "also_web", Label: "In a browser too", Type: "toggle",
			Help: "Off, the name, start page and apps below shape only the desktop app. On, they shape these people's browser as well; on the default profile, that is every browser but an administrator's."},
		{Field: "start_path", Label: "Opens on", Type: "text", Placeholder: "a page of this site, as a path",
			Help: "A page of this deployment, such as an app built for them (/apps/<name>/). Empty opens the dashboard."},
		{Field: "apps", Label: "Apps it shows", Type: "checklist", Options: userProfileAppOptions(),
			Help: "The apps on their dashboard. None ticked shows every app they may use. This narrows what is shown, not what they may open."},
		{Field: "tools", Label: "Computer tools it lends", Type: "tags", Placeholder: "tool names",
			Help: "The desktop bridge's tools on their computer their agents may use, by name as the bridge lists them (filesystem_read, contacts_search). Empty lends them all."},
		{Field: "removes", Label: "Takes away", Type: "checklist", Options: removeLabels(),
			Help: "Gone from their pages and refused at the server, in the desktop app, a browser and over an API alike. Taking API keys away stops the keys they have from working; the keys are kept, and work again if the profile stops taking them. A profile only takes away: what they may do is still their access's to decide."},
	}
}

// userProfilesSection is the section on the People tab.
func userProfilesSection() ui.Section {
	return ui.Section{
		Title:    "User profiles",
		Subtitle: "Shape gohort for a set of people: its name, where it opens, its apps, the computer tools it lends, and what it takes away. Nothing is rebuilt; a change reaches them on their next page.",
		Detail: "Each person gets one profile: one that names them, else one for a team they are on, else the default (one naming no one and no team). Administrators are never shaped by one. Who gets which lists every person and why.\n\n" +
			"What a profile shows applies in the desktop app, and in a browser when \"In a browser too\" is on. What it takes away holds everywhere.\n\n" +
			"Opens on is usually an app built for them with Builder, which then is their home. Apps it shows narrows the dashboard; what they may open is still decided by their access, under Users.\n\n" +
			"The desktop app's name in the macOS menu bar and its icon come from the installed app itself, and change only with a build of its own.",
		Body: ui.Stack{Children: []ui.Component{
			ui.ModalButton{Label: "＋ New profile", Variant: "primary", Align: "left", Title: "New user profile", Width: "600px",
				Body: ui.FormPanel{PostURL: userProfilesSource, Method: "POST", SubmitLabel: "Create", Fields: userProfileFields(),
					Invalidate: []string{userProfilesSource, userProfilesSource + "/people"}}},
			ui.Table{
				Source:    userProfilesSource,
				RowKey:    "id",
				EmptyText: "No profiles: everyone gets the ordinary gohort.",
				Columns: []ui.Col{
					{Field: "name", Label: "Name", Flex: 1},
					{Field: "who", Label: "For", Flex: 2},
					{Field: "where", Label: "Where", Flex: 1, Mute: true},
					{Field: "opens", Label: "Opens on", Flex: 1, Mute: true},
					{Field: "apps", Label: "Apps", Flex: 1, Mute: true},
					{Field: "removes", Label: "Takes away", Flex: 1, Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.ModalAction("Edit", ui.FormPanel{Source: userProfilesSource + "/one?id={id}", PostURL: userProfilesSource,
						Method: "POST", SubmitLabel: "Save", Fields: userProfileFields(), Invalidate: []string{userProfilesSource, userProfilesSource + "/people"}}),
					{Type: "button", Label: "Delete", Variant: "danger", Method: "DELETE", PostTo: userProfilesSource + "?id={id}",
						Confirm: "Delete this profile? Its people get the ordinary gohort (or the default profile), and what it took away comes back."},
				},
			},
			ui.ModalButton{Label: "Who gets which", Align: "left", Title: "Who gets which profile", Width: "600px",
				Body: ui.Table{
					Source:    userProfilesSource + "/people",
					RowKey:    "user",
					EmptyText: "No people yet.",
					Columns: []ui.Col{
						{Field: "user", Label: "Person", Flex: 1},
						{Field: "profile", Label: "Profile", Flex: 1},
						{Field: "why", Label: "Why", Flex: 2, Mute: true},
					},
				}},
		}},
	}
}
