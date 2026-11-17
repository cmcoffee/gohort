package admin

// Desktop app profiles (core/desktopprofile): the desktop app shaped for a
// set of people, by an administrator, with nothing rebuilt. A profile names
// its people, what the app is called for them, where it opens, which apps it
// shows, and which of their computer's tools the bridge lends their agents.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/desktopprofile"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

const desktopProfilesSource = "api/desktop-profiles"

func (a *AdminApp) registerDesktopProfileRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+desktopProfilesSource, jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleDesktopProfiles, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleDesktopProfileSave, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleDesktopProfileDelete, a.gate()),
	}.Serve)
	sub.HandleFunc("/"+desktopProfilesSource+"/one", jsonapi.Get(a.db, a.handleDesktopProfileOne, a.gate()))
}

// desktopProfileRow is one profile as the table lists it.
type desktopProfileRow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Who   string `json:"who"`
	Opens string `json:"opens"`
	Apps  string `json:"apps"`
	Tools string `json:"tools"`
}

func (a *AdminApp) handleDesktopProfiles(c *jsonapi.Call) ([]desktopProfileRow, error) {
	out := []desktopProfileRow{}
	for _, p := range desktopprofile.List() {
		row := desktopProfileRow{ID: p.ID, Name: p.Name, Who: strings.Join(p.Users, ", "),
			Opens: p.StartPath, Apps: strings.Join(p.Apps, ", "), Tools: strings.Join(p.Tools, ", ")}
		if row.Who == "" {
			row.Who = "Everyone no other profile names"
		}
		if row.Opens == "" {
			row.Opens = "The dashboard"
		}
		if row.Apps == "" {
			row.Apps = "Every app they may use"
		}
		if row.Tools == "" {
			row.Tools = "All of them"
		}
		out = append(out, row)
	}
	return out, nil
}

// desktopProfileForm is the form's record: the profile with its lists as the
// form's fields hold them.
type desktopProfileForm struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	StartPath string   `json:"start_path"`
	Apps      []string `json:"apps"`
	Tools     []string `json:"tools"`
	Users     []string `json:"users"`
}

func (a *AdminApp) handleDesktopProfileOne(c *jsonapi.Call) (desktopProfileForm, error) {
	p, ok := desktopprofile.Get(strings.TrimSpace(c.R.URL.Query().Get("id")))
	if !ok {
		return desktopProfileForm{}, jsonapi.NotFound("no such profile")
	}
	return desktopProfileForm{ID: p.ID, Name: p.Name, StartPath: p.StartPath, Apps: p.Apps, Tools: p.Tools, Users: p.Users}, nil
}

func (a *AdminApp) handleDesktopProfileSave(c *jsonapi.Call, f desktopProfileForm) (map[string]any, error) {
	p := desktopprofile.Profile{ID: strings.TrimSpace(f.ID), Name: f.Name, StartPath: f.StartPath, Apps: f.Apps, Tools: f.Tools, Users: f.Users}.Normalize()
	if p.ID == "" {
		p.ID = UUIDv4()
	}
	// A name that is nobody's would shape the app for no one, unseen.
	// Matched without case, kept as the account spells it.
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
	}
	if err := desktopprofile.Save(p); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "saved the desktop profile %q", p.Name)
	return map[string]any{"ok": true, "id": p.ID}, nil
}

func (a *AdminApp) handleDesktopProfileDelete(c *jsonapi.Call, _ struct{}) (map[string]any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	p, ok := desktopprofile.Get(id)
	if !ok {
		return nil, jsonapi.NotFound("no such profile")
	}
	desktopprofile.Delete(id)
	adminChange(c.R, activity.Settings, "deleted the desktop profile %q", p.Name)
	return map[string]any{"ok": true}, nil
}

// desktopProfileAppOptions are the apps a profile may show, as the
// dashboard knows them.
func desktopProfileAppOptions() []ui.SelectOption {
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

// desktopProfileFields are the profile form's fields.
func desktopProfileFields() []ui.FormField {
	return []ui.FormField{
		{Field: "id", Type: "hidden"},
		{Field: "name", Label: "Name", Type: "text", Required: true,
			Help: "What the desktop app is called for these people: its window and the top of its dashboard."},
		{Field: "users", Label: "For", Type: "tags", Placeholder: "usernames",
			Help: "The people it is for. Leave empty to make it the default, for everyone no other profile names."},
		{Field: "start_path", Label: "Opens on", Type: "text", Placeholder: "a page of this site, as a path",
			Help: "A page of this deployment, such as an app built for them (/apps/<name>/). Empty opens the dashboard."},
		{Field: "apps", Label: "Apps it shows", Type: "checklist", Options: desktopProfileAppOptions(),
			Help: "The apps on its dashboard. None ticked shows every app they may use. This narrows what is shown, not what they may open."},
		{Field: "tools", Label: "Computer tools it lends", Type: "tags", Placeholder: "tool names",
			Help: "The bridge's tools on their computer their agents may use, by name as the bridge lists them (filesystem_read, contacts_search). Empty lends them all."},
	}
}

// desktopProfilesSection is the section on the People tab.
func desktopProfilesSection() ui.Section {
	return ui.Section{
		Title:    "Desktop app profiles",
		Subtitle: "Shape the desktop app for a set of people: its name, where it opens, its apps and the computer tools it lends. Nothing is rebuilt; a change reaches them on their next page.",
		Detail: "A profile applies to the desktop app only: the same people in a browser get the ordinary dashboard. Each person gets one profile; one that names nobody is the default for everyone else.\n\n" +
			"Opens on is usually an app built for them with Builder, which then is their home: the desktop app's dashboard opens it. Apps it shows narrows the dashboard; what they may open is still decided by their access, under Users.\n\n" +
			"The app's name in the macOS menu bar and its icon come from the installed app itself, and change only with a build of its own.",
		Body: ui.Stack{Children: []ui.Component{
			ui.ModalButton{Label: "＋ New profile", Variant: "primary", Align: "left", Title: "New desktop profile", Width: "600px",
				Body: ui.FormPanel{PostURL: desktopProfilesSource, Method: "POST", SubmitLabel: "Create", Fields: desktopProfileFields(),
					Invalidate: []string{desktopProfilesSource}}},
			ui.Table{
				Source:    desktopProfilesSource,
				RowKey:    "id",
				EmptyText: "No profiles: everyone gets the ordinary desktop app.",
				Columns: []ui.Col{
					{Field: "name", Label: "Name", Flex: 1},
					{Field: "who", Label: "For", Flex: 2},
					{Field: "opens", Label: "Opens on", Flex: 1, Mute: true},
					{Field: "apps", Label: "Apps", Flex: 1, Mute: true},
					{Field: "tools", Label: "Tools", Flex: 1, Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.ModalAction("Edit", ui.FormPanel{Source: desktopProfilesSource + "/one?id={id}", PostURL: desktopProfilesSource,
						Method: "POST", SubmitLabel: "Save", Fields: desktopProfileFields(), Invalidate: []string{desktopProfilesSource}}),
					{Type: "button", Label: "Delete", Variant: "danger", Method: "DELETE", PostTo: desktopProfilesSource + "?id={id}",
						Confirm: "Delete this profile? Its people get the ordinary desktop app (or the default profile)."},
				},
			},
		}},
	}
}
